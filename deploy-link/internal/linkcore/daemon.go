package linkcore

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	StateWaiting    = "waiting_for_listener"
	StateConnecting = "connecting"
	StateActive     = "active"
	StateRecovering = "recovering"
	StateInstalling = "installing"
	StateLost       = "session_lost"
	StateStopping   = "stopping"
)

const installTransferMaximumAttempts = 3

type Daemon struct {
	config       Config
	profilePath  string
	socketPath   string
	daemonID     string
	openSession  func(context.Context, Config) (sessionHandle, error)
	discoverPort func(context.Context, Config) (int, error)
	validateApp  func(context.Context, string) (AppBundle, error)
	writeJob     func(string, InstallJob) error
	now          func() time.Time
	retryDelay   func(int) time.Duration

	mu              sync.RWMutex
	operationMu     sync.Mutex
	state           string
	generation      uint64
	sessionLosses   uint64
	installCount    uint64
	installAttempts uint64
	lastErrorCode   string
	sessionEnded    bool
	nextDiscovery   time.Time
	discoveryFails  uint
	session         sessionHandle
	cancel          context.CancelFunc
}

type sessionHandle interface {
	Close() error
	Done() <-chan error
	Install(context.Context, AppBundle, func(int, string), func(string, uint64, uint64), time.Duration) error
}

func NewDaemon(config Config, profilePath string) (*Daemon, error) {
	socketPath, err := SocketPath(profilePath)
	if err != nil {
		return nil, err
	}
	daemonID, err := newInstallJobID()
	if err != nil {
		return nil, coded("daemon_create_failed", "could not create the daemon session identifier", err)
	}
	return &Daemon{
		config:      config,
		profilePath: profilePath,
		socketPath:  socketPath,
		daemonID:    daemonID,
		state:       StateWaiting,
		openSession: func(ctx context.Context, config Config) (sessionHandle, error) {
			return OpenSession(ctx, config)
		},
		discoverPort: discoverRemotePairingPort,
		validateApp:  ValidateApp,
		writeJob:     writeInstallJob,
		now:          time.Now,
		retryDelay: func(completedAttempts int) time.Duration {
			return time.Duration(completedAttempts) * time.Second
		},
	}, nil
}

func (d *Daemon) Snapshot() Snapshot {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return Snapshot{
		State:         d.state,
		DaemonID:      d.daemonID,
		Generation:    d.generation,
		SessionLosses: d.sessionLosses,
		InstallCount:  d.installCount,
		LastErrorCode: d.lastErrorCode,
	}
}

func (d *Daemon) setState(state string, lastErrorCode string) {
	d.mu.Lock()
	d.state = state
	d.lastErrorCode = lastErrorCode
	d.mu.Unlock()
}

func (d *Daemon) currentSession() sessionHandle {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.session
}

func (d *Daemon) openWithTimeout(ctx context.Context, config Config) (sessionHandle, error) {
	connectCtx, cancel := context.WithTimeout(ctx, config.ConnectTimeout())
	defer cancel()
	return d.openSession(connectCtx, config)
}

func (d *Daemon) discoveryDue() bool {
	return d.nextDiscovery.IsZero() || !d.now().Before(d.nextDiscovery)
}

func (d *Daemon) recordDiscovery(success bool) {
	if success {
		d.discoveryFails = 0
		d.nextDiscovery = d.now().Add(remotePairingDiscoverySuccessInterval)
		return
	}
	d.discoveryFails++
	delay := remotePairingDiscoveryRetryInitial
	for attempt := uint(1); attempt < d.discoveryFails && delay < remotePairingDiscoveryRetryMaximum; attempt++ {
		delay *= 2
		if delay > remotePairingDiscoveryRetryMaximum {
			delay = remotePairingDiscoveryRetryMaximum
		}
	}
	d.nextDiscovery = d.now().Add(delay)
}

func (d *Daemon) resetDiscovery() {
	d.discoveryFails = 0
	d.nextDiscovery = time.Time{}
}

