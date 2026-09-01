package linkcore

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

const InstallJobSchemaVersion = 1

const (
	InstallJobStateQueued      = "queued"
	InstallJobStateRunning     = "running"
	InstallJobStateInstalled   = "installed"
	InstallJobStateFailed      = "failed"
	InstallJobStateInterrupted = "interrupted"
)

const installJobLaunchAgentLabelPrefix = launchAgentLabelPrefix + ".install-job"
const installJobLaunchGracePeriod = 30 * time.Second

type InstallJobArtifact struct {
	BundleIdentifier string `json:"bundle_identifier"`
	Version          string `json:"version,omitempty"`
	Build            string `json:"build,omitempty"`
	ContentSHA256    string `json:"content_sha256"`
}

type InstallJobBaseline struct {
	DaemonID     string `json:"daemon_id"`
	Generation   uint64 `json:"generation"`
	InstallCount uint64 `json:"install_count"`
}

type InstallJobProgress struct {
	Percent    int    `json:"percent"`
	Status     string `json:"status,omitempty"`
	Phase      string `json:"phase,omitempty"`
	BytesSent  uint64 `json:"bytes_sent,omitempty"`
	BytesTotal uint64 `json:"bytes_total,omitempty"`
}

type InstallJobResult struct {
	Event        string `json:"event"`
	DaemonID     string `json:"daemon_id"`
	Generation   uint64 `json:"generation"`
	InstallCount uint64 `json:"install_count"`
}

type InstallJob struct {
	SchemaVersion     int                `json:"schema_version"`
	ID                string             `json:"id"`
	State             string             `json:"state"`
	Artifact          InstallJobArtifact `json:"artifact"`
	Baseline          InstallJobBaseline `json:"baseline"`
	Progress          InstallJobProgress `json:"progress"`
	Result            *InstallJobResult  `json:"result,omitempty"`
	ErrorCode         string             `json:"error_code,omitempty"`
	Error             string             `json:"error,omitempty"`
	SubmittedAt       time.Time          `json:"submitted_at"`
	WorkerScheduledAt *time.Time         `json:"worker_scheduled_at,omitempty"`
	StartedAt         *time.Time         `json:"started_at,omitempty"`
	FinishedAt        *time.Time         `json:"finished_at,omitempty"`
	UpdatedAt         time.Time          `json:"updated_at"`
}

func (job InstallJob) Terminal() bool {
	switch job.State {
	case InstallJobStateInstalled, InstallJobStateFailed, InstallJobStateInterrupted:
		return true
	default:
		return false
	}
}

type installJobLaunchAgentSpec struct {
	label         string
	plistPath     string
	serviceTarget string
	standardOut   string
	standardError string
}

func validInstallJobID(value string) bool {
	if len(value) != 32 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 16
}

func newInstallJobID() (string, error) {
	buffer := make([]byte, 16)
	if _, err := rand.Read(buffer); err != nil {
		return "", coded("job_create_failed", "could not create an install job identifier", err)
	}
	return hex.EncodeToString(buffer), nil
}

func installJobsRoot(profilePath string) (string, error) {
	if !filepath.IsAbs(profilePath) {
		return "", coded("job_invalid", "profile path must be absolute", nil)
	}
	cleanProfile := filepath.Clean(profilePath)
	base := strings.TrimSuffix(filepath.Base(cleanProfile), filepath.Ext(cleanProfile))
	return filepath.Join(filepath.Dir(cleanProfile), "."+base+"-install-jobs"), nil
}

func installJobDirectory(profilePath string, jobID string) (string, error) {
	if !validInstallJobID(jobID) {
		return "", coded("job_invalid", "install job identifier is invalid", nil)
	}
	root, err := installJobsRoot(profilePath)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, jobID), nil
}

func installJobRecordPath(profilePath string, jobID string) (string, error) {
	directory, err := installJobDirectory(profilePath, jobID)
	if err != nil {
		return "", err
	}
	return filepath.Join(directory, "receipt.json"), nil
}

