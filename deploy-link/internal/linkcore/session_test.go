package linkcore

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestInstallProgressTrackerIgnoresDuplicateCallbacks(t *testing.T) {
	tracker := newInstallProgressTracker()
	if !tracker.transferAdvanced("zip_payload", 0) {
		t.Fatal("first transfer phase was not activity")
	}
	if tracker.transferAdvanced("zip_payload", 0) {
		t.Fatal("duplicate transfer callback was activity")
	}
	if !tracker.transferAdvanced("zip_payload", 32*1024) {
		t.Fatal("increased byte count was not activity")
	}
	if tracker.transferAdvanced("zip_payload", 32*1024) {
		t.Fatal("repeated byte count was activity")
	}
	if !tracker.transferAdvanced("awaiting_device_progress", 32*1024) {
		t.Fatal("transfer phase change was not activity")
	}

	if !tracker.deviceAdvanced(5, "CreatingStagingDirectory") {
		t.Fatal("first device progress was not activity")
	}
	if tracker.deviceAdvanced(5, "CreatingStagingDirectory") {
		t.Fatal("duplicate device progress was activity")
	}
	if !tracker.deviceAdvanced(15, "ExtractingPackage") {
		t.Fatal("advanced device progress was not activity")
	}
}

func TestWaitForInstallResultAllowsWorkLongerThanStallWindowWhenProgressContinues(t *testing.T) {
	result := make(chan error, 1)
	activity := newInstallActivityMonitor()
	stallTimeout := 300 * time.Millisecond
	go func() {
		time.Sleep(200 * time.Millisecond)
		activity.signal()
		time.Sleep(200 * time.Millisecond)
		result <- nil
	}()
	started := time.Now()
	if err := waitForInstallResult(context.Background(), result, activity, stallTimeout); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed <= stallTimeout {
		t.Fatalf("install returned after %s, want longer than original stall window %s", elapsed, stallTimeout)
	}
}

func TestWaitForInstallResultPrefersReadyCompletionAtStallBoundary(t *testing.T) {
	result := make(chan error, 1)
	result <- nil
	activity := newInstallActivityMonitor()
	activity.mu.Lock()
	activity.last = time.Now().Add(-time.Second)
	activity.mu.Unlock()

	if err := waitForInstallResult(context.Background(), result, activity, time.Nanosecond); err != nil {
		t.Fatalf("ready result lost to stall timer: %v", err)
	}
}

func TestWaitForInstallResultStopsAfterNoProgress(t *testing.T) {
	result := make(chan error)
	activity := newInstallActivityMonitor()
	stallTimeout := 30 * time.Millisecond
	started := time.Now()
	err := waitForInstallResult(context.Background(), result, activity, stallTimeout)
	if !errors.Is(err, errInstallStalled) {
		t.Fatalf("error = %v, want install stalled", err)
	}
	if elapsed := time.Since(started); elapsed < stallTimeout || elapsed > 500*time.Millisecond {
		t.Fatalf("stall elapsed = %s", elapsed)
	}
}

func TestWaitForInstallResultHonorsDaemonCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error)
	activity := newInstallActivityMonitor()
	cancel()
	if err := waitForInstallResult(ctx, result, activity, time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context canceled", err)
	}
}

func TestFinishInstallContextErrorDistinguishesStopFromHardLimit(t *testing.T) {
	stopErr := finishInstallContextError(newInstallTransferDiagnostic(context.Background()), context.Canceled)
	if errorCode(stopErr) != "install_failed" || publicError(stopErr) != "install_failed: installation stopped because Deploy Link is shutting down" {
		t.Fatalf("stop error = %v", stopErr)
	}

	timeoutErr := finishInstallContextError(newInstallTransferDiagnostic(context.Background()), context.DeadlineExceeded)
	if errorCode(timeoutErr) != "install_failed" || publicError(timeoutErr) != "install_failed: installation reached its hard safety limit" {
		t.Fatalf("timeout error = %v", timeoutErr)
	}
}
