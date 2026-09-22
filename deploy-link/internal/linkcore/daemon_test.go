package linkcore

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestDirectInstallStreamsPayloadProgress(t *testing.T) {
	daemon := testDaemon(t)
	daemon.validateApp = func(context.Context, string) (AppBundle, error) {
		return AppBundle{Path: "/tmp/Test.app", BundleIdentifier: "one.example.test"}, nil
	}
	now := time.Unix(1_800_000_000, 0)
	daemon.now = func() time.Time { return now }
	daemon.session = &fakeSession{install: func(_ context.Context, _ AppBundle, progress func(int, string), transferProgress func(string, uint64, uint64), _ time.Duration) error {
		transferProgress("zip_payload", 50, 100)
		now = now.Add(2 * time.Second)
		transferProgress("zip_payload", 75, 100)
		progress(20, "InspectingPackage")
		return nil
	}}
	daemon.state = StateActive

	server, client := net.Pipe()
	done := make(chan struct{})
	go func() {
		daemon.handleConnection(context.Background(), server)
		close(done)
	}()
	defer client.Close()
	if err := json.NewEncoder(client).Encode(Request{Command: "install", AppPath: "/tmp/Test.app"}); err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(client)
	var responses []Response
	for {
		var response Response
		if err := decoder.Decode(&response); err != nil {
			t.Fatal(err)
		}
		responses = append(responses, response)
		if response.Final {
			break
		}
	}
	if len(responses) != 5 {
		t.Fatalf("responses = %#v, want accepted, two transfer updates, device progress, and installed", responses)
	}
	first := responses[1]
	if first.Event != "install_transfer" || first.Phase != "zip_payload" || first.Percent != 50 || first.BytesSent != 50 || first.BytesTotal != 100 {
		t.Fatalf("first transfer response = %#v", first)
	}
	second := responses[2]
	if second.Event != "install_transfer" || second.Percent != 75 || second.ElapsedMS != 2000 || second.ThroughputBPS != 37 {
		t.Fatalf("second transfer response = %#v", second)
	}
	if responses[3].Event != "progress" || responses[3].Status != "InspectingPackage" {
		t.Fatalf("device progress response = %#v", responses[3])
	}
	if responses[4].Event != "installed" || !responses[4].Final {
		t.Fatalf("final response = %#v", responses[4])
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("connection handler did not finish")
	}
}

func TestDirectInstallCancellationStopsDaemonOperation(t *testing.T) {
	daemon := testDaemon(t)
	daemon.validateApp = func(context.Context, string) (AppBundle, error) {
		return AppBundle{Path: "/tmp/Test.app", BundleIdentifier: "one.example.test"}, nil
	}
	cancelled := make(chan struct{})
	daemon.session = &fakeSession{install: func(ctx context.Context, _ AppBundle, _ func(int, string), _ func(string, uint64, uint64), _ time.Duration) error {
		<-ctx.Done()
		close(cancelled)
		return coded("install_failed", "cancelled", ctx.Err())
	}}
	daemon.state = StateActive

	server, client := net.Pipe()
	done := make(chan struct{})
	go func() {
		daemon.handleConnection(context.Background(), server)
		close(done)
	}()
	if err := json.NewEncoder(client).Encode(Request{Command: "install", AppPath: "/tmp/Test.app"}); err != nil {
		t.Fatal(err)
	}
	var accepted Response
	if err := json.NewDecoder(client).Decode(&accepted); err != nil {
		t.Fatal(err)
	}
	if accepted.Event != "accepted" {
		t.Fatalf("first response = %#v, want accepted", accepted)
	}
	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("client disconnect did not cancel the install")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("connection handler did not finish after cancellation")
	}
	if got := daemon.Snapshot(); got.State != StateActive || got.InstallCount != 0 {
		t.Fatalf("daemon after client cancellation = %#v, want active without an install", got)
	}
}

type fakeSession struct {
	closed  atomic.Int32
	done    chan error
	install func(context.Context, AppBundle, func(int, string), func(string, uint64, uint64), time.Duration) error
}

func (s *fakeSession) Close() error {
	s.closed.Add(1)
	return nil
}

func (s *fakeSession) Done() <-chan error {
	return s.done
}

