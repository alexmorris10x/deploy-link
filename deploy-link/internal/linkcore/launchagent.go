package linkcore

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
)

const launchAgentLabelPrefix = "one.zjz.deploy-link"

type launchAgentSpec struct {
	label         string
	plistPath     string
	serviceTarget string
	logsDirectory string
	standardOut   string
	standardError string
}

func launchAgentProfileID(profilePath string) string {
	digest := sha256.Sum256([]byte(filepath.Clean(profilePath)))
	return hex.EncodeToString(digest[:16])
}

func buildLaunchAgentSpec(profilePath string, homeDirectory string, target string) launchAgentSpec {
	label := launchAgentLabelPrefix + "." + launchAgentProfileID(profilePath)
	logsDirectory := filepath.Join(filepath.Dir(profilePath), "logs")
	return launchAgentSpec{
		label:         label,
		plistPath:     filepath.Join(homeDirectory, "Library", "LaunchAgents", label+".plist"),
		serviceTarget: target + "/" + label,
		logsDirectory: logsDirectory,
		standardOut:   filepath.Join(logsDirectory, label+".log"),
		standardError: filepath.Join(logsDirectory, label+".error.log"),
	}
}

func launchAgentSpecForProfile(profilePath string) (launchAgentSpec, error) {
	if !filepath.IsAbs(profilePath) {
		return launchAgentSpec{}, coded("launch_agent_invalid", "profile path must be absolute", nil)
	}
	homeDirectory, err := os.UserHomeDir()
	if err != nil {
		return launchAgentSpec{}, coded("launch_agent_failed", "could not locate the user home directory", err)
	}
	return buildLaunchAgentSpec(profilePath, homeDirectory, launchTarget()), nil
}

func xmlEscape(value string) string {
	var buffer bytes.Buffer
	_ = xml.EscapeText(&buffer, []byte(value))
	return buffer.String()
}

func launchTarget() string {
	return "gui/" + strconv.Itoa(os.Getuid())
}

func InstallLaunchAgent(binaryPath string, profilePath string) error {
	if !filepath.IsAbs(binaryPath) || !filepath.IsAbs(profilePath) {
		return coded("launch_agent_invalid", "binary and profile paths must be absolute", nil)
	}
	spec, err := launchAgentSpecForProfile(profilePath)
	if err != nil {
		return err
	}
	if err := prepareLaunchAgentLogs(spec); err != nil {
		return err
	}
	contents := launchAgentContents(spec, binaryPath, profilePath)
	path := spec.plistPath
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return coded("launch_agent_failed", "could not create LaunchAgents directory", err)
	}
	if err := writeAtomic(path, []byte(contents), 0o644); err != nil {
		return coded("launch_agent_failed", "could not write LaunchAgent", err)
	}
	if exec.Command("/bin/launchctl", "print", spec.serviceTarget).Run() == nil {
		return nil
	}
	if output, err := exec.Command("/bin/launchctl", "bootstrap", launchTarget(), path).CombinedOutput(); err != nil {
		return coded("launch_agent_failed", fmt.Sprintf("launchctl bootstrap failed (%d diagnostic bytes)", len(output)), err)
	}
	if output, err := exec.Command("/bin/launchctl", "kickstart", spec.serviceTarget).CombinedOutput(); err != nil {
		return coded("launch_agent_failed", fmt.Sprintf("launchctl kickstart failed (%d diagnostic bytes)", len(output)), err)
	}
	return nil
}

func prepareLaunchAgentLogs(spec launchAgentSpec) error {
	if err := ensurePrivateDirectory(spec.logsDirectory); err != nil {
		return coded("launch_agent_failed", "could not prepare private log directory", err)
	}
	for _, path := range []string{spec.standardOut, spec.standardError} {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return coded("launch_agent_failed", "could not prepare a private log file", err)
		}
		chmodErr := file.Chmod(0o600)
		closeErr := file.Close()
		if chmodErr != nil {
			return coded("launch_agent_failed", "could not protect a log file", chmodErr)
		}
		if closeErr != nil {
			return coded("launch_agent_failed", "could not close a log file", closeErr)
		}
	}
	return nil
}

func launchAgentContents(spec launchAgentSpec, binaryPath string, profilePath string) string {
	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>%s</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>supervise</string>
    <string>--profile</string>
    <string>%s</string>
  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
  <key>ThrottleInterval</key>
  <integer>10</integer>
  <key>ProcessType</key>
  <string>Standard</string>
  <key>Umask</key>
  <integer>63</integer>
  <key>EnvironmentVariables</key>
  <dict>
    <key>GO_IOS_TUNNEL_STATS</key>
    <string></string>
  </dict>
  <key>StandardOutPath</key>
  <string>%s</string>
  <key>StandardErrorPath</key>
  <string>%s</string>
</dict>
</plist>
`, spec.label, xmlEscape(binaryPath), xmlEscape(profilePath),
		xmlEscape(spec.standardOut), xmlEscape(spec.standardError))
}

func RemoveLaunchAgent(profilePath string) error {
	spec, err := launchAgentSpecForProfile(profilePath)
	if err != nil {
		return err
	}
	if exec.Command("/bin/launchctl", "print", spec.serviceTarget).Run() == nil {
		if output, bootoutErr := exec.Command("/bin/launchctl", "bootout", spec.serviceTarget).CombinedOutput(); bootoutErr != nil {
			return coded("launch_agent_failed", fmt.Sprintf("launchctl bootout failed (%d diagnostic bytes)", len(output)), bootoutErr)
		}
	}
	err = os.Remove(spec.plistPath)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return coded("launch_agent_failed", "could not remove LaunchAgent plist", err)
	}
	return nil
}

func writeAtomic(path string, data []byte, mode os.FileMode) error {
	parent := filepath.Dir(path)
	temporary, err := os.CreateTemp(parent, ".launch-agent-*.plist")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(mode); err != nil {
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		return err
	}
	if err := temporary.Sync(); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	committed = true
	return nil
}
