package linkcore

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	testInstallJobID = "0123456789abcdef0123456789abcdef"
	testDaemonID     = "fedcba9876543210fedcba9876543210"
)

func testInstallJob(t *testing.T, profilePath string, state string) InstallJob {
	t.Helper()
	now := time.Date(2026, 8, 31, 12, 0, 0, 0, time.UTC)
	job := InstallJob{
		SchemaVersion: InstallJobSchemaVersion,
		ID:            testInstallJobID,
		State:         state,
		Artifact: InstallJobArtifact{
			BundleIdentifier: "com.example.Reader",
			Version:          "2.4",
			Build:            "81",
			ContentSHA256:    strings.Repeat("a", 64),
		},
		Baseline:    InstallJobBaseline{DaemonID: testDaemonID, Generation: 7, InstallCount: 3},
		Progress:    InstallJobProgress{Percent: 42, Status: "DataComplete"},
		SubmittedAt: now,
		UpdatedAt:   now,
	}
	directory, err := installJobDirectory(profilePath, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	return job
}

func TestInstallJobIDValidationAndPathContainment(t *testing.T) {
	if !validInstallJobID(testInstallJobID) {
		t.Fatalf("expected %q to be a valid install job identifier", testInstallJobID)
	}
	for _, value := range []string{
		"",
		"short",
		"../0123456789abcdef0123456789abcdef",
		"0123456789abcdef/123456789abcdef",
		"0123456789abcdef0123456789abcdeg",
		"0123456789abcdef0123456789abcdef00",
	} {
		if validInstallJobID(value) {
			t.Errorf("accepted invalid install job identifier %q", value)
		}
		if _, err := installJobDirectory(filepath.Join(t.TempDir(), "profile.json"), value); errorCode(err) != "job_invalid" {
			t.Errorf("installJobDirectory(%q) error code = %q, want job_invalid", value, errorCode(err))
		}
	}

	profilePath := filepath.Join(t.TempDir(), "profiles", "iphone.json")
	root, err := installJobsRoot(profilePath)
	if err != nil {
		t.Fatal(err)
	}
	directory, err := installJobDirectory(profilePath, testInstallJobID)
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(root, directory)
	if err != nil {
		t.Fatal(err)
	}
	if relative != testInstallJobID || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		t.Fatalf("job directory escaped its root: root=%q directory=%q relative=%q", root, directory, relative)
	}
}

func TestInstallJobLaunchAgentIsOneShotAndUsesWorkerCommand(t *testing.T) {
	homeDirectory := filepath.Join(string(filepath.Separator), "Users", "developer")
	profilePath := filepath.Join(homeDirectory, "Deploy & Link", "profiles", "iphone.json")
	binaryPath := filepath.Join(homeDirectory, "Deploy & Link", "bin", "deploy-link")
	spec := buildInstallJobLaunchAgentSpec(profilePath, testInstallJobID, homeDirectory, "gui/501")
	contents := installJobLaunchAgentContents(spec, binaryPath, profilePath, testInstallJobID)

	for _, value := range []string{
		"<key>RunAtLoad</key>",
		"<true/>",
		"<string>job</string>",
		"<string>run</string>",
		"<string>--profile</string>",
		"<key>Umask</key>",
		"<integer>63</integer>",
		"<string>" + testInstallJobID + "</string>",
		xmlEscape(binaryPath),
		xmlEscape(profilePath),
		xmlEscape(spec.standardOut),
		xmlEscape(spec.standardError),
	} {
		if !strings.Contains(contents, value) {
			t.Fatalf("one-shot LaunchAgent does not contain %q", value)
		}
	}
	if strings.Contains(contents, "<key>KeepAlive</key>") {
		t.Fatal("one-shot install worker must not use KeepAlive")
	}
	if strings.Contains(contents, "Deploy & Link") {
		t.Fatal("one-shot LaunchAgent contains an unescaped path")
	}
	jobDirectory, err := installJobDirectory(profilePath, testInstallJobID)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{spec.standardOut, spec.standardError} {
		if filepath.Dir(path) != jobDirectory {
			t.Fatalf("worker log %q is outside private job directory %q", path, jobDirectory)
		}
	}
}

func TestInstallJobTerminalStates(t *testing.T) {
	for _, state := range []string{
		InstallJobStateQueued,
		InstallJobStateRunning,
		"unknown",
	} {
		if (InstallJob{State: state}).Terminal() {
			t.Errorf("state %q unexpectedly reported terminal", state)
		}
	}
	for _, state := range []string{
		InstallJobStateInstalled,
		InstallJobStateFailed,
		InstallJobStateInterrupted,
	} {
		if !(InstallJob{State: state}).Terminal() {
			t.Errorf("state %q did not report terminal", state)
		}
	}
}