func (d *Daemon) establish(ctx context.Context) {
	d.operationMu.Lock()
	defer d.operationMu.Unlock()
	if d.currentSession() != nil || ctx.Err() != nil {
		return
	}
	d.setState(StateConnecting, "")
	config := d.config
	session, err := d.openWithTimeout(ctx, config)
	if err != nil && errorCode(err) == "remote_pairing_cold" && d.discoveryDue() {
		discoveryCtx, cancel := context.WithTimeout(ctx, remotePairingDiscoveryTimeout)
		port, discoveryErr := d.discoverPort(discoveryCtx, config)
		cancel()
		d.recordDiscovery(discoveryErr == nil)
		if discoveryErr == nil {
			config.RemotePairingPort = port
			slog.Info("Deploy Link discovered a RemotePairing listener", "port", port)
			session, err = d.openWithTimeout(ctx, config)
			if err == nil {
				d.config.RemotePairingPort = port
			}
		}
	}
	if err != nil {
		code := errorCode(err)
		d.setState(StateWaiting, code)
		if code != "remote_pairing_cold" {
			slog.Warn("Deploy Link acquisition failed", "errorCode", code)
		}
		return
	}
	d.mu.Lock()
	d.session = session
	d.sessionEnded = false
	d.resetDiscovery()
	d.generation++
	d.state = StateActive
	d.lastErrorCode = ""
	generation := d.generation
	d.mu.Unlock()
	slog.Info("Deploy Link session active", "generation", generation)
}

func (d *Daemon) loseSession(generation uint64, err error) {
	d.mu.Lock()
	if d.session == nil || d.generation != generation {
		d.mu.Unlock()
		return
	}
	session := d.session
	d.session = nil
	d.sessionEnded = false
	d.state = StateLost
	d.lastErrorCode = "session_lost"
	d.sessionLosses++
	d.mu.Unlock()
	if session != nil {
		_ = session.Close()
	}
	slog.Warn("Deploy Link outer tunnel stopped", "generation", generation, "error", err)
}

func (d *Daemon) markSessionEnded(session sessionHandle, generation uint64) {
	d.mu.Lock()
	if d.session == session && d.generation == generation {
		d.sessionEnded = true
	}
	d.mu.Unlock()
}

func (d *Daemon) sessionCanRetry(session sessionHandle, generation uint64) bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.session == session && d.generation == generation && !d.sessionEnded
}

