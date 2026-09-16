package linkcore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/danielpaulus/go-ios/ios"
	"github.com/danielpaulus/go-ios/ios/installationproxy"
	"github.com/danielpaulus/go-ios/ios/tunnel"
	"github.com/danielpaulus/go-ios/ios/zipconduit"
)

type Session struct {
	device    ios.DeviceEntry
	tunnel    tunnel.Tunnel
	closeOnce sync.Once
}

var errInstallStalled = errors.New("install made no progress")

func isRetryableInstallTransferError(err error) bool {
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET) || errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.ETIMEDOUT) {
		return true
	}
	var networkError net.Error
	return errors.As(err, &networkError) && (networkError.Timeout() || networkError.Temporary())
}

func finishInstallTransferError(diagnostic *installTransferDiagnostic, message string, diagnosticCode string, err error) error {
	if isRetryableInstallTransferError(err) {
		diagnostic.finish("retryable", "error_code", "transient_transport_interrupted")
		return coded("install_retryable", message, err)
	}
	diagnostic.finish("failed", "error_code", diagnosticCode)
	return coded("install_failed", message, err)
}

type installProgressTracker struct {
	mu sync.Mutex

	phase         string
	bytesSent     uint64
	devicePercent int
	deviceStatus  string
}

func newInstallProgressTracker() *installProgressTracker {
	return &installProgressTracker{devicePercent: -1}
}

func (tracker *installProgressTracker) transferAdvanced(phase string, bytesSent uint64) bool {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	advanced := phase != tracker.phase || bytesSent > tracker.bytesSent
	tracker.phase = phase
	if bytesSent > tracker.bytesSent {
		tracker.bytesSent = bytesSent
	}
	return advanced
}

func (tracker *installProgressTracker) deviceAdvanced(percent int, status string) bool {
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	advanced := tracker.phase != "device_installing" ||
		percent > tracker.devicePercent ||
		(status != "" && status != tracker.deviceStatus)
	tracker.phase = "device_installing"
	if percent > tracker.devicePercent {
		tracker.devicePercent = percent
	}
	if status != "" {
		tracker.deviceStatus = status
	}
	return advanced
}

type installActivityMonitor struct {
	mu sync.Mutex

	last time.Time
	wake chan struct{}
}

func newInstallActivityMonitor() *installActivityMonitor {
	return &installActivityMonitor{last: time.Now(), wake: make(chan struct{}, 1)}
}

func (monitor *installActivityMonitor) signal() {
	monitor.mu.Lock()
	monitor.last = time.Now()
	monitor.mu.Unlock()
	select {
	case monitor.wake <- struct{}{}:
	default:
	}
}

func (monitor *installActivityMonitor) lastActivity() time.Time {
	monitor.mu.Lock()
	defer monitor.mu.Unlock()
	return monitor.last
}

func resetInstallTimer(timer *time.Timer, duration time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(duration)
}

func waitForInstallResult(ctx context.Context, result <-chan error, activity *installActivityMonitor, stallTimeout time.Duration) error {
	timer := time.NewTimer(stallTimeout)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-result:
			return err
		case <-activity.wake:
			resetInstallTimer(timer, stallTimeout)
		case <-timer.C:
			remaining := stallTimeout - time.Since(activity.lastActivity())
			if remaining > 0 {
				timer.Reset(remaining)
				continue
			}
			select {
			case err := <-result:
				return err
			default:
			}
			return errInstallStalled
		}
	}
}

func finishInstallContextError(diagnostic *installTransferDiagnostic, err error) error {
	result := "cancelled"
	cause := "daemon_stop"
	message := "installation stopped because Deploy Link is shutting down"
	if errors.Is(err, context.DeadlineExceeded) {
		result = "timed_out"
		cause = "hard_safety_limit"
		message = "installation reached its hard safety limit"
	}
	diagnostic.finish(result, "cancel_reason", cause)
	return coded("install_failed", message, err)
}