func (s *fakeSession) Install(ctx context.Context, app AppBundle, progress func(int, string), transferProgress func(string, uint64, uint64), stallTimeout time.Duration) error {
	if s.install != nil {
		return s.install(ctx, app, progress, transferProgress, stallTimeout)
	}
	return nil
}

func testDaemon(t *testing.T) *Daemon {
	t.Helper()
	config, profilePath := testConfig(t)
	daemon, err := NewDaemon(config, profilePath)
	if err != nil {
		t.Fatal(err)
	}
	return daemon
}

func TestDaemonEstablishesAndDropsOnlyOuterFailedSession(t *testing.T) {
	daemon := testDaemon(t)
	session := &fakeSession{done: make(chan error, 1)}
	var opens atomic.Int32
	daemon.openSession = func(context.Context, Config) (sessionHandle, error) {
		if opens.Add(1) > 1 {
			return nil, coded("remote_pairing_cold", "listener closed", nil)
		}
		return session, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		daemon.connectionLoop(ctx)
		close(done)
	}()
	deadline := time.Now().Add(time.Second)
	for daemon.Snapshot().State != StateActive {
		if time.Now().After(deadline) {
			t.Fatal("daemon did not establish session")
		}
		time.Sleep(time.Millisecond)
	}
	session.done <- errors.New("transport closed")
	for daemon.Snapshot().SessionLosses != 1 {
		if time.Now().After(deadline) {
			t.Fatal("daemon did not record outer session loss")
		}
		time.Sleep(time.Millisecond)
	}
	if got := daemon.Snapshot(); got.Generation != 1 {
		t.Fatalf("lost snapshot = %#v", got)
	}
	if got := session.closed.Load(); got != 1 {
		t.Fatalf("close count = %d, want 1", got)
	}
	cancel()
	<-done
}

func TestDaemonClassifiesColdAcquisition(t *testing.T) {
	daemon := testDaemon(t)
	daemon.openSession = func(context.Context, Config) (sessionHandle, error) {
		return nil, coded("remote_pairing_cold", "listener closed", nil)
	}
	daemon.discoverPort = func(context.Context, Config) (int, error) {
		return 0, coded("remote_pairing_cold", "listener closed", nil)
	}
	daemon.establish(context.Background())
	if got := daemon.Snapshot(); got.State != StateWaiting || got.LastErrorCode != "remote_pairing_cold" || got.Generation != 0 {
		t.Fatalf("snapshot = %#v", got)
	}
}

func TestDaemonLearnsChangedRemotePairingPort(t *testing.T) {
	daemon := testDaemon(t)
	session := &fakeSession{done: make(chan error, 1)}
	var openedPorts []int
	daemon.openSession = func(_ context.Context, config Config) (sessionHandle, error) {
		openedPorts = append(openedPorts, config.RemotePairingPort)
		if len(openedPorts) == 1 {
			return nil, coded("remote_pairing_cold", "listener moved", nil)
		}
		return session, nil
	}
	daemon.discoverPort = func(context.Context, Config) (int, error) {
		return 52592, nil
	}
	daemon.establish(context.Background())
	if got := daemon.Snapshot(); got.State != StateActive || got.Generation != 1 {
		t.Fatalf("snapshot = %#v", got)
	}
	if len(openedPorts) != 2 || openedPorts[0] != 49152 || openedPorts[1] != 52592 {
		t.Fatalf("opened ports = %#v", openedPorts)
	}
	if daemon.config.RemotePairingPort != 52592 {
		t.Fatalf("runtime port = %d, want 52592", daemon.config.RemotePairingPort)
	}
}

func TestDaemonDoesNotLearnUnverifiedRemotePairingPort(t *testing.T) {
	daemon := testDaemon(t)
	var openedPorts []int
	daemon.openSession = func(_ context.Context, config Config) (sessionHandle, error) {
		openedPorts = append(openedPorts, config.RemotePairingPort)
		if len(openedPorts) == 1 {
			return nil, coded("remote_pairing_cold", "listener moved", nil)
		}
		return nil, coded("pair_verify_failed", "different paired device", nil)
	}
	daemon.discoverPort = func(context.Context, Config) (int, error) {
		return 52592, nil
	}
	daemon.establish(context.Background())
	if daemon.config.RemotePairingPort != 49152 {
		t.Fatalf("runtime port = %d, want original 49152", daemon.config.RemotePairingPort)
	}
	if got := daemon.Snapshot(); got.LastErrorCode != "pair_verify_failed" || got.Generation != 0 {
		t.Fatalf("snapshot = %#v", got)
	}
}

