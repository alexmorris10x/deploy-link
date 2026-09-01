package linkcore

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func writeDaemonInstallJob(t *testing.T, daemon *Daemon, state string) (InstallJob, string) {
	t.Helper()
	job := testInstallJob(t, daemon.profilePath, state)
	snapshot := daemon.Snapshot()
	job.Baseline = InstallJobBaseline{
		DaemonID:     snapshot.DaemonID,
		Generation:   snapshot.Generation,
		InstallCount: snapshot.InstallCount,
	}
	artifactPath, err := installJobArtifactPath(daemon.profilePath, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(artifactPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(artifactPath, "payload"), []byte("submitted app snapshot"), 0o600); err != nil {
		t.Fatal(err)
	}
	digest, err := digestInstallJobArtifact(context.Background(), artifactPath)
	if err != nil {
		t.Fatal(err)
	}
	job.Artifact.ContentSHA256 = digest
	if err := writeInstallJob(daemon.profilePath, job); err != nil {
		t.Fatal(err)
	}
	return job, artifactPath
}

func TestDaemonSnapshotResponseCarriesStableIdentity(t *testing.T) {
	daemon := testDaemon(t)
	first := daemon.Snapshot()
	second := daemon.Snapshot()
	if !validInstallJobID(first.DaemonID) || first.DaemonID != second.DaemonID {
		t.Fatalf("daemon identity is invalid or unstable: %q then %q", first.DaemonID, second.DaemonID)
	}
	response := snapshotResponse(first)
	if response.DaemonID != first.DaemonID || response.ControlSchema != ControlSchemaVersion {
		t.Fatalf("status response = %#v, want daemon identity and current control schema", response)
	}
}

func TestDaemonJobInstallWritesProgressAndExactInstalledResult(t *testing.T) {
	daemon := testDaemon(t)
	daemon.mu.Lock()
	daemon.state = StateActive
	daemon.generation = 5
	daemon.installCount = 11
	daemon.mu.Unlock()
	job, artifactPath := writeDaemonInstallJob(t, daemon, InstallJobStateQueued)

	daemon.validateApp = func(_ context.Context, path string) (AppBundle, error) {
		if path != artifactPath {
			t.Fatalf("validated app path = %q, want private snapshot %q", path, artifactPath)
		}
		return AppBundle{
			Path:             path,
			BundleIdentifier: job.Artifact.BundleIdentifier,
			Version:          job.Artifact.Version,
			Build:            job.Artifact.Build,
		}, nil
	}
	var installCalls atomic.Int32
	daemon.session = &fakeSession{install: func(_ context.Context, app AppBundle, progress func(int, string), transferProgress func(string, uint64, uint64), _ time.Duration) error {
		installCalls.Add(1)
		if app.Path != artifactPath || app.BundleIdentifier != job.Artifact.BundleIdentifier {
			t.Fatalf("installed app = %#v, want submitted snapshot", app)
		}
		transferProgress("payload_sending", 50, 100)
		progress(20, "CopyingFile")
		progress(75, "DataComplete")
		return nil
	}}
	var accepted atomic.Int32
	var observedProgress []InstallJobProgress
	err := daemon.installRequest(context.Background(), Request{
		Command: "install",
		AppPath: artifactPath,
		JobID:   job.ID,
	}, func() {
		accepted.Add(1)
	}, func(percent int, status string) {
		observedProgress = append(observedProgress, InstallJobProgress{Percent: percent, Status: status})
	})
	if err != nil {
		t.Fatal(err)
	}
	if installCalls.Load() != 1 || accepted.Load() != 1 {
		t.Fatalf("install calls = %d, accepted calls = %d; want one each", installCalls.Load(), accepted.Load())
	}
	if len(observedProgress) != 2 || observedProgress[0].Percent != 20 || observedProgress[1].Status != "DataComplete" {
		t.Fatalf("forwarded progress = %#v", observedProgress)
	}

	written, err := readInstallJobRecord(daemon.profilePath, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if written.State != InstallJobStateInstalled || written.StartedAt == nil || written.FinishedAt == nil {
		t.Fatalf("terminal job = %#v, want installed with start and finish times", written)
	}
	if written.Progress.Percent != 100 || written.Progress.Status != "InstallComplete" {
		t.Fatalf("terminal progress = %#v, want InstallComplete", written.Progress)
	}
	if written.Result == nil || written.Result.Event != "installed" ||
		written.Result.DaemonID != daemon.daemonID ||
		written.Result.Generation != job.Baseline.Generation ||
		written.Result.InstallCount != job.Baseline.InstallCount+1 {
		t.Fatalf("installed result = %#v, want exact daemon and counter proof", written.Result)
	}
	snapshot := daemon.Snapshot()
	if snapshot.State != StateActive || snapshot.InstallCount != job.Baseline.InstallCount+1 {
		t.Fatalf("daemon after install = %#v", snapshot)
	}
}

func TestDaemonJobRejectsChangedSnapshotContentsBeforeClaim(t *testing.T) {
	daemon := testDaemon(t)
	daemon.mu.Lock()
	daemon.state = StateActive
	daemon.generation = 5
	daemon.installCount = 11
	daemon.mu.Unlock()
	job, artifactPath := writeDaemonInstallJob(t, daemon, InstallJobStateQueued)
	if err := os.WriteFile(filepath.Join(artifactPath, "payload"), []byte("replaced app snapshot"), 0o600); err != nil {
		t.Fatal(err)
	}
	daemon.validateApp = func(_ context.Context, path string) (AppBundle, error) {
		return AppBundle{
			Path:             path,
			BundleIdentifier: job.Artifact.BundleIdentifier,
			Version:          job.Artifact.Version,
			Build:            job.Artifact.Build,
		}, nil
	}
	var installCalls atomic.Int32
	daemon.session = &fakeSession{install: func(context.Context, AppBundle, func(int, string), func(string, uint64, uint64), time.Duration) error {
		installCalls.Add(1)
		return nil
	}}

	err := daemon.installRequest(context.Background(), Request{Command: "install", AppPath: artifactPath, JobID: job.ID}, nil, nil)
	if errorCode(err) != "job_snapshot_changed" {
		t.Fatalf("changed snapshot error code = %q, want job_snapshot_changed", errorCode(err))
	}
	if installCalls.Load() != 0 {
		t.Fatalf("changed snapshot reached the device %d times", installCalls.Load())
	}
	written, readErr := readInstallJobRecord(daemon.profilePath, job.ID)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if written.State != InstallJobStateQueued || written.StartedAt != nil {
		t.Fatalf("changed snapshot was claimed: %#v", written)
	}
}

func TestDaemonJobProgressWriteFailureCreatesTerminalReceipt(t *testing.T) {
	daemon := testDaemon(t)
	daemon.mu.Lock()
	daemon.state = StateActive
	daemon.generation = 5
	daemon.installCount = 11
	daemon.mu.Unlock()
	job, artifactPath := writeDaemonInstallJob(t, daemon, InstallJobStateQueued)
	daemon.validateApp = func(_ context.Context, path string) (AppBundle, error) {
		return AppBundle{
			Path:             path,
			BundleIdentifier: job.Artifact.BundleIdentifier,
			Version:          job.Artifact.Version,
			Build:            job.Artifact.Build,
		}, nil
	}
	daemon.writeJob = func(string, InstallJob) error {
		return coded("job_write_failed", "injected progress receipt failure", nil)
	}
	daemon.session = &fakeSession{install: func(_ context.Context, _ AppBundle, progress func(int, string), _ func(string, uint64, uint64), _ time.Duration) error {
		progress(20, "CopyingFile")
		return nil
	}}

	err := daemon.installRequest(context.Background(), Request{Command: "install", AppPath: artifactPath, JobID: job.ID}, nil, nil)
	if errorCode(err) != "job_write_failed" {
		t.Fatalf("progress write error code = %q, want job_write_failed", errorCode(err))
	}
	written, readErr := readInstallJobRecord(daemon.profilePath, job.ID)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if written.State != InstallJobStateFailed || written.FinishedAt == nil || written.ErrorCode != "job_write_failed" {
		t.Fatalf("progress write failure receipt = %#v, want terminal failed", written)
	}
}

func TestDirectInstallObservesQueuedJobCreatedWhileWaitingForOperation(t *testing.T) {
	daemon := testDaemon(t)
	daemon.state = StateActive
	validated := make(chan struct{})
	daemon.validateApp = func(_ context.Context, path string) (AppBundle, error) {
		close(validated)
		return AppBundle{Path: path, BundleIdentifier: "com.example.Direct"}, nil
	}
	var installCalls atomic.Int32
	daemon.session = &fakeSession{install: func(context.Context, AppBundle, func(int, string), func(string, uint64, uint64), time.Duration) error {
		installCalls.Add(1)
		return nil
	}}

	daemon.operationMu.Lock()
	locked := true
	defer func() {
		if locked {
			daemon.operationMu.Unlock()
		}
	}()
	result := make(chan error, 1)
	go func() {
		result <- daemon.install(context.Background(), "/tmp/Direct.app", nil)
	}()
	<-validated
	queued, _ := writeDaemonInstallJob(t, daemon, InstallJobStateQueued)
	daemon.operationMu.Unlock()
	locked = false

	if err := <-result; errorCode(err) != "job_active" {
		t.Fatalf("direct install error code = %q, want job_active", errorCode(err))
	}
	if installCalls.Load() != 0 {
		t.Fatalf("direct install reached the device %d times", installCalls.Load())
	}
	written, err := readInstallJobRecord(daemon.profilePath, queued.ID)
	if err != nil {
		t.Fatal(err)
	}
	if written.State != InstallJobStateQueued {
		t.Fatalf("queued job state = %q, want queued", written.State)
	}
}

func TestDirectInstallRejectsExistingRunningJob(t *testing.T) {
	daemon := testDaemon(t)
	daemon.state = StateActive
	daemon.validateApp = func(_ context.Context, path string) (AppBundle, error) {
		return AppBundle{Path: path, BundleIdentifier: "com.example.Direct"}, nil
	}
	var installCalls atomic.Int32
	daemon.session = &fakeSession{install: func(context.Context, AppBundle, func(int, string), func(string, uint64, uint64), time.Duration) error {
		installCalls.Add(1)
		return nil
	}}
	running, _ := writeDaemonInstallJob(t, daemon, InstallJobStateRunning)

	if err := daemon.install(context.Background(), "/tmp/Direct.app", nil); errorCode(err) != "job_active" {
		t.Fatalf("direct install error code = %q, want job_active", errorCode(err))
	}
	if installCalls.Load() != 0 {
		t.Fatalf("direct install reached the device %d times", installCalls.Load())
	}
	written, err := readInstallJobRecord(daemon.profilePath, running.ID)
	if err != nil {
		t.Fatal(err)
	}
	if written.State != InstallJobStateRunning {
		t.Fatalf("running job state = %q, want running", written.State)
	}
}

func TestDaemonServeReconcilesRunningJobBeforeListening(t *testing.T) {
	previous := testDaemon(t)
	previous.state = StateActive
	previous.generation = 4
	running, _ := writeDaemonInstallJob(t, previous, InstallJobStateRunning)
	restarted, err := NewDaemon(previous.config, previous.profilePath)
	if err != nil {
		t.Fatal(err)
	}
	restarted.daemonID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if restarted.daemonID == running.Baseline.DaemonID {
		t.Fatal("restart test requires a new daemon identity")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	served := make(chan error, 1)
	go func() {
		served <- restarted.Serve(ctx)
	}()
	select {
	case err := <-served:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled daemon did not finish startup reconciliation")
	}

	written, err := readInstallJobRecord(restarted.profilePath, running.ID)
	if err != nil {
		t.Fatal(err)
	}
	if written.State != InstallJobStateInterrupted || written.ErrorCode != "job_interrupted" || written.FinishedAt == nil {
		t.Fatalf("Serve restart reconciliation = %#v, want interrupted", written)
	}
}