func classifyTunnelError(err error) error {
	message := strings.ToLower(err.Error())
	switch {
	case strings.Contains(message, "connection refused"),
		strings.Contains(message, "dial timed out"),
		strings.Contains(message, "i/o timeout"),
		strings.Contains(message, "no route to host"):
		return coded("remote_pairing_cold", "Tailnet is reachable but iOS exposes no usable RemotePairing listener", err)
	case strings.Contains(message, "pair verification failed"), strings.Contains(message, "saved identity was rejected"):
		return coded("pair_verify_failed", "the endpoint rejected the configured pairing record", err)
	default:
		return coded("tunnel_failed", "could not establish the trusted userspace tunnel", err)
	}
}

func OpenSession(ctx context.Context, config Config) (*Session, error) {
	pairRecords, err := tunnel.NewPairRecordManagerFromPymobiledevice3(config.PairRecordPath)
	if err != nil {
		return nil, coded("pair_record_invalid", "could not load the RemotePairing identity", err)
	}
	device := ios.DeviceEntry{Properties: ios.DeviceProperties{
		SerialNumber:   config.RemoteIdentifier,
		ConnectionType: "Network",
	}}
	tun, err := tunnel.ConnectRemotePairingTCPUserspace(
		ctx,
		device,
		config.TargetTailnetIP,
		config.RemotePairingPort,
		config.LocalForwardPort,
		pairRecords,
	)
	if err != nil {
		return nil, classifyTunnelError(err)
	}

	device.Address = tun.Address
	device.UserspaceTUN = true
	device.UserspaceTUNHost = "127.0.0.1"
	device.UserspaceTUNPort = config.LocalForwardPort
	rsdService, err := ios.NewWithAddrPortDevice(tun.Address, tun.RsdPort, device)
	if err != nil {
		_ = tun.Close()
		return nil, coded("tunnel_failed", "could not connect to RSD", err)
	}
	type handshakeResult struct {
		response ios.RsdHandshakeResponse
		err      error
	}
	handshake := make(chan handshakeResult, 1)
	go func() {
		response, handshakeErr := rsdService.Handshake()
		handshake <- handshakeResult{response: response, err: handshakeErr}
	}()
	var rsd ios.RsdHandshakeResponse
	select {
	case <-ctx.Done():
		_ = rsdService.Close()
		_ = tun.Close()
		return nil, coded("tunnel_failed", "RSD handshake timed out", ctx.Err())
	case result := <-handshake:
		_ = rsdService.Close()
		if result.err != nil {
			_ = tun.Close()
			return nil, coded("tunnel_failed", "RSD handshake failed", result.err)
		}
		rsd = result.response
	}
	if rsd.Udid != config.RemoteIdentifier {
		_ = tun.Close()
		return nil, coded("rsd_identity_mismatch", "RSD identity differs from the configured device", nil)
	}
	device.Rsd = rsd
	session := &Session{device: device, tunnel: tun}
	if _, err := session.browseUserApps(ctx); err != nil {
		_ = session.Close()
		return nil, coded("tunnel_failed", "InstallationProxy readiness check failed", err)
	}
	return session, nil
}

func (s *Session) Close() error {
	var closeErr error
	s.closeOnce.Do(func() {
		closeErr = s.tunnel.Close()
	})
	return closeErr
}

func (s *Session) Done() <-chan error {
	return s.tunnel.Done()
}

func (s *Session) browseUserApps(ctx context.Context) ([]installationproxy.AppInfo, error) {
	proxy, err := installationproxy.NewWithContext(ctx, s.device)
	if err != nil {
		return nil, err
	}
	type browseResult struct {
		apps []installationproxy.AppInfo
		err  error
	}
	result := make(chan browseResult, 1)
	go func() {
		apps, browseErr := proxy.BrowseUserApps()
		result <- browseResult{apps: apps, err: browseErr}
	}()
	select {
	case <-ctx.Done():
		proxy.Close()
		return nil, ctx.Err()
	case response := <-result:
		proxy.Close()
		return response.apps, response.err
	}
}