func TestDaemonRateLimitsRemotePairingDiscovery(t *testing.T) {
	daemon := testDaemon(t)
	daemon.openSession = func(context.Context, Config) (sessionHandle, error) {
		return nil, coded("remote_pairing_cold", "listener closed", nil)
	}
	var discoveries atomic.Int32
	daemon.discoverPort = func(context.Context, Config) (int, error) {
		discoveries.Add(1)
		return 0, coded("remote_pairing_cold", "listener closed", nil)
	}
	now := time.Unix(1_800_000_000, 0)
	daemon.now = func() time.Time { return now }
	daemon.establish(context.Background())
	daemon.establish(context.Background())
	if got := discoveries.Load(); got != 1 {
		t.Fatalf("discoveries = %d, want 1", got)
	}
	now = now.Add(remotePairingDiscoveryRetryInitial)
	daemon.establish(context.Background())
	if got := discoveries.Load(); got != 2 {
		t.Fatalf("discoveries after interval = %d, want 2", got)
	}
}

func TestTrustedSessionResetsDiscoveryBackoff(t *testing.T) {
	daemon := testDaemon(t)
	session := &fakeSession{done: make(chan error, 1)}
	var opens atomic.Int32
	daemon.openSession = func(context.Context, Config) (sessionHandle, error) {
		switch opens.Add(1) {
		case 1, 3:
			return nil, coded("remote_pairing_cold", "listener closed", nil)
		default:
			return session, nil
		}
	}
	var discoveries atomic.Int32
	daemon.discoverPort = func(context.Context, Config) (int, error) {
		discoveries.Add(1)
		return 0, coded("remote_pairing_cold", "listener closed", nil)
	}
	now := time.Unix(1_800_000_000, 0)
	daemon.now = func() time.Time { return now }
	daemon.establish(context.Background())
	daemon.establish(context.Background())
	if got := daemon.Snapshot(); got.State != StateActive {
		t.Fatalf("snapshot = %#v", got)
	}
	daemon.mu.Lock()
	daemon.session = nil
	daemon.mu.Unlock()
	daemon.establish(context.Background())
	if got := discoveries.Load(); got != 2 {
		t.Fatalf("discoveries = %d, want immediate second discovery after trusted session", got)
	}
}