func waitContext(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func (d *Daemon) connectionLoop(ctx context.Context) {
	for ctx.Err() == nil {
		if d.currentSession() == nil {
			d.setState(StateWaiting, d.Snapshot().LastErrorCode)
			d.establish(ctx)
			if d.currentSession() == nil && !waitContext(ctx, d.config.RetryInterval()) {
				return
			}
			continue
		}
		session := d.currentSession()
		generation := d.Snapshot().Generation
		select {
		case <-ctx.Done():
			return
		case err, ok := <-session.Done():
			if ctx.Err() != nil {
				return
			}
			if !ok && err == nil {
				err = errors.New("outer userspace tunnel stopped")
			}
			d.markSessionEnded(session, generation)
			d.operationMu.Lock()
			d.loseSession(generation, err)
			d.operationMu.Unlock()
		}
	}
}

func (d *Daemon) install(ctx context.Context, appPath string, progress func(int, string)) error {
	return d.installRequest(ctx, Request{Command: "install", AppPath: appPath}, nil, progress, nil)
}

func (d *Daemon) installRequest(
	ctx context.Context,
	request Request,
	accepted func(),
	progress func(int, string),
	transferProgress func(Response),
) error {
	installCtx, cancel := context.WithTimeout(ctx, d.config.InstallHardTimeout())
	defer cancel()
	validationCtx, validationCancel := context.WithTimeout(installCtx, 30*time.Second)
	app, err := d.validateApp(validationCtx, request.AppPath)
	validationCancel()
	if err != nil {
		return err
	}
	d.operationMu.Lock()
	defer d.operationMu.Unlock()
	if err := installCtx.Err(); err != nil {
		return coded("install_failed", "installation expired while waiting for the current operation", err)
	}
	job, err := d.validateInstallOwnership(installCtx, request, app)
	if err != nil {
		return err
	}
	if job != nil {
		claimed, err := claimInstallJob(d.profilePath, job.ID, d.Snapshot())
		if err != nil {
			return err
		}
		job = &claimed
	}
	session := d.currentSession()
	if session == nil {
		err := coded("session_not_active", "daemon is waiting for a usable RemotePairing listener", nil)
		if job != nil {
			_, err = markInstallJobTerminal(d.profilePath, *job, InstallJobStateInterrupted, err)
		}
		return err
	}
	d.mu.Lock()
	d.installAttempts++
	installAttempt := d.installAttempts
	generation := d.generation
	d.mu.Unlock()
	d.setState(StateInstalling, "")
	if accepted != nil {
		accepted()
	}
	installStartedAt := d.now()
	installCtx = withInstallDiagnosticContext(installCtx, installAttempt, generation, installStartedAt)
	var receiptWriteError error
	var lastReceiptWrite time.Time
	var lastDirectProgress time.Time
	lastDirectPhase := ""
	lastDirectPercent := -1
	writeProgress := func(next InstallJobProgress, force bool) {
		if job == nil || receiptWriteError != nil {
			return
		}
		now := time.Now().UTC()
		if !force && !lastReceiptWrite.IsZero() && now.Sub(lastReceiptWrite) < time.Second {
			return
		}
		updated := *job
		updated.Progress = next
		updated.UpdatedAt = now
		receiptWriteError = d.writeJob(d.profilePath, updated)
		if receiptWriteError != nil {
			cancel()
			return
		}
		*job = updated
		lastReceiptWrite = now
	}
	reportTransferProgress := func(phase string, bytesSent uint64, bytesTotal uint64) {
		percent := 0
		if bytesTotal > 0 {
			percent = int(bytesSent * 100 / bytesTotal)
		}
		status := "Transferring app"
		if phase == "payload_complete" || phase == "awaiting_device_progress" {
			percent = 100
			status = "Waiting for device"
		} else if phase == "readback" {
			percent = 100
			status = "Verifying installed app"
		}
		now := d.now()
		if transferProgress != nil && (phase != lastDirectPhase || percent != lastDirectPercent || now.Sub(lastDirectProgress) >= time.Second) {
			elapsed := now.Sub(installStartedAt)
			if elapsed < 0 {
				elapsed = 0
			}
			var throughput uint64
			if elapsed > 0 {
				throughput = uint64(float64(bytesSent) / elapsed.Seconds())
			}
			transferProgress(Response{
				OK:            true,
				Final:         false,
				ControlSchema: ControlSchemaVersion,
				Event:         "install_transfer",
				State:         StateInstalling,
				Percent:       percent,
				Status:        status,
				Phase:         phase,
				BytesSent:     bytesSent,
				BytesTotal:    bytesTotal,
				ElapsedMS:     elapsed.Milliseconds(),
				ThroughputBPS: throughput,
			})
			lastDirectPhase = phase
			lastDirectPercent = percent
			lastDirectProgress = now
		}
		if job == nil || receiptWriteError != nil {
			return
		}
		current := job.Progress
		advanced := phase != current.Phase || bytesSent > current.BytesSent
		if !advanced {
			return
		}
		jobPercent := percent * 90 / 100
		if phase == "payload_complete" || phase == "awaiting_device_progress" {
			jobPercent = 90
		} else if phase == "readback" {
			jobPercent = 99
		}
		writeProgress(InstallJobProgress{
			Percent:    jobPercent,
			Status:     status,
			Phase:      phase,
			BytesSent:  bytesSent,
			BytesTotal: bytesTotal,
		}, phase != current.Phase || (bytesTotal > 0 && bytesSent >= bytesTotal))
	}
	reportProgress := func(percent int, status string) {
		if job != nil {
			overallPercent := 90 + percent*9/100
			if overallPercent > 99 {
				overallPercent = 99
			}
			writeProgress(InstallJobProgress{
				Percent:    overallPercent,
				Status:     status,
				Phase:      "device_installing",
				BytesSent:  job.Progress.BytesSent,
				BytesTotal: job.Progress.BytesTotal,
			}, true)
		}
		if progress != nil {
			progress(percent, status)
		}
	}
	for transferAttempt := 1; transferAttempt <= installTransferMaximumAttempts; transferAttempt++ {
		err = session.Install(installCtx, app, reportProgress, reportTransferProgress, d.config.InstallStallTimeout())
		if err == nil || errorCode(err) != "install_retryable" {
			break
		}
		if transferAttempt == installTransferMaximumAttempts || installCtx.Err() != nil ||
			!d.sessionCanRetry(session, generation) {
			err = coded("install_failed", "the app transfer was interrupted repeatedly", err)
			break
		}
		var retryBytesTotal uint64
		if job != nil {
			retryBytesTotal = job.Progress.BytesTotal
		}
		writeProgress(InstallJobProgress{
			Percent:    0,
			Status:     "Retrying app transfer",
			Phase:      "retrying",
			BytesTotal: retryBytesTotal,
		}, true)
		slog.Warn("Deploy Link retrying transient install transfer", "generation", generation,
			"install_attempt", installAttempt, "transfer_attempt", transferAttempt+1)
		if !waitContext(installCtx, d.retryDelay(transferAttempt)) {
			err = coded("install_failed", "installation stopped before the transfer retry", installCtx.Err())
			break
		}
	}
	if receiptWriteError != nil {
		err = receiptWriteError
	}
	if err != nil {
		if ctx.Err() != nil {
			d.setState(StateActive, "")
		} else {
			d.setState(StateRecovering, "service_unresponsive")
			slog.Warn("Deploy Link install service failed; retaining outer tunnel", "generation", generation, "install_attempt", installAttempt, "errorCode", errorCode(err))
		}
		if job != nil {
			state := InstallJobStateFailed
			if ctx.Err() != nil {
				state = InstallJobStateInterrupted
			}
			_, err = markInstallJobTerminal(d.profilePath, *job, state, err)
		}
		return err
	}
	d.mu.Lock()
	d.installCount++
	d.state = StateActive
	d.lastErrorCode = ""
	d.mu.Unlock()
	if job != nil {
		snapshot := d.Snapshot()
		if snapshot.DaemonID != job.Baseline.DaemonID || snapshot.Generation != job.Baseline.Generation || snapshot.InstallCount != job.Baseline.InstallCount+1 {
			_, err := markInstallJobTerminal(d.profilePath, *job, InstallJobStateFailed,
				coded("job_proof_failed", "install completion counters did not match the submitted job", nil))
			return err
		}
		job.Progress.Percent = 100
		job.Progress.Status = "InstallComplete"
		job.Progress.Phase = "installed"
		job.Result = &InstallJobResult{Event: "installed", DaemonID: snapshot.DaemonID, Generation: snapshot.Generation, InstallCount: snapshot.InstallCount}
		_, err := markInstallJobTerminal(d.profilePath, *job, InstallJobStateInstalled, nil)
		if err != nil {
			return err
		}
	}
	return nil
}

func (d *Daemon) validateInstallOwnership(ctx context.Context, request Request, app AppBundle) (*InstallJob, error) {
	active, found, err := activeInstallJob(d.profilePath)
	if err != nil {
		return nil, err
	}
	if request.JobID == "" {
		if found {
			return nil, coded("job_active", "this profile has an active unattended install job", nil)
		}
		return nil, nil
	}
	if !found || active.ID != request.JobID || active.State != InstallJobStateQueued {
		return nil, coded("job_invalid", "install request does not own the queued install job", nil)
	}
	expectedPath, err := installJobArtifactPath(d.profilePath, request.JobID)
	if err != nil {
		return nil, err
	}
	if filepath.Clean(request.AppPath) != filepath.Clean(expectedPath) {
		return nil, coded("job_invalid", "install job requested a different app snapshot", nil)
	}
	if app.BundleIdentifier != active.Artifact.BundleIdentifier || app.Version != active.Artifact.Version || app.Build != active.Artifact.Build {
		return nil, coded("job_snapshot_changed", "install job app identity no longer matches its receipt", nil)
	}
	digest, err := digestInstallJobArtifact(ctx, expectedPath)
	if err != nil {
		return nil, err
	}
	if digest != active.Artifact.ContentSHA256 {
		return nil, coded("job_snapshot_changed", "install job app contents no longer match its receipt", nil)
	}
	return &active, nil
}

func (d *Daemon) handleConnection(ctx context.Context, connection net.Conn) {
	defer connection.Close()
	request, err := readRequest(connection)
	if err != nil {
		_ = writeOneResponse(connection, errorResponse(err))
		return
	}
	if err := validateCommand(request); err != nil {
		_ = writeOneResponse(connection, errorResponse(err))
		return
	}
	encoder := &safeEncoder{connection: connection, encoder: jsonEncoder(connection)}
	switch request.Command {
	case "status":
		_ = encoder.Encode(snapshotResponse(d.Snapshot()))
	case "stop":
		_ = encoder.Encode(Response{OK: true, Final: true, ControlSchema: ControlSchemaVersion, Event: "stopping", State: StateStopping})
		if d.cancel != nil {
			d.cancel()
		}
	case "install":
		installCtx, cancelInstall := context.WithCancel(ctx)
		defer cancelInstall()
		go func() {
			var buffer [1]byte
			_, _ = connection.Read(buffer[:])
			cancelInstall()
		}()
		encode := func(response Response) {
			if err := encoder.Encode(response); err != nil {
				cancelInstall()
			}
		}
		err := d.installRequest(installCtx, request, func() {
			encode(Response{OK: true, Final: false, ControlSchema: ControlSchemaVersion, Event: "accepted", State: d.Snapshot().State})
		}, func(percent int, status string) {
			encode(Response{OK: true, Final: false, ControlSchema: ControlSchemaVersion, Event: "progress", Percent: percent, Status: status})
		}, encode)
		if err != nil {
			_ = encoder.Encode(errorResponse(err))
			return
		}
		snapshot := d.Snapshot()
		_ = encoder.Encode(Response{
			OK:            true,
			Final:         true,
			ControlSchema: ControlSchemaVersion,
			Event:         "installed",
			State:         snapshot.State,
			DaemonID:      snapshot.DaemonID,
			Generation:    snapshot.Generation,
			InstallCount:  snapshot.InstallCount,
		})
	}
}

func jsonEncoder(connection net.Conn) *json.Encoder {
	return json.NewEncoder(connection)
}

func (d *Daemon) Serve(parent context.Context) error {
	ctx, cancel := context.WithCancel(parent)
	d.cancel = cancel
	defer cancel()
	daemonLock, err := acquireAdvisoryLock(
		daemonLockPath(d.socketPath),
		"deploy_link_already_running",
		"another Deploy Link process owns this profile",
	)
	if err != nil {
		return err
	}
	defer daemonLock.Close()
	forwardLock, err := acquireAdvisoryLock(
		forwardPortLockPath(d.config.LocalForwardPort),
		"local_forward_port_conflict",
		"another Deploy Link profile owns the configured local forward port",
	)
	if err != nil {
		return err
	}
	defer forwardLock.Close()
	if err := reconcileRunningInstallJob(d.profilePath); err != nil {
		return err
	}
	listener, err := prepareUnixListener(d.socketPath)
	if err != nil {
		return err
	}
	defer func() {
		_ = listener.Close()
		_ = os.Remove(d.socketPath)
	}()
	defer func() {
		cancel()
		d.setState(StateStopping, "")
		d.operationMu.Lock()
		if session := d.currentSession(); session != nil {
			_ = session.Close()
			d.mu.Lock()
			d.session = nil
			d.mu.Unlock()
		}
		d.operationMu.Unlock()
	}()
	go d.connectionLoop(ctx)
	go func() {
		<-ctx.Done()
		_ = listener.Close()
	}()
	slog.Info("Deploy Link daemon listening")
	for {
		connection, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				break
			}
			return coded("control_socket_failed", "control socket accept failed", err)
		}
		go d.handleConnection(ctx, connection)
	}
	return nil
}