func verifyInstallReadback(expected AppBundle, apps []installationproxy.AppInfo) (string, error) {
	bundleFound := false
	for _, installed := range apps {
		if installed.CFBundleIdentifier() != expected.BundleIdentifier {
			continue
		}
		bundleFound = true
		if expected.Version != "" && installed.CFBundleShortVersionString() != expected.Version {
			continue
		}
		installedBuild, _ := installed[installationproxy.CFBundleVersion].(string)
		if expected.Build != "" && installedBuild != expected.Build {
			continue
		}
		return "", nil
	}
	if bundleFound {
		return "readback_identity_mismatch", coded("install_failed", "installed app version or build did not match the requested artifact", errors.New("installed app identity mismatch"))
	}
	return "readback_missing", coded("install_failed", "installed bundle was absent from InstallationProxy readback", errors.New(expected.BundleIdentifier))
}

func (s *Session) Install(ctx context.Context, app AppBundle, progress func(int, string), transferProgress func(string, uint64, uint64), stallTimeout time.Duration) error {
	diagnostic := newInstallTransferDiagnostic(ctx)
	diagnostic.begin("conduit_connecting")
	conduit, err := zipconduit.NewWithShimConnectionContext(ctx, s.device)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return finishInstallContextError(diagnostic, ctxErr)
		}
		return finishInstallTransferError(diagnostic, "could not connect to streaming zip conduit", "conduit_connect_failed", err)
	}
	diagnostic.begin("conduit_connected")
	installResult := make(chan error, 1)
	activity := newInstallActivityMonitor()
	tracker := newInstallProgressTracker()
	var lastBytesSent uint64
	var lastBytesTotal uint64
	go func() {
		installResult <- conduit.SendFileWithTransferProgress(app.Path, func(percent int, status string) {
			if tracker.deviceAdvanced(percent, status) {
				activity.signal()
			}
			diagnostic.deviceProgress(percent, status)
			if progress != nil {
				progress(percent, status)
			}
		}, func(phase string, bytesSent uint64, bytesTotal uint64) {
			advanced := tracker.transferAdvanced(phase, bytesSent)
			if advanced {
				activity.signal()
			}
			lastBytesSent = bytesSent
			lastBytesTotal = bytesTotal
			diagnostic.transferProgress(phase, bytesSent, bytesTotal)
			if advanced && transferProgress != nil {
				transferProgress(phase, bytesSent, bytesTotal)
			}
		})
	}()
	err = waitForInstallResult(ctx, installResult, activity, stallTimeout)
	_ = conduit.Close()
	if errors.Is(err, errInstallStalled) {
		diagnostic.finish("stalled", "cancel_reason", "no_forward_progress")
		return coded("install_stalled", "installation stopped after making no progress", err)
	}
	if ctx.Err() != nil {
		return finishInstallContextError(diagnostic, ctx.Err())
	}
	if err != nil {
		return finishInstallTransferError(diagnostic, "iOS rejected or interrupted the app transfer", "transfer_or_device_failed", err)
	}

	diagnostic.readback()
	if transferProgress != nil {
		transferProgress("readback", lastBytesSent, lastBytesTotal)
	}
	readbackCtx, readbackCancel := context.WithTimeout(ctx, 30*time.Second)
	apps, err := s.browseUserApps(readbackCtx)
	readbackCancel()
	if err != nil {
		diagnostic.finish("failed", "error_code", "readback_failed")
		return coded("install_failed", "post-install InstallationProxy readback failed", err)
	}
	readbackCode, err := verifyInstallReadback(app, apps)
	if err != nil {
		diagnostic.finish("failed", "error_code", readbackCode)
		return err
	}
	diagnostic.finish("completed", "", "")
	return nil
}

func (s *Session) String() string {
	return fmt.Sprintf("session<userspace=%t>", s.device.UserspaceTUN)
}