func TestDaemonRejectsSiblingProfileForwardPortCollision(t *testing.T) {
	first := testDaemon(t)
	first.openSession = func(context.Context, Config) (sessionHandle, error) {
		return nil, coded("remote_pairing_cold", "listener closed", nil)
	}
	first.discoverPort = func(context.Context, Config) (int, error) {
		return 0, coded("remote_pairing_cold", "listener closed", nil)
	}
	secondProfile := filepath.Join(filepath.Dir(first.profilePath), "ipad.json")
	second, err := NewDaemon(first.config, secondProfile)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- first.Serve(ctx) }()
	deadline := time.Now().Add(time.Second)
	for !profileDaemonAvailable(context.Background(), first.profilePath) {
		if time.Now().After(deadline) {
			cancel()
			<-served
			t.Fatal("first profile daemon did not become available")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := second.Serve(context.Background()); errorCode(err) != "local_forward_port_conflict" {
		cancel()
		<-served
		t.Fatalf("second Serve error = %v, want local_forward_port_conflict", err)
	}
	cancel()
	if err := <-served; err != nil {
		t.Fatal(err)
	}
}

func TestDaemonSerializesInstalls(t *testing.T) {
	daemon := testDaemon(t)
	daemon.validateApp = func(context.Context, string) (AppBundle, error) {
		return AppBundle{Path: "/tmp/Test.app", BundleIdentifier: "one.example.test"}, nil
	}
	var running atomic.Int32
	var maximum atomic.Int32
	session := &fakeSession{install: func(_ context.Context, _ AppBundle, progress func(int, string), _ func(string, uint64, uint64), _ time.Duration) error {
		current := running.Add(1)
		for {
			previous := maximum.Load()
			if current <= previous || maximum.CompareAndSwap(previous, current) {
				break
			}
		}
		progress(50, "Installing")
		time.Sleep(20 * time.Millisecond)
		running.Add(-1)
		return nil
	}}
	daemon.session = session
	daemon.state = StateActive

	results := make(chan error, 2)
	for range 2 {
		go func() {
			results <- daemon.install(context.Background(), "/tmp/Test.app", func(int, string) {})
		}()
	}
	for range 2 {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if got := maximum.Load(); got != 1 {
		t.Fatalf("maximum concurrent installs = %d, want 1", got)
	}
	if got := daemon.Snapshot(); got.State != StateActive || got.InstallCount != 2 {
		t.Fatalf("snapshot = %#v", got)
	}
}

func TestDaemonExpiresQueuedInstallBeforeItStarts(t *testing.T) {
	daemon := testDaemon(t)
	validated := make(chan struct{})
	daemon.validateApp = func(context.Context, string) (AppBundle, error) {
		close(validated)
		return AppBundle{Path: "/tmp/Test.app", BundleIdentifier: "one.example.test"}, nil
	}
	var installs atomic.Int32
	daemon.session = &fakeSession{install: func(context.Context, AppBundle, func(int, string), func(string, uint64, uint64), time.Duration) error {
		installs.Add(1)
		return nil
	}}
	daemon.state = StateActive

	daemon.operationMu.Lock()
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- daemon.install(ctx, "/tmp/Test.app", func(int, string) {})
	}()
	<-validated
	cancel()
	daemon.operationMu.Unlock()

	if err := <-result; errorCode(err) != "install_failed" {
		t.Fatalf("queued install error = %v, want install_failed", err)
	}
	if got := installs.Load(); got != 0 {
		t.Fatalf("install calls = %d, want 0", got)
	}
}

func TestDaemonRetainsSessionWhenInstallServiceFails(t *testing.T) {
	daemon := testDaemon(t)
	daemon.validateApp = func(context.Context, string) (AppBundle, error) {
		return AppBundle{Path: "/tmp/Test.app", BundleIdentifier: "one.example.test"}, nil
	}
	var observedStallTimeout time.Duration
	var observedHardTimeout time.Duration
	session := &fakeSession{
		install: func(ctx context.Context, _ AppBundle, _ func(int, string), _ func(string, uint64, uint64), stallTimeout time.Duration) error {
			observedStallTimeout = stallTimeout
			if deadline, ok := ctx.Deadline(); ok {
				observedHardTimeout = time.Until(deadline)
			}
			return coded("install_failed", "transfer stalled", context.DeadlineExceeded)
		},
	}
	daemon.session = session
	daemon.state = StateActive
	daemon.generation = 1

	if err := daemon.install(context.Background(), "/tmp/Test.app", func(int, string) {}); errorCode(err) != "install_failed" {
		t.Fatalf("install error = %v, want install_failed", err)
	}
	if got := daemon.Snapshot(); got.State != StateRecovering || got.LastErrorCode != "service_unresponsive" || got.Generation != 1 {
		t.Fatalf("recovering snapshot = %#v", got)
	}
	if got := session.closed.Load(); got != 0 {
		t.Fatalf("close count after install timeout = %d, want 0", got)
	}
	if daemon.currentSession() != session {
		t.Fatal("failed install discarded the warm outer session")
	}
	if observedStallTimeout != 5*time.Minute {
		t.Fatalf("stall timeout = %s, want 5m", observedStallTimeout)
	}
	if observedHardTimeout < 29*time.Minute || observedHardTimeout > 30*time.Minute {
		t.Fatalf("hard timeout = %s, want about 30m", observedHardTimeout)
	}
}

func TestDaemonRetriesTransientInstallTransferAndThenSucceeds(t *testing.T) {
	daemon := testDaemon(t)
	daemon.validateApp = func(context.Context, string) (AppBundle, error) {
		return AppBundle{Path: "/tmp/Test.app", BundleIdentifier: "one.example.test"}, nil
	}
	daemon.retryDelay = func(int) time.Duration { return 0 }
	var calls atomic.Int32
	session := &fakeSession{install: func(context.Context, AppBundle, func(int, string), func(string, uint64, uint64), time.Duration) error {
		if calls.Add(1) == 1 {
			return coded("install_retryable", "transient transport interruption", errors.New("connection reset"))
		}
		return nil
	}}
	daemon.session = session
	daemon.state = StateActive
	daemon.generation = 4

	if err := daemon.install(context.Background(), "/tmp/Test.app", nil); err != nil {
		t.Fatal(err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("install calls = %d, want 2", got)
	}
	if got := daemon.Snapshot(); got.State != StateActive || got.InstallCount != 1 || got.Generation != 4 {
		t.Fatalf("snapshot after retry success = %#v", got)
	}
	if daemon.currentSession() != session {
		t.Fatal("successful retry replaced the warm outer session")
	}
}

func TestDaemonStopsAfterMaximumTransientInstallAttempts(t *testing.T) {
	daemon := testDaemon(t)
	daemon.validateApp = func(context.Context, string) (AppBundle, error) {
		return AppBundle{Path: "/tmp/Test.app", BundleIdentifier: "one.example.test"}, nil
	}
	daemon.retryDelay = func(int) time.Duration { return 0 }
	var calls atomic.Int32
	session := &fakeSession{install: func(context.Context, AppBundle, func(int, string), func(string, uint64, uint64), time.Duration) error {
		calls.Add(1)
		return coded("install_retryable", "transient transport interruption", errors.New("connection reset"))
	}}
	daemon.session = session
	daemon.state = StateActive
	daemon.generation = 2

	err := daemon.install(context.Background(), "/tmp/Test.app", nil)
	if errorCode(err) != "install_failed" {
		t.Fatalf("install error = %v, want install_failed", err)
	}
	if got := calls.Load(); got != installTransferMaximumAttempts {
		t.Fatalf("install calls = %d, want %d", got, installTransferMaximumAttempts)
	}
	if got := daemon.Snapshot(); got.State != StateRecovering || got.InstallCount != 0 || got.Generation != 2 {
		t.Fatalf("snapshot after exhausted retries = %#v", got)
	}
	if daemon.currentSession() != session {
		t.Fatal("exhausted retries discarded the warm outer session")
	}
}

func TestDaemonDoesNotRetryPermanentInstallFailure(t *testing.T) {
	daemon := testDaemon(t)
	daemon.validateApp = func(context.Context, string) (AppBundle, error) {
		return AppBundle{Path: "/tmp/Test.app", BundleIdentifier: "one.example.test"}, nil
	}
	daemon.retryDelay = func(int) time.Duration { return 0 }
	var calls atomic.Int32
	daemon.session = &fakeSession{install: func(context.Context, AppBundle, func(int, string), func(string, uint64, uint64), time.Duration) error {
		calls.Add(1)
		return coded("install_failed", "device rejected app", errors.New("rejected"))
	}}
	daemon.state = StateActive
	daemon.generation = 3

	if err := daemon.install(context.Background(), "/tmp/Test.app", nil); errorCode(err) != "install_failed" {
		t.Fatalf("install error = %v, want install_failed", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("install calls = %d, want 1", got)
	}
}

func TestDaemonDoesNotRetryAcrossSessionGenerationChange(t *testing.T) {
	daemon := testDaemon(t)
	daemon.validateApp = func(context.Context, string) (AppBundle, error) {
		return AppBundle{Path: "/tmp/Test.app", BundleIdentifier: "one.example.test"}, nil
	}
	daemon.retryDelay = func(int) time.Duration { return 0 }
	var calls atomic.Int32
	session := &fakeSession{install: func(context.Context, AppBundle, func(int, string), func(string, uint64, uint64), time.Duration) error {
		calls.Add(1)
		daemon.mu.Lock()
		daemon.generation++
		daemon.mu.Unlock()
		return coded("install_retryable", "transient transport interruption", errors.New("connection reset"))
	}}
	daemon.session = session
	daemon.state = StateActive
	daemon.generation = 7

	if err := daemon.install(context.Background(), "/tmp/Test.app", nil); errorCode(err) != "install_failed" {
		t.Fatalf("install error = %v, want install_failed", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("install calls = %d, want 1", got)
	}
	if got := daemon.Snapshot().Generation; got != 8 {
		t.Fatalf("generation = %d, want 8", got)
	}
}

func TestDaemonDoesNotRetryAfterOuterSessionEndsDuringInstall(t *testing.T) {
	daemon := testDaemon(t)
	daemon.validateApp = func(context.Context, string) (AppBundle, error) {
		return AppBundle{Path: "/tmp/Test.app", BundleIdentifier: "one.example.test"}, nil
	}
	daemon.retryDelay = func(int) time.Duration { return 0 }
	var calls atomic.Int32
	session := &fakeSession{done: make(chan error, 1)}
	session.install = func(context.Context, AppBundle, func(int, string), func(string, uint64, uint64), time.Duration) error {
		calls.Add(1)
		session.done <- errors.New("outer tunnel closed")
		deadline := time.Now().Add(time.Second)
		for daemon.sessionCanRetry(session, 9) {
			if time.Now().After(deadline) {
				t.Fatal("connection loop did not mark the outer session ended")
			}
			time.Sleep(time.Millisecond)
		}
		return coded("install_retryable", "transient transport interruption", errors.New("connection reset"))
	}
	daemon.session = session
	daemon.state = StateActive
	daemon.generation = 9

	ctx, cancel := context.WithCancel(context.Background())
	loopDone := make(chan struct{})
	go func() {
		daemon.connectionLoop(ctx)
		close(loopDone)
	}()

	err := daemon.install(context.Background(), "/tmp/Test.app", nil)
	if errorCode(err) != "install_failed" {
		t.Fatalf("install error = %v, want install_failed", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("install calls = %d, want 1", got)
	}
	cancel()
	select {
	case <-loopDone:
	case <-time.After(time.Second):
		t.Fatal("connection loop did not stop")
	}
	if got := daemon.Snapshot().SessionLosses; got != 1 {
		t.Fatalf("session losses = %d, want 1", got)
	}
}

func TestDaemonServeControlSocketLifecycle(t *testing.T) {
	daemon := testDaemon(t)
	session := &fakeSession{}
	daemon.openSession = func(context.Context, Config) (sessionHandle, error) {
		return session, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	served := make(chan error, 1)
	go func() { served <- daemon.Serve(ctx) }()

	deadline := time.Now().Add(2 * time.Second)
	for {
		if info, err := os.Stat(daemon.socketPath); err == nil {
			if got := info.Mode().Perm(); got != 0o600 {
				t.Fatalf("socket mode = %#o, want 0600", got)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("control socket did not appear")
		}
		time.Sleep(5 * time.Millisecond)
	}
	for daemon.Snapshot().State != StateActive {
		if time.Now().After(deadline) {
			t.Fatal("daemon did not establish its test session")
		}
		time.Sleep(5 * time.Millisecond)
	}

	var responses []Response
	callCtx, callCancel := context.WithTimeout(context.Background(), time.Second)
	defer callCancel()
	if err := Call(callCtx, daemon.profilePath, Request{Command: "status"}, func(response Response) {
		responses = append(responses, response)
	}); err != nil {
		t.Fatal(err)
	}
	if len(responses) != 1 || !responses[0].Final || responses[0].Event != "status" {
		t.Fatalf("responses = %#v", responses)
	}
	if err := Call(callCtx, daemon.profilePath, Request{Command: "stop"}, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-served:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("daemon did not stop")
	}
	if _, err := os.Stat(daemon.socketPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("socket still exists: %v", err)
	}
	if got := session.closed.Load(); got != 1 {
		t.Fatalf("close count = %d, want 1", got)
	}
}

func TestPrepareUnixListenerRejectsLiveOwner(t *testing.T) {
	directory, err := os.MkdirTemp("/tmp", "deploy-link-listener-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	socketPath := filepath.Join(directory, "deploy-link.sock")
	listener, err := prepareUnixListener(socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	if _, err := prepareUnixListener(socketPath); errorCode(err) != "deploy_link_already_running" {
		t.Fatalf("error = %v, want deploy_link_already_running", err)
	}
}
