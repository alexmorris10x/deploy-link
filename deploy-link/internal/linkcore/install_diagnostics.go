package linkcore

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

const installProgressLogInterval = 5 * time.Second

type installDiagnosticMetadata struct {
	attempt    uint64
	generation uint64
	startedAt  time.Time
}

type installDiagnosticContextKey struct{}

func withInstallDiagnosticContext(ctx context.Context, attempt uint64, generation uint64, startedAt time.Time) context.Context {
	return context.WithValue(ctx, installDiagnosticContextKey{}, installDiagnosticMetadata{
		attempt:    attempt,
		generation: generation,
		startedAt:  startedAt,
	})
}

type installTransferDiagnostic struct {
	mu sync.Mutex

	metadata   installDiagnosticMetadata
	phase      string
	bytesSent  uint64
	bytesTotal uint64
	lastWrite  time.Time
	lastLog    time.Time
	terminal   bool
}

func newInstallTransferDiagnostic(ctx context.Context) *installTransferDiagnostic {
	metadata, _ := ctx.Value(installDiagnosticContextKey{}).(installDiagnosticMetadata)
	if metadata.startedAt.IsZero() {
		metadata.startedAt = time.Now()
	}
	return &installTransferDiagnostic{metadata: metadata}
}

func (diagnostic *installTransferDiagnostic) begin(phase string) {
	diagnostic.mu.Lock()
	defer diagnostic.mu.Unlock()
	if diagnostic.terminal {
		return
	}
	diagnostic.phase = phase
	diagnostic.logLocked("started", time.Now())
}

func (diagnostic *installTransferDiagnostic) transferProgress(phase string, bytesSent uint64, bytesTotal uint64) {
	now := time.Now()
	diagnostic.mu.Lock()
	defer diagnostic.mu.Unlock()
	if diagnostic.terminal {
		return
	}
	previousTotal := diagnostic.bytesTotal
	if bytesSent > diagnostic.bytesSent {
		diagnostic.lastWrite = now
	}
	diagnostic.phase = phase
	diagnostic.bytesSent = bytesSent
	diagnostic.bytesTotal = bytesTotal
	force := previousTotal == 0 && bytesTotal > 0
	result := "progress"
	switch phase {
	case "payload_complete":
		result = "payload_complete"
		force = true
	case "awaiting_device_progress":
		result = "waiting"
		force = true
	}
	if !force && !diagnostic.lastLog.IsZero() && now.Sub(diagnostic.lastLog) < installProgressLogInterval {
		return
	}
	diagnostic.logLocked(result, now)
}

func (diagnostic *installTransferDiagnostic) deviceProgress(percent int, status string) {
	diagnostic.mu.Lock()
	defer diagnostic.mu.Unlock()
	if diagnostic.terminal {
		return
	}
	diagnostic.phase = "device_installing"
	diagnostic.logLocked("progress", time.Now(),
		"device_percent", percent,
		"device_status", allowedDeviceInstallStatus(status),
	)
}

func (diagnostic *installTransferDiagnostic) readback() {
	diagnostic.mu.Lock()
	defer diagnostic.mu.Unlock()
	if diagnostic.terminal {
		return
	}
	diagnostic.phase = "readback"
	diagnostic.logLocked("waiting", time.Now())
}

func (diagnostic *installTransferDiagnostic) finish(result string, causeKey string, cause string) {
	diagnostic.mu.Lock()
	defer diagnostic.mu.Unlock()
	if diagnostic.terminal {
		return
	}
	diagnostic.terminal = true
	if causeKey == "" {
		diagnostic.logLocked(result, time.Now())
		return
	}
	diagnostic.logLocked(result, time.Now(), causeKey, cause)
}

func (diagnostic *installTransferDiagnostic) logLocked(result string, now time.Time, extra ...any) {
	elapsed := now.Sub(diagnostic.metadata.startedAt)
	if elapsed < 0 {
		elapsed = 0
	}
	var throughput uint64
	if elapsed > 0 {
		throughput = uint64(float64(diagnostic.bytesSent) / elapsed.Seconds())
	}
	lastWriteAgeMilliseconds := int64(-1)
	if !diagnostic.lastWrite.IsZero() {
		lastWriteAgeMilliseconds = now.Sub(diagnostic.lastWrite).Milliseconds()
	}
	percent := 0
	if diagnostic.bytesTotal > 0 {
		percent = int(diagnostic.bytesSent * 100 / diagnostic.bytesTotal)
	}
	attributes := []any{
		"event", "install_transfer",
		"phase", diagnostic.phase,
		"result", result,
		"generation", diagnostic.metadata.generation,
		"install_attempt", diagnostic.metadata.attempt,
		"bytes_sent", diagnostic.bytesSent,
		"bytes_total", diagnostic.bytesTotal,
		"percent", percent,
		"elapsed_ms", elapsed.Milliseconds(),
		"throughput_bps", throughput,
		"last_write_age_ms", lastWriteAgeMilliseconds,
	}
	attributes = append(attributes, extra...)
	slog.Info("Deploy Link install diagnostic", attributes...)
	diagnostic.lastLog = now
}

func allowedDeviceInstallStatus(status string) string {
	switch status {
	case "CreatingStagingDirectory",
		"VerifyingApplication",
		"CreatingContainer",
		"ExtractingPackage",
		"InspectingPackage",
		"PreflightingApplication",
		"InstallingApplication",
		"PostflightingApplication",
		"SandboxingApplication",
		"GeneratingApplicationMap",
		"InstallComplete",
		"DataComplete":
		return status
	case "":
		return "unspecified"
	default:
		return "other"
	}
}