func installJobArtifactPath(profilePath string, jobID string) (string, error) {
	directory, err := installJobDirectory(profilePath, jobID)
	if err != nil {
		return "", err
	}
	return filepath.Join(directory, "artifact.app"), nil
}

func installJobSubmissionLockPath(profilePath string) (string, error) {
	root, err := installJobsRoot(profilePath)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "submit.lock"), nil
}

func installJobWorkerLockPath(profilePath string, jobID string) (string, error) {
	directory, err := installJobDirectory(profilePath, jobID)
	if err != nil {
		return "", err
	}
	return filepath.Join(directory, "worker.lock"), nil
}

func installJobRecordLockPath(profilePath string, jobID string) (string, error) {
	directory, err := installJobDirectory(profilePath, jobID)
	if err != nil {
		return "", err
	}
	return filepath.Join(directory, "receipt.lock"), nil
}

func buildInstallJobLaunchAgentSpec(profilePath string, jobID string, homeDirectory string, target string) installJobLaunchAgentSpec {
	digest := sha256.Sum256([]byte(filepath.Clean(profilePath) + "\x00" + jobID))
	label := installJobLaunchAgentLabelPrefix + "." + hex.EncodeToString(digest[:16])
	directory, _ := installJobDirectory(profilePath, jobID)
	return installJobLaunchAgentSpec{
		label:         label,
		plistPath:     filepath.Join(homeDirectory, "Library", "LaunchAgents", label+".plist"),
		serviceTarget: target + "/" + label,
		standardOut:   filepath.Join(directory, "worker.log"),
		standardError: filepath.Join(directory, "worker.error.log"),
	}
}

func installJobLaunchAgentSpecForProfile(profilePath string, jobID string) (installJobLaunchAgentSpec, error) {
	if !filepath.IsAbs(profilePath) || !validInstallJobID(jobID) {
		return installJobLaunchAgentSpec{}, coded("job_invalid", "profile path or install job identifier is invalid", nil)
	}
	homeDirectory, err := os.UserHomeDir()
	if err != nil {
		return installJobLaunchAgentSpec{}, coded("job_launch_failed", "could not locate the user home directory", err)
	}
	return buildInstallJobLaunchAgentSpec(profilePath, jobID, homeDirectory, launchTarget()), nil
}

