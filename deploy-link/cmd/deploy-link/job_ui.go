package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"os/exec"
	"time"

	"github.com/zjz-connect/xcode-deploy-link-deploy-beyond-local-network/internal/linkcore"
)

const jobUIPage = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>{{.DocumentTitle}} · Deploy Link</title>
  <style>
    :root { color-scheme: light dark; font-family: -apple-system, BlinkMacSystemFont, sans-serif; }
    body { margin: 0; min-height: 100vh; display: grid; place-items: center; background: Canvas; color: CanvasText; }
    main { width: min(640px, calc(100vw - 32px)); }
    .card { border: 1px solid color-mix(in srgb, CanvasText 14%, transparent); border-radius: 20px; padding: 24px; box-shadow: 0 14px 40px color-mix(in srgb, CanvasText 8%, transparent); }
    header { display: flex; align-items: start; justify-content: space-between; gap: 18px; }
    h1 { margin: 0; font-size: 24px; }
    .build { margin-top: 5px; opacity: .66; }
    .badge { border-radius: 999px; padding: 7px 11px; font-size: 13px; font-weight: 700; background: color-mix(in srgb, #377cf6 16%, Canvas); }
    .badge.done { background: color-mix(in srgb, #28a745 18%, Canvas); }
    .badge.problem { background: color-mix(in srgb, #d73a49 18%, Canvas); }
    .stage { margin: 30px 0 10px; font-size: 17px; font-weight: 650; }
    progress { width: 100%; height: 12px; accent-color: #377cf6; }
    .percent { margin-top: 8px; text-align: right; font-variant-numeric: tabular-nums; }
    dl { display: grid; grid-template-columns: 1fr auto; gap: 12px 20px; margin: 28px 0 0; }
    dt { opacity: .66; }
    dd { margin: 0; font-variant-numeric: tabular-nums; text-align: right; }
    .message { margin-top: 22px; padding: 13px 15px; border-radius: 12px; background: color-mix(in srgb, CanvasText 7%, transparent); }
    .message:empty { display: none; }
    footer { margin-top: 15px; text-align: center; font-size: 13px; opacity: .58; }
    @media (max-width: 480px) { .card { padding: 20px; } header { display: block; } .badge { display: inline-block; margin-top: 14px; } }
  </style>
</head>
<body>
<main>
  <section class="card" aria-labelledby="title">
    <header>
      <div><h1>{{.Title}}</h1><div class="build">{{.BuildLine}}</div></div>
      <div class="{{.BadgeClass}}" aria-live="polite">{{.StateLabel}}</div>
    </header>
    <div class="stage">{{.Stage}}</div>
    <progress max="100" value="{{.Percent}}" aria-label="Installation progress"></progress>
    <div class="percent">{{.Percent}}%</div>
    <dl>
      <dt>Elapsed</dt><dd>{{.Elapsed}}</dd>
      <dt>Last progress</dt><dd>{{.LastProgress}}</dd>
      <dt>Transferred</dt><dd>{{.Transferred}}</dd>
      <dt>Job</dt><dd>{{.JobShort}}</dd>
    </dl>
    <div class="message" role="status" aria-live="polite">{{.Message}}</div>
  </section>
  <footer>The install continues if this page is closed.</footer>
</main>
</body>
</html>`

var jobUITemplate = template.Must(template.New("job-ui").Parse(jobUIPage))

type jobUIStatus struct {
	State               string `json:"state"`
	Stalled             bool   `json:"stalled"`
	Percent             int    `json:"percent"`
	Stage               string `json:"stage"`
	Phase               string `json:"phase"`
	BytesSent           uint64 `json:"bytes_sent"`
	BytesTotal          uint64 `json:"bytes_total"`
	BundleIdentifier    string `json:"bundle_identifier"`
	Version             string `json:"version"`
	Build               string `json:"build"`
	JobShort            string `json:"job_short"`
	ElapsedSeconds      int64  `json:"elapsed_seconds"`
	LastProgressSeconds int64  `json:"last_progress_seconds"`
	Message             string `json:"message,omitempty"`
}

type jobUIView struct {
	DocumentTitle string
	Title         string
	BuildLine     string
	BadgeClass    string
	StateLabel    string
	Stage         string
	Percent       int
	Elapsed       string
	LastProgress  string
	Transferred   string
	JobShort      string
	Message       string
}

func jobUIViewFor(status jobUIStatus) jobUIView {
	state := status.State
	if status.Stalled {
		state = "stalled"
	}
	stateLabel := jobUIStateLabel(state)
	badgeClass := "badge"
	if status.State == linkcore.InstallJobStateInstalled {
		badgeClass += " done"
	} else if status.Stalled || status.State == linkcore.InstallJobStateFailed || status.State == linkcore.InstallJobStateInterrupted {
		badgeClass += " problem"
	}
	title := status.BundleIdentifier
	if title == "" {
		title = "iOS installation"
	}
	version := status.Version
	if version == "" {
		version = "Unknown version"
	}
	build := status.Build
	if build == "" {
		build = "unknown"
	}
	stage := status.Stage
	if status.Stalled {
		stage = "No forward progress for five minutes"
	} else if status.Phase == "installed" {
		stage = "InstallComplete"
	} else if status.Phase == "readback" {
		stage = "Verifying installed app"
	} else if status.Phase == "payload_complete" || status.Phase == "awaiting_device_progress" {
		stage = "Waiting for device installation"
	} else if status.Phase != "" && status.Phase != "device_installing" {
		stage = "Transferring app"
	} else if stage == "" {
		stage = stateLabel
	}
	message := status.Message
	if message == "" && status.Stalled {
		message = "The background job remains recorded. Check the safe diagnostics before retrying."
	}
	percent := status.Percent
	if percent < 0 {
		percent = 0
	} else if percent > 100 {
		percent = 100
	}
	return jobUIView{
		DocumentTitle: stateLabel,
		Title:         title,
		BuildLine:     version + " · build " + build,
		BadgeClass:    badgeClass,
		StateLabel:    stateLabel,
		Stage:         stage,
		Percent:       percent,
		Elapsed:       formatJobUIDuration(status.ElapsedSeconds),
		LastProgress:  formatJobUIDuration(status.LastProgressSeconds) + " ago",
		Transferred:   formatJobUIBytes(status.BytesSent, status.BytesTotal),
		JobShort:      status.JobShort,
		Message:       message,
	}
}

func jobUIStateLabel(state string) string {
	switch state {
	case "queued":
		return "Queued"
	case "running":
		return "Installing"
	case "installed":
		return "Installed"
	case "failed":
		return "Failed"
	case "interrupted":
		return "Interrupted"
	case "stalled":
		return "Stalled"
	default:
		return "Unknown"
	}
}

func formatJobUIDuration(seconds int64) string {
	if seconds < 60 {
		return fmt.Sprintf("%ds", seconds)
	}
	minutes := seconds / 60
	if minutes < 60 {
		return fmt.Sprintf("%dm %ds", minutes, seconds%60)
	}
	return fmt.Sprintf("%dh %dm", minutes/60, minutes%60)
}

func formatJobUIBytes(sent uint64, total uint64) string {
	if total == 0 {
		return "—"
	}
	const megabyte = 1_000_000
	return fmt.Sprintf("%.1f / %.1f MB", float64(sent)/megabyte, float64(total)/megabyte)
}

func jobUIStatusFor(profilePath string, jobID string, stallTimeout time.Duration) (jobUIStatus, error) {
	job, err := linkcore.ReadInstallJob(profilePath, jobID)
	if err != nil {
		return jobUIStatus{}, err
	}
	now := time.Now().UTC()
	startedAt := job.SubmittedAt
	if job.StartedAt != nil {
		startedAt = *job.StartedAt
	}
	endedAt := now
	if job.FinishedAt != nil {
		endedAt = *job.FinishedAt
	}
	lastProgress := now.Sub(job.UpdatedAt)
	if lastProgress < 0 {
		lastProgress = 0
	}
	message := ""
	if job.State == linkcore.InstallJobStateFailed || job.State == linkcore.InstallJobStateInterrupted {
		message = "The install did not finish."
		if job.ErrorCode != "" {
			message = "Deploy Link stopped with " + job.ErrorCode + "."
		}
	}
	jobShort := job.ID
	if len(jobShort) > 8 {
		jobShort = jobShort[:8]
	}
	return jobUIStatus{
		State:               job.State,
		Stalled:             job.State == linkcore.InstallJobStateRunning && lastProgress >= stallTimeout,
		Percent:             job.Progress.Percent,
		Stage:               job.Progress.Status,
		Phase:               job.Progress.Phase,
		BytesSent:           job.Progress.BytesSent,
		BytesTotal:          job.Progress.BytesTotal,
		BundleIdentifier:    job.Artifact.BundleIdentifier,
		Version:             job.Artifact.Version,
		Build:               job.Artifact.Build,
		JobShort:            jobShort,
		ElapsedSeconds:      max(0, int64(endedAt.Sub(startedAt).Seconds())),
		LastProgressSeconds: max(0, int64(lastProgress.Seconds())),
		Message:             message,
	}, nil
}

func runJobUI(ctx context.Context, profilePath string, jobID string) error {
	config, err := linkcore.LoadConfig(profilePath)
	if err != nil {
		return err
	}
	if _, err := linkcore.ReadInstallJob(profilePath, jobID); err != nil {
		return err
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("job UI could not listen on localhost: %w", err)
	}
	defer listener.Close()

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/" {
			http.NotFound(writer, request)
			return
		}
		jobUIHeaders(writer)
		writer.Header().Set("Content-Type", "text/html; charset=utf-8")
		status, statusErr := jobUIStatusFor(profilePath, jobID, config.InstallStallTimeout())
		if statusErr != nil {
			http.Error(writer, "The background job status is temporarily unavailable.", http.StatusServiceUnavailable)
			return
		}
		if status.State != linkcore.InstallJobStateInstalled && status.State != linkcore.InstallJobStateFailed && status.State != linkcore.InstallJobStateInterrupted {
			writer.Header().Set("Refresh", "2")
		}
		if err := jobUITemplate.Execute(writer, jobUIViewFor(status)); err != nil {
			return
		}
	})
	mux.HandleFunc("/api/status", func(writer http.ResponseWriter, _ *http.Request) {
		jobUIHeaders(writer)
		writer.Header().Set("Content-Type", "application/json")
		status, statusErr := jobUIStatusFor(profilePath, jobID, config.InstallStallTimeout())
		if statusErr != nil {
			writer.WriteHeader(http.StatusServiceUnavailable)
			_ = json.NewEncoder(writer).Encode(map[string]string{"message": "The background job status is temporarily unavailable."})
			return
		}
		_ = json.NewEncoder(writer).Encode(status)
	})

	server := &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
		IdleTimeout:       30 * time.Second,
	}
	uiContext, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		<-uiContext.Done()
		shutdownContext, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = server.Shutdown(shutdownContext)
	}()
	go stopJobUIAfterTerminal(uiContext, cancel, profilePath, jobID)

	url := "http://" + listener.Addr().String()
	emit(map[string]any{"ok": true, "event": "job_ui_started", "url": url, "job_id": jobID})
	if err := exec.Command("/usr/bin/open", url).Start(); err != nil {
		return fmt.Errorf("job UI could not open the browser: %w", err)
	}
	serveErr := server.Serve(listener)
	if serveErr != nil && !errors.Is(serveErr, http.ErrServerClosed) {
		return serveErr
	}
	return nil
}

func jobUIHeaders(writer http.ResponseWriter) {
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'none'; style-src 'unsafe-inline'")
	writer.Header().Set("X-Content-Type-Options", "nosniff")
}

func stopJobUIAfterTerminal(ctx context.Context, cancel context.CancelFunc, profilePath string, jobID string) {
	deadline := time.NewTimer(2 * time.Hour)
	defer deadline.Stop()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	var terminalAt time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-deadline.C:
			cancel()
			return
		case <-ticker.C:
			job, err := linkcore.ReadInstallJob(profilePath, jobID)
			if err != nil {
				continue
			}
			if job.Terminal() && terminalAt.IsZero() {
				terminalAt = time.Now()
			}
			if !terminalAt.IsZero() && time.Since(terminalAt) >= 10*time.Minute {
				cancel()
				return
			}
		}
	}
}

func max(first int64, second int64) int64 {
	if first > second {
		return first
	}
	return second
}