func TestInstallJobArtifactDigestDetectsContentChanges(t *testing.T) {
	artifactPath := filepath.Join(t.TempDir(), "Reader.app")
	if err := os.MkdirAll(filepath.Join(artifactPath, "Frameworks"), 0o700); err != nil {
		t.Fatal(err)
	}
	payloadPath := filepath.Join(artifactPath, "Reader")
	if err := os.WriteFile(payloadPath, []byte("first signed payload"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../Reader", filepath.Join(artifactPath, "Frameworks", "ReaderLink")); err != nil {
		t.Fatal(err)
	}
	first, err := digestInstallJobArtifact(context.Background(), artifactPath)
	if err != nil {
		t.Fatal(err)
	}
	second, err := digestInstallJobArtifact(context.Background(), artifactPath)
	if err != nil {
		t.Fatal(err)
	}
	if !validSHA256(first) || first != second {
		t.Fatalf("artifact digest is invalid or unstable: %q then %q", first, second)
	}
	if err := os.WriteFile(payloadPath, []byte("different signed payload"), 0o700); err != nil {
		t.Fatal(err)
	}
	changed, err := digestInstallJobArtifact(context.Background(), artifactPath)
	if err != nil {
		t.Fatal(err)
	}
	if changed == first {
		t.Fatal("artifact digest did not change after payload replacement")
	}
}

func TestWriteInstallJobReceiptIsOwnerOnlyAndContainsNoFilesystemPaths(t *testing.T) {
	profilePath := filepath.Join(t.TempDir(), "profiles", "iphone.json")
	job := testInstallJob(t, profilePath, InstallJobStateQueued)
	if err := writeInstallJob(profilePath, job); err != nil {
		t.Fatal(err)
	}

	receiptPath, err := installJobRecordPath(profilePath, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := info.Mode().Perm(), os.FileMode(0o600); got != want {
		t.Fatalf("receipt mode = %#o, want %#o", got, want)
	}
	data, err := os.ReadFile(receiptPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, privatePath := range []string{profilePath, filepath.Dir(profilePath), "artifact.app", "/private/source/Reader.app"} {
		if strings.Contains(string(data), privatePath) {
			t.Fatalf("public receipt exposes filesystem path %q", privatePath)
		}
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["id"] != job.ID || decoded["state"] != InstallJobStateQueued {
		t.Fatalf("receipt identity = %#v, want id %q and state %q", decoded, job.ID, InstallJobStateQueued)
	}

	job.State = InstallJobStateRunning
	job.UpdatedAt = job.UpdatedAt.Add(time.Second)
	if err := writeInstallJob(profilePath, job); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Dir(receiptPath))
	if err != nil {
		t.Fatal(err)
	}
	wantEntries := map[string]bool{filepath.Base(receiptPath): false, "receipt.lock": false}
	for _, entry := range entries {
		if _, ok := wantEntries[entry.Name()]; !ok {
			t.Fatalf("atomic receipt update left unexpected file %q", entry.Name())
		}
		wantEntries[entry.Name()] = true
	}
	for name, found := range wantEntries {
		if !found {
			t.Fatalf("atomic receipt update did not leave required file %q", name)
		}
	}
	written, err := ReadInstallJob(profilePath, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if written.State != InstallJobStateRunning {
		t.Fatalf("updated receipt state = %q, want running", written.State)
	}
}

func TestWaitInstallJobStopsAtTerminalAndReportsFinalUpdate(t *testing.T) {
	profilePath := filepath.Join(t.TempDir(), "profiles", "iphone.json")
	job := testInstallJob(t, profilePath, InstallJobStateInstalled)
	finishedAt := job.UpdatedAt.Add(time.Second)
	job.FinishedAt = &finishedAt
	job.Result = &InstallJobResult{Event: "installed", DaemonID: testDaemonID, Generation: 7, InstallCount: 4}
	if err := writeInstallJob(profilePath, job); err != nil {
		t.Fatal(err)
	}

	var updates []InstallJob
	got, err := WaitInstallJob(context.Background(), profilePath, job.ID, time.Hour, func(update InstallJob) {
		updates = append(updates, update)
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.State != InstallJobStateInstalled {
		t.Fatalf("wait returned state %q, want installed", got.State)
	}
	if len(updates) != 1 || updates[0].State != InstallJobStateInstalled {
		t.Fatalf("wait updates = %#v, want one installed update", updates)
	}
}

func TestWaitInstallJobCancellationDoesNotChangeActiveReceipt(t *testing.T) {
	profilePath := filepath.Join(t.TempDir(), "profiles", "iphone.json")
	job := testInstallJob(t, profilePath, InstallJobStateRunning)
	if err := writeInstallJob(profilePath, job); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	got, err := WaitInstallJob(ctx, profilePath, job.ID, time.Hour, nil)
	if errorCode(err) != "job_wait_cancelled" {
		t.Fatalf("wait error code = %q, want job_wait_cancelled", errorCode(err))
	}
	if got.State != InstallJobStateRunning {
		t.Fatalf("wait returned state %q, want running", got.State)
	}
	written, readErr := ReadInstallJob(profilePath, job.ID)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if written.State != InstallJobStateRunning || written.FinishedAt != nil {
		t.Fatalf("cancelled wait changed active receipt: %#v", written)
	}
}

func TestRemoveInstallJobRejectsActiveReceiptBeforeLaunchd(t *testing.T) {
	profilePath := filepath.Join(t.TempDir(), "profiles", "iphone.json")
	job := testInstallJob(t, profilePath, InstallJobStateRunning)
	if err := writeInstallJob(profilePath, job); err != nil {
		t.Fatal(err)
	}

	if err := RemoveInstallJob(profilePath, job.ID); errorCode(err) != "job_active" {
		t.Fatalf("remove error code = %q, want job_active", errorCode(err))
	}
	if _, err := ReadInstallJob(profilePath, job.ID); err != nil {
		t.Fatalf("active receipt was removed: %v", err)
	}
}

func TestClaimInstallJobMovesMatchingQueueToRunning(t *testing.T) {
	profilePath := filepath.Join(t.TempDir(), "profiles", "iphone.json")
	job := testInstallJob(t, profilePath, InstallJobStateQueued)
	if err := writeInstallJob(profilePath, job); err != nil {
		t.Fatal(err)
	}
	snapshot := Snapshot{
		State:        StateActive,
		DaemonID:     job.Baseline.DaemonID,
		Generation:   job.Baseline.Generation,
		InstallCount: job.Baseline.InstallCount,
	}

	claimed, err := claimInstallJob(profilePath, job.ID, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.State != InstallJobStateRunning || claimed.StartedAt == nil || claimed.FinishedAt != nil {
		t.Fatalf("claimed job = %#v, want running with only a start time", claimed)
	}
	written, err := readInstallJobRecord(profilePath, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if written.State != InstallJobStateRunning || written.StartedAt == nil || !written.UpdatedAt.Equal(claimed.UpdatedAt) {
		t.Fatalf("durable claimed job = %#v, want running receipt", written)
	}
}

func TestClaimInstallJobInterruptsChangedDaemonContract(t *testing.T) {
	tests := []struct {
		name   string
		change func(*Snapshot)
	}{
		{
			name: "daemon identity",
			change: func(snapshot *Snapshot) {
				snapshot.DaemonID = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			},
		},
		{
			name: "generation",
			change: func(snapshot *Snapshot) {
				snapshot.Generation++
			},
		},
		{
			name: "install count",
			change: func(snapshot *Snapshot) {
				snapshot.InstallCount++
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			profilePath := filepath.Join(t.TempDir(), "profiles", "iphone.json")
			job := testInstallJob(t, profilePath, InstallJobStateQueued)
			if err := writeInstallJob(profilePath, job); err != nil {
				t.Fatal(err)
			}
			snapshot := Snapshot{
				State:        StateActive,
				DaemonID:     job.Baseline.DaemonID,
				Generation:   job.Baseline.Generation,
				InstallCount: job.Baseline.InstallCount,
			}
			test.change(&snapshot)

			interrupted, err := claimInstallJob(profilePath, job.ID, snapshot)
			if errorCode(err) != "job_interrupted" {
				t.Fatalf("claim error code = %q, want job_interrupted", errorCode(err))
			}
			if interrupted.State != InstallJobStateInterrupted || interrupted.FinishedAt == nil || interrupted.StartedAt != nil {
				t.Fatalf("interrupted job = %#v", interrupted)
			}
			written, readErr := readInstallJobRecord(profilePath, job.ID)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if written.State != InstallJobStateInterrupted || written.ErrorCode != "job_interrupted" {
				t.Fatalf("durable mismatch result = %#v, want interrupted", written)
			}
		})
	}
}

func TestReconcileRunningInstallJobMarksDaemonRestartInterrupted(t *testing.T) {
	profilePath := filepath.Join(t.TempDir(), "profiles", "iphone.json")
	job := testInstallJob(t, profilePath, InstallJobStateRunning)
	startedAt := job.UpdatedAt.Add(time.Second)
	job.StartedAt = &startedAt
	job.UpdatedAt = startedAt
	if err := writeInstallJob(profilePath, job); err != nil {
		t.Fatal(err)
	}

	if err := reconcileRunningInstallJob(profilePath); err != nil {
		t.Fatal(err)
	}
	written, err := readInstallJobRecord(profilePath, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if written.State != InstallJobStateInterrupted || written.ErrorCode != "job_interrupted" || written.FinishedAt == nil {
		t.Fatalf("restart reconciliation = %#v, want terminal interrupted receipt", written)
	}
}

func TestActiveInstallJobIgnoresLaunchdLogDirectoryWithoutReceipt(t *testing.T) {
	profilePath := filepath.Join(t.TempDir(), "profiles", "iphone.json")
	job := testInstallJob(t, profilePath, InstallJobStateRunning)
	if err := writeInstallJob(profilePath, job); err != nil {
		t.Fatal(err)
	}
	// A leftover one-shot LaunchAgent makes launchd recreate a removed job
	// directory that holds only its worker log and no receipt.
	orphan, err := installJobDirectory(profilePath, "00000000000000000000000000000000")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(orphan, 0o744); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(orphan, "worker.log"), nil, 0o644); err != nil {
		t.Fatal(err)
	}

	active, found, err := activeInstallJob(profilePath)
	if err != nil || !found || active.ID != job.ID {
		t.Fatalf("activeInstallJob = %q, %v, %v; want running job beside orphan", active.ID, found, err)
	}
	if err := reconcileRunningInstallJob(profilePath); err != nil {
		t.Fatalf("daemon startup reconciliation failed on orphan directory: %v", err)
	}
}

func TestRunInstallJobRetiresOneShotLaunchAgentFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	profilePath := filepath.Join(t.TempDir(), "profiles", "iphone.json")
	job := testInstallJob(t, profilePath, InstallJobStateFailed)
	if err := writeInstallJob(profilePath, job); err != nil {
		t.Fatal(err)
	}
	spec, err := installJobLaunchAgentSpecForProfile(profilePath, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(spec.plistPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(spec.plistPath, []byte("plist"), 0o600); err != nil {
		t.Fatal(err)
	}

	// A login reruns every RunAtLoad agent whose file remains, including
	// workers for long-finished jobs.
	if _, err := RunInstallJob(context.Background(), profilePath, job.ID, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(spec.plistPath); !os.IsNotExist(err) {
		t.Fatalf("one-shot LaunchAgent file still present after worker ran: %v", err)
	}
}

func TestInstallJobTerminalCompareAndSwapPreservesNewRunningClaim(t *testing.T) {
	profilePath := filepath.Join(t.TempDir(), "profiles", "iphone.json")
	staleQueued := testInstallJob(t, profilePath, InstallJobStateQueued)
	if err := writeInstallJob(profilePath, staleQueued); err != nil {
		t.Fatal(err)
	}
	snapshot := Snapshot{
		State:        StateActive,
		DaemonID:     staleQueued.Baseline.DaemonID,
		Generation:   staleQueued.Baseline.Generation,
		InstallCount: staleQueued.Baseline.InstallCount,
	}
	claimed, err := claimInstallJob(profilePath, staleQueued.ID, snapshot)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.State != InstallJobStateRunning {
		t.Fatalf("claimed state = %q, want running", claimed.State)
	}

	current, err := markInstallJobTerminal(profilePath, staleQueued, InstallJobStateInterrupted,
		coded("job_interrupted", "stale queued worker check", nil))
	if errorCode(err) != "job_state_changed" {
		t.Fatalf("stale terminal write error code = %q, want job_state_changed", errorCode(err))
	}
	if current.State != InstallJobStateRunning {
		t.Fatalf("compare-and-swap returned state %q, want running", current.State)
	}
	written, readErr := readInstallJobRecord(profilePath, staleQueued.ID)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if written.State != InstallJobStateRunning || written.FinishedAt != nil || written.ErrorCode != "" {
		t.Fatalf("stale queued update overwrote running receipt: %#v", written)
	}
}

func TestRunInstallJobTreatsSocketLossAfterDurableClaimAsHandoff(t *testing.T) {
	directory, err := os.MkdirTemp("/tmp", "deploy-link-job-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	profilePath := filepath.Join(directory, "iphone.json")
	job := testInstallJob(t, profilePath, InstallJobStateQueued)
	if err := writeInstallJob(profilePath, job); err != nil {
		t.Fatal(err)
	}
	socketPath, err := SocketPath(profilePath)
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()

	type serverResult struct {
		request Request
		err     error
	}
	serverDone := make(chan serverResult, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			serverDone <- serverResult{err: acceptErr}
			return
		}
		defer connection.Close()
		request, readErr := readRequest(connection)
		if readErr != nil {
			serverDone <- serverResult{err: readErr}
			return
		}
		_, claimErr := claimInstallJob(profilePath, job.ID, Snapshot{
			State:        StateActive,
			DaemonID:     job.Baseline.DaemonID,
			Generation:   job.Baseline.Generation,
			InstallCount: job.Baseline.InstallCount,
		})
		serverDone <- serverResult{request: request, err: claimErr}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	result, err := RunInstallJob(ctx, profilePath, job.ID, nil)
	if err != nil {
		t.Fatalf("RunInstallJob returned socket loss after durable handoff: %v", err)
	}
	server := <-serverDone
	if server.err != nil {
		t.Fatal(server.err)
	}
	artifactPath, pathErr := installJobArtifactPath(profilePath, job.ID)
	if pathErr != nil {
		t.Fatal(pathErr)
	}
	if server.request.Command != "install" || server.request.JobID != job.ID || server.request.AppPath != artifactPath {
		t.Fatalf("worker request = %#v, want owned install snapshot", server.request)
	}
	if result.State != InstallJobStateRunning || result.StartedAt == nil || result.Terminal() {
		t.Fatalf("handoff result = %#v, want durable running receipt", result)
	}
	written, readErr := readInstallJobRecord(profilePath, job.ID)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if written.State != InstallJobStateRunning || written.FinishedAt != nil {
		t.Fatalf("socket loss changed claimed receipt: %#v", written)
	}
}

func TestReadInstallJobInterruptsExpiredQueueWithoutLaunchdWorker(t *testing.T) {
	profilePath := filepath.Join(t.TempDir(), "profiles", "iphone.json")
	job := testInstallJob(t, profilePath, InstallJobStateQueued)
	expired := time.Now().UTC().Add(-installJobLaunchGracePeriod - time.Second)
	job.WorkerScheduledAt = &expired
	job.UpdatedAt = expired
	if err := writeInstallJob(profilePath, job); err != nil {
		t.Fatal(err)
	}

	reconciled, err := ReadInstallJob(profilePath, job.ID)
	if err != nil {
		t.Fatal(err)
	}
	if reconciled.State != InstallJobStateInterrupted || reconciled.ErrorCode != "job_interrupted" || reconciled.FinishedAt == nil {
		t.Fatalf("expired queue reconciliation = %#v, want interrupted", reconciled)
	}
	written, readErr := readInstallJobRecord(profilePath, job.ID)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if written.State != InstallJobStateInterrupted || written.ErrorCode != "job_interrupted" {
		t.Fatalf("expired queue result was not durable: %#v", written)
	}
}

func TestCleanupInstallJobStagingRemovesOnlyExactJobStagingDirectories(t *testing.T) {
	root := t.TempDir()
	staging := installJobStagingDirectory(root, testInstallJobID)
	if err := os.Mkdir(staging, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(staging, "partial"), []byte("partial snapshot"), 0o600); err != nil {
		t.Fatal(err)
	}
	preservedDirectories := []string{
		testInstallJobID,
		"." + testInstallJobID + ".staging-extra",
		"." + testInstallJobID[:31] + ".staging",
		"." + strings.Repeat("g", 32) + ".staging",
		testInstallJobID + ".staging",
		"notes",
	}
	for _, name := range preservedDirectories {
		if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	preservedFile := filepath.Join(root, ".aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa.staging")
	if err := os.WriteFile(preservedFile, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := cleanupInstallJobStaging(root); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(staging); !os.IsNotExist(err) {
		t.Fatalf("exact staging directory still exists: %v", err)
	}
	for _, name := range preservedDirectories {
		if info, err := os.Stat(filepath.Join(root, name)); err != nil || !info.IsDir() {
			t.Fatalf("cleanup removed unrelated directory %q: %v", name, err)
		}
	}
	if info, err := os.Stat(preservedFile); err != nil || !info.Mode().IsRegular() {
		t.Fatalf("cleanup removed unrelated staging-shaped file: %v", err)
	}
}