func installJobLaunchAgentContents(spec installJobLaunchAgentSpec, binaryPath string, profilePath string, jobID string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>%s</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>job</string>
    <string>run</string>
    <string>--profile</string>
    <string>%s</string>
    <string>%s</string>
  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>ProcessType</key>
  <string>Standard</string>
  <key>Umask</key>
  <integer>63</integer>
  <key>StandardOutPath</key>
  <string>%s</string>
  <key>StandardErrorPath</key>
  <string>%s</string>
</dict>
</plist>
`, spec.label, xmlEscape(binaryPath), xmlEscape(profilePath), xmlEscape(jobID),
		xmlEscape(spec.standardOut), xmlEscape(spec.standardError))
}

func encodeInstallJob(job InstallJob) ([]byte, error) {
	if job.SchemaVersion != InstallJobSchemaVersion || !validInstallJobID(job.ID) {
		return nil, coded("job_invalid", "install job receipt is invalid", nil)
	}
	data, err := json.MarshalIndent(job, "", "  ")
	if err != nil {
		return nil, coded("job_write_failed", "could not encode the install job receipt", err)
	}
	return append(data, '\n'), nil
}

func writeInstallJobReceiptAt(path string, job InstallJob) error {
	data, err := encodeInstallJob(job)
	if err != nil {
		return err
	}
	if err := writeAtomic(path, data, 0o600); err != nil {
		return coded("job_write_failed", "could not commit the install job receipt", err)
	}
	return nil
}

func writeInstallJobUnlocked(profilePath string, job InstallJob) error {
	path, err := installJobRecordPath(profilePath, job.ID)
	if err != nil {
		return err
	}
	return writeInstallJobReceiptAt(path, job)
}

func writeInstallJob(profilePath string, job InstallJob) error {
	lockPath, err := installJobRecordLockPath(profilePath, job.ID)
	if err != nil {
		return err
	}
	lock, err := acquireInstallJobRecordLock(lockPath)
	if err != nil {
		return err
	}
	defer lock.Close()
	return writeInstallJobUnlocked(profilePath, job)
}

func acquireInstallJobRecordLock(path string) (*advisoryLock, error) {
	deadline := time.Now().Add(5 * time.Second)
	for {
		lock, err := acquireAdvisoryLock(path, "job_receipt_busy", "another process is updating the install job receipt")
		if err == nil {
			return lock, nil
		}
		if errorCode(err) != "job_receipt_busy" || !time.Now().Before(deadline) {
			return nil, err
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func readInstallJobRecord(profilePath string, jobID string) (InstallJob, error) {
	path, err := installJobRecordPath(profilePath, jobID)
	if err != nil {
		return InstallJob{}, err
	}
	if err := requirePrivateRegularFile(path, "job_not_found", "job_permissions"); err != nil {
		return InstallJob{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return InstallJob{}, coded("job_read_failed", "could not read the install job receipt", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var job InstallJob
	if err := decoder.Decode(&job); err != nil {
		return InstallJob{}, coded("job_invalid", "could not decode the install job receipt", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return InstallJob{}, coded("job_invalid", "install job receipt must contain one JSON object", err)
	}
	if job.SchemaVersion != InstallJobSchemaVersion || job.ID != jobID || !validInstallJobState(job.State) {
		return InstallJob{}, coded("job_invalid", "install job receipt has invalid identity or state", nil)
	}
	if job.Artifact.BundleIdentifier == "" || job.Artifact.Version == "" || job.Artifact.Build == "" || !validSHA256(job.Artifact.ContentSHA256) || !validInstallJobID(job.Baseline.DaemonID) {
		return InstallJob{}, coded("job_invalid", "install job receipt has an invalid artifact or daemon identity", nil)
	}
	if job.State == InstallJobStateInstalled {
		if job.FinishedAt == nil || job.Result == nil || job.Result.Event != "installed" || job.Result.DaemonID != job.Baseline.DaemonID || job.Result.Generation != job.Baseline.Generation || job.Result.InstallCount != job.Baseline.InstallCount+1 {
			return InstallJob{}, coded("job_invalid", "installed job receipt has invalid completion proof", nil)
		}
	}
	return job, nil
}

func validInstallJobState(state string) bool {
	switch state {
	case InstallJobStateQueued, InstallJobStateRunning, InstallJobStateInstalled, InstallJobStateFailed, InstallJobStateInterrupted:
		return true
	default:
		return false
	}
}

func ReadInstallJob(profilePath string, jobID string) (InstallJob, error) {
	job, err := readInstallJobRecord(profilePath, jobID)
	if err != nil {
		return InstallJob{}, err
	}
	return reconcileQueuedInstallJob(profilePath, job)
}

func installJobLaunchAgentIsRunning(profilePath string, jobID string) bool {
	spec, err := installJobLaunchAgentSpecForProfile(profilePath, jobID)
	if err != nil {
		return false
	}
	output, err := exec.Command("/bin/launchctl", "print", spec.serviceTarget).Output()
	return err == nil && bytes.Contains(output, []byte("state = running"))
}

func reconcileQueuedInstallJob(profilePath string, job InstallJob) (InstallJob, error) {
	if job.State != InstallJobStateQueued || job.WorkerScheduledAt == nil || time.Since(*job.WorkerScheduledAt) < installJobLaunchGracePeriod {
		return job, nil
	}
	if installJobLaunchAgentIsRunning(profilePath, job.ID) {
		return job, nil
	}
	updated, err := markInstallJobTerminal(profilePath, job, InstallJobStateInterrupted,
		coded("job_interrupted", "the one-shot worker ended before the daemon accepted the install", nil))
	if errorCode(err) == "job_state_changed" {
		return readInstallJobRecord(profilePath, job.ID)
	}
	if updated.State == InstallJobStateInterrupted && errorCode(err) == "job_interrupted" {
		return updated, nil
	}
	return updated, err
}

func daemonStatus(ctx context.Context, profilePath string) (Response, error) {
	statusContext, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	var response Response
	err := Call(statusContext, profilePath, Request{Command: "status"}, func(value Response) {
		if value.Final {
			response = value
		}
	})
	if err != nil {
		return Response{}, err
	}
	if response.Event != "status" || !response.OK {
		return Response{}, coded("job_preflight_failed", "Deploy Link returned no usable status", nil)
	}
	return response, nil
}

func activeInstallJob(profilePath string) (InstallJob, bool, error) {
	root, err := installJobsRoot(profilePath)
	if err != nil {
		return InstallJob{}, false, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return InstallJob{}, false, nil
		}
		return InstallJob{}, false, coded("job_read_failed", "could not inspect install jobs", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() || !validInstallJobID(entry.Name()) {
			continue
		}
		job, err := readInstallJobRecord(profilePath, entry.Name())
		if err != nil {
			return InstallJob{}, false, err
		}
		if !job.Terminal() {
			return job, true, nil
		}
	}
	return InstallJob{}, false, nil
}

func installJobStagingDirectory(root string, jobID string) string {
	return filepath.Join(root, "."+jobID+".staging")
}

func cleanupInstallJobStaging(root string) error {
	entries, err := os.ReadDir(root)
	if err != nil {
		return coded("job_read_failed", "could not inspect install job staging directories", err)
	}
	for _, entry := range entries {
		name := entry.Name()
		if !entry.IsDir() || !strings.HasPrefix(name, ".") || !strings.HasSuffix(name, ".staging") {
			continue
		}
		jobID := strings.TrimSuffix(strings.TrimPrefix(name, "."), ".staging")
		if !validInstallJobID(jobID) {
			continue
		}
		if err := os.RemoveAll(filepath.Join(root, name)); err != nil {
			return coded("job_create_failed", "could not clean an incomplete install job snapshot", err)
		}
	}
	return nil
}

func reconcileRunningInstallJob(profilePath string) error {
	job, found, err := activeInstallJob(profilePath)
	if err != nil || !found || job.State != InstallJobStateRunning {
		return err
	}
	updated, terminalErr := markInstallJobTerminal(profilePath, job, InstallJobStateInterrupted,
		coded("job_interrupted", "the Deploy Link daemon restarted during the install", nil))
	if updated.State == InstallJobStateInterrupted && errorCode(terminalErr) == "job_interrupted" {
		return nil
	}
	return terminalErr
}

func copyInstallJobArtifact(ctx context.Context, sourcePath string, destinationPath string) error {
	command := exec.CommandContext(ctx, "/usr/bin/ditto", sourcePath, destinationPath)
	if output, err := command.CombinedOutput(); err != nil {
		return coded("job_snapshot_failed", fmt.Sprintf("could not copy the app snapshot (%d diagnostic bytes)", len(output)), err)
	}
	return nil
}

func sameInstallJobArtifact(first AppBundle, second AppBundle) bool {
	return first.BundleIdentifier == second.BundleIdentifier && first.Version == second.Version && first.Build == second.Build
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func writeInstallJobDigestField(writer io.Writer, value string) error {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	if _, err := writer.Write(size[:]); err != nil {
		return err
	}
	_, err := io.WriteString(writer, value)
	return err
}

func digestInstallJobArtifact(ctx context.Context, appPath string) (string, error) {
	root := filepath.Clean(appPath)
	digest := sha256.New()
	buffer := make([]byte, 128*1024)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		kind := ""
		switch {
		case entry.IsDir():
			kind = "directory"
		case entry.Type()&os.ModeSymlink != 0:
			kind = "symlink"
		case info.Mode().IsRegular():
			kind = "file"
		default:
			return fmt.Errorf("unsupported file type at %s", relative)
		}
		for _, value := range []string{kind, filepath.ToSlash(relative), info.Mode().Perm().String()} {
			if err := writeInstallJobDigestField(digest, value); err != nil {
				return err
			}
		}
		switch kind {
		case "symlink":
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			return writeInstallJobDigestField(digest, target)
		case "file":
			file, err := os.Open(path)
			if err != nil {
				return err
			}
			_, copyErr := io.CopyBuffer(digest, file, buffer)
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			return closeErr
		default:
			return nil
		}
	})
	if err != nil {
		return "", coded("job_snapshot_failed", "could not verify the app snapshot contents", err)
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func prepareInstallJobLog(path string) error {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return coded("job_launch_failed", "could not prepare an install job log", err)
	}
	chmodErr := file.Chmod(0o600)
	closeErr := file.Close()
	if chmodErr != nil {
		return coded("job_launch_failed", "could not protect an install job log", chmodErr)
	}
	if closeErr != nil {
		return coded("job_launch_failed", "could not close an install job log", closeErr)
	}
	return nil
}

func launchInstallJob(binaryPath string, profilePath string, jobID string) error {
	spec, err := installJobLaunchAgentSpecForProfile(profilePath, jobID)
	if err != nil {
		return err
	}
	for _, path := range []string{spec.standardOut, spec.standardError} {
		if err := prepareInstallJobLog(path); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Dir(spec.plistPath), 0o755); err != nil {
		return coded("job_launch_failed", "could not create the LaunchAgents directory", err)
	}
	contents := installJobLaunchAgentContents(spec, binaryPath, profilePath, jobID)
	if err := writeAtomic(spec.plistPath, []byte(contents), 0o600); err != nil {
		return coded("job_launch_failed", "could not write the one-shot LaunchAgent", err)
	}
	if output, err := exec.Command("/bin/launchctl", "bootstrap", launchTarget(), spec.plistPath).CombinedOutput(); err != nil {
		return coded("job_launch_failed", fmt.Sprintf("launchctl bootstrap failed (%d diagnostic bytes)", len(output)), err)
	}
	return nil
}

func terminalInstallJob(job InstallJob, state string, err error) InstallJob {
	now := time.Now().UTC()
	job.State = state
	job.UpdatedAt = now
	job.FinishedAt = &now
	if err != nil {
		job.ErrorCode, job.Error = ErrorDetails(err)
	}
	return job
}

func claimInstallJob(profilePath string, jobID string, snapshot Snapshot) (InstallJob, error) {
	lockPath, err := installJobRecordLockPath(profilePath, jobID)
	if err != nil {
		return InstallJob{}, err
	}
	lock, err := acquireInstallJobRecordLock(lockPath)
	if err != nil {
		return InstallJob{}, err
	}
	defer lock.Close()
	job, err := readInstallJobRecord(profilePath, jobID)
	if err != nil {
		return InstallJob{}, err
	}
	if job.State != InstallJobStateQueued {
		return job, coded("job_state_changed", "install job is no longer queued", nil)
	}
	if snapshot.State != StateActive || snapshot.DaemonID != job.Baseline.DaemonID || snapshot.Generation != job.Baseline.Generation || snapshot.InstallCount != job.Baseline.InstallCount {
		failure := coded("job_interrupted", "Deploy Link session changed before the install started", nil)
		job = terminalInstallJob(job, InstallJobStateInterrupted, failure)
		if writeErr := writeInstallJobUnlocked(profilePath, job); writeErr != nil {
			return job, writeErr
		}
		return job, failure
	}
	now := time.Now().UTC()
	job.State = InstallJobStateRunning
	job.StartedAt = &now
	job.UpdatedAt = now
	if err := writeInstallJobUnlocked(profilePath, job); err != nil {
		return job, err
	}
	return job, nil
}

func markInstallJobTerminal(profilePath string, job InstallJob, state string, err error) (InstallJob, error) {
	lockPath, lockPathErr := installJobRecordLockPath(profilePath, job.ID)
	if lockPathErr != nil {
		return job, lockPathErr
	}
	lock, lockErr := acquireInstallJobRecordLock(lockPath)
	if lockErr != nil {
		return job, lockErr
	}
	defer lock.Close()
	current, readErr := readInstallJobRecord(profilePath, job.ID)
	if readErr != nil {
		return job, readErr
	}
	if current.Terminal() {
		if current.State == InstallJobStateInstalled {
			return current, nil
		}
		return current, coded(current.ErrorCode, strings.TrimPrefix(current.Error, current.ErrorCode+": "), nil)
	}
	if current.State != job.State || !current.UpdatedAt.Equal(job.UpdatedAt) {
		return current, coded("job_state_changed", "install job state changed before the terminal receipt was written", nil)
	}
	current.Progress = job.Progress
	current.Result = job.Result
	current = terminalInstallJob(current, state, err)
	if writeErr := writeInstallJobUnlocked(profilePath, current); writeErr != nil {
		return current, writeErr
	}
	return current, err
}

func SubmitInstallJob(ctx context.Context, binaryPath string, profilePath string, appPath string) (InstallJob, error) {
	if !filepath.IsAbs(binaryPath) || !filepath.IsAbs(profilePath) || !filepath.IsAbs(appPath) {
		return InstallJob{}, coded("job_invalid", "binary, profile, and app paths must be absolute", nil)
	}
	binaryInfo, err := os.Stat(binaryPath)
	if err != nil || !binaryInfo.Mode().IsRegular() || binaryInfo.Mode().Perm()&0o111 == 0 {
		return InstallJob{}, coded("job_invalid", "Deploy Link binary must be an executable regular file", err)
	}
	if _, err := LoadConfig(profilePath); err != nil {
		return InstallJob{}, err
	}
	status, err := daemonStatus(ctx, profilePath)
	if err != nil {
		return InstallJob{}, err
	}
	if status.ControlSchema != ControlSchemaVersion || !validInstallJobID(status.DaemonID) {
		return InstallJob{}, coded("daemon_upgrade_required", "the running Deploy Link service does not support install jobs", nil)
	}
	if status.State != StateActive {
		return InstallJob{}, coded("session_not_active", "Deploy Link must be active before an install job is submitted", nil)
	}
	validationContext, validationCancel := context.WithTimeout(ctx, 30*time.Second)
	app, err := ValidateApp(validationContext, appPath)
	validationCancel()
	if err != nil {
		return InstallJob{}, err
	}
	if app.Version == "" || app.Build == "" {
		return InstallJob{}, coded("app_invalid", "install jobs require an app version and build", nil)
	}
	root, err := installJobsRoot(profilePath)
	if err != nil {
		return InstallJob{}, err
	}
	if err := ensurePrivateDirectory(root); err != nil {
		return InstallJob{}, err
	}
	lockPath, err := installJobSubmissionLockPath(profilePath)
	if err != nil {
		return InstallJob{}, err
	}
	lock, err := acquireAdvisoryLock(lockPath, "job_submission_busy", "another install job is being submitted for this profile")
	if err != nil {
		return InstallJob{}, err
	}
	defer lock.Close()
	if err := cleanupInstallJobStaging(root); err != nil {
		return InstallJob{}, err
	}
	if active, found, err := activeInstallJob(profilePath); err != nil {
		return InstallJob{}, err
	} else if found {
		active, err = reconcileQueuedInstallJob(profilePath, active)
		if err != nil {
			return active, err
		}
		if !active.Terminal() {
			return active, coded("job_active", "this profile already has an active install job", nil)
		}
	}
	jobID, err := newInstallJobID()
	if err != nil {
		return InstallJob{}, err
	}
	directory, err := installJobDirectory(profilePath, jobID)
	if err != nil {
		return InstallJob{}, err
	}
	stagingDirectory := installJobStagingDirectory(root, jobID)
	if err := os.Mkdir(stagingDirectory, 0o700); err != nil {
		return InstallJob{}, coded("job_create_failed", "could not create the private install job directory", err)
	}
	published := false
	defer func() {
		if !published {
			_ = os.RemoveAll(stagingDirectory)
		}
	}()
	artifactPath := filepath.Join(stagingDirectory, "artifact.app")
	sourceDigest, err := digestInstallJobArtifact(ctx, app.Path)
	if err != nil {
		return InstallJob{}, err
	}
	if err := copyInstallJobArtifact(ctx, app.Path, artifactPath); err != nil {
		return InstallJob{}, err
	}
	copied, err := ValidateApp(ctx, artifactPath)
	if err != nil {
		return InstallJob{}, coded("job_snapshot_failed", "copied app snapshot did not pass validation", err)
	}
	if !sameInstallJobArtifact(app, copied) {
		return InstallJob{}, coded("job_snapshot_failed", "copied app snapshot identity changed", nil)
	}
	copiedDigest, err := digestInstallJobArtifact(ctx, artifactPath)
	if err != nil {
		return InstallJob{}, err
	}
	if copiedDigest != sourceDigest {
		return InstallJob{}, coded("job_snapshot_failed", "copied app snapshot contents changed", nil)
	}
	now := time.Now().UTC()
	job := InstallJob{
		SchemaVersion: InstallJobSchemaVersion,
		ID:            jobID,
		State:         InstallJobStateQueued,
		Artifact: InstallJobArtifact{
			BundleIdentifier: app.BundleIdentifier,
			Version:          app.Version,
			Build:            app.Build,
			ContentSHA256:    copiedDigest,
		},
		Baseline:          InstallJobBaseline{DaemonID: status.DaemonID, Generation: status.Generation, InstallCount: status.InstallCount},
		Progress:          InstallJobProgress{},
		SubmittedAt:       now,
		WorkerScheduledAt: &now,
		UpdatedAt:         now,
	}
	if err := writeInstallJobReceiptAt(filepath.Join(stagingDirectory, "receipt.json"), job); err != nil {
		return InstallJob{}, err
	}
	if err := os.Rename(stagingDirectory, directory); err != nil {
		return InstallJob{}, coded("job_create_failed", "could not publish the complete install job", err)
	}
	published = true
	if err := launchInstallJob(binaryPath, profilePath, jobID); err != nil {
		return markInstallJobTerminal(profilePath, job, InstallJobStateFailed, err)
	}
	return job, nil
}

func WaitInstallJob(ctx context.Context, profilePath string, jobID string, interval time.Duration, onUpdate func(InstallJob)) (InstallJob, error) {
	if interval <= 0 {
		return InstallJob{}, coded("job_invalid", "wait interval must be positive", nil)
	}
	var lastUpdated time.Time
	for {
		job, err := ReadInstallJob(profilePath, jobID)
		if err != nil {
			return InstallJob{}, err
		}
		if !job.UpdatedAt.Equal(lastUpdated) {
			lastUpdated = job.UpdatedAt
			if onUpdate != nil {
				onUpdate(job)
			}
		}
		if job.Terminal() {
			if job.State == InstallJobStateInstalled {
				return job, nil
			}
			return job, storedInstallJobError(job)
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return job, coded("job_wait_cancelled", "stopped waiting; the install job continues", ctx.Err())
		case <-timer.C:
		}
	}
}

func storedInstallJobError(job InstallJob) error {
	code := job.ErrorCode
	if code == "" {
		code = "job_failed"
	}
	return coded(code, strings.TrimPrefix(job.Error, code+": "), nil)
}

func RunInstallJob(ctx context.Context, profilePath string, jobID string, onResponse func(Response)) (InstallJob, error) {
	workerLockPath, err := installJobWorkerLockPath(profilePath, jobID)
	if err != nil {
		return InstallJob{}, err
	}
	workerLock, err := acquireAdvisoryLock(workerLockPath, "job_worker_active", "the install job worker is already running")
	if err != nil {
		return InstallJob{}, err
	}
	defer workerLock.Close()
	job, err := readInstallJobRecord(profilePath, jobID)
	if err != nil {
		return InstallJob{}, err
	}
	if job.Terminal() {
		return job, nil
	}
	if job.State == InstallJobStateRunning {
		return job, nil
	}
	if job.State != InstallJobStateQueued {
		return job, coded("job_invalid", "install job is not queued", nil)
	}
	artifactPath, err := installJobArtifactPath(profilePath, jobID)
	if err != nil {
		return markInstallJobTerminal(profilePath, job, InstallJobStateFailed, err)
	}
	installContext, cancel := context.WithTimeout(ctx, MaximumInstallHardTimeout()+30*time.Second)
	defer cancel()
	err = Call(installContext, profilePath, Request{Command: "install", AppPath: artifactPath, JobID: jobID}, func(response Response) {
		if onResponse != nil {
			onResponse(response)
		}
	})
	latest, readErr := readInstallJobRecord(profilePath, jobID)
	if readErr != nil {
		return job, readErr
	}
	if err != nil {
		switch latest.State {
		case InstallJobStateQueued:
			updated, terminalErr := markInstallJobTerminal(profilePath, latest, InstallJobStateInterrupted, err)
			if errorCode(terminalErr) == "job_state_changed" && updated.State == InstallJobStateRunning {
				return updated, nil
			}
			return updated, terminalErr
		case InstallJobStateRunning, InstallJobStateInstalled:
			return latest, nil
		case InstallJobStateFailed, InstallJobStateInterrupted:
			return latest, storedInstallJobError(latest)
		default:
			return latest, err
		}
	}
	if latest.State != InstallJobStateInstalled || latest.Result == nil || latest.Result.Event != "installed" {
		return latest, coded("job_proof_failed", "daemon returned without a durable install receipt", nil)
	}
	return latest, nil
}

func removeInstallJobLaunchAgent(profilePath string, jobID string) error {
	spec, err := installJobLaunchAgentSpecForProfile(profilePath, jobID)
	if err != nil {
		return err
	}
	if exec.Command("/bin/launchctl", "print", spec.serviceTarget).Run() == nil {
		if output, bootoutErr := exec.Command("/bin/launchctl", "bootout", spec.serviceTarget).CombinedOutput(); bootoutErr != nil {
			return coded("job_remove_failed", fmt.Sprintf("launchctl bootout failed (%d diagnostic bytes)", len(output)), bootoutErr)
		}
	}
	if err := os.Remove(spec.plistPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return coded("job_remove_failed", "could not remove the one-shot LaunchAgent", err)
	}
	return nil
}

func RemoveInstallJob(profilePath string, jobID string) error {
	root, err := installJobsRoot(profilePath)
	if err != nil {
		return err
	}
	if err := ensurePrivateDirectory(root); err != nil {
		return err
	}
	lockPath, err := installJobSubmissionLockPath(profilePath)
	if err != nil {
		return err
	}
	lock, err := acquireAdvisoryLock(lockPath, "job_submission_busy", "another install job operation owns this profile")
	if err != nil {
		return err
	}
	defer lock.Close()
	job, err := ReadInstallJob(profilePath, jobID)
	if err != nil {
		return err
	}
	if !job.Terminal() {
		return coded("job_active", "an active install job cannot be removed", nil)
	}
	if err := removeInstallJobLaunchAgent(profilePath, jobID); err != nil {
		return err
	}
	directory, err := installJobDirectory(profilePath, jobID)
	if err != nil {
		return err
	}
	if err := os.RemoveAll(directory); err != nil {
		return coded("job_remove_failed", "could not remove the install job snapshot and receipt", err)
	}
	return nil
}
