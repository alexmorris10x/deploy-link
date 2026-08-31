package linkcore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLaunchAgentSpecIsProfileScoped(t *testing.T) {
	homeDirectory := filepath.Join(string(filepath.Separator), "Users", "developer")
	profilesDirectory := filepath.Join(homeDirectory, ".local", "share", "deploy-link", "profiles")
	iphoneProfile := filepath.Join(profilesDirectory, "alex-iphone.json")
	ipadProfile := filepath.Join(profilesDirectory, "alex-ipad.json")

	iphone := buildLaunchAgentSpec(iphoneProfile, homeDirectory, "gui/501")
	ipad := buildLaunchAgentSpec(ipadProfile, homeDirectory, "gui/501")

	if iphone.label == ipad.label {
		t.Fatal("different profiles received the same LaunchAgent label")
	}
	if iphone.plistPath == ipad.plistPath {
		t.Fatal("different profiles received the same LaunchAgent plist path")
	}
	if iphone.serviceTarget == ipad.serviceTarget {
		t.Fatal("different profiles received the same launchd service target")
	}
	if iphone.standardOut == ipad.standardOut || iphone.standardError == ipad.standardError {
		t.Fatal("different profiles received colliding log paths")
	}
	if strings.Contains(iphone.label, "iphone") || strings.Contains(ipad.label, "ipad") {
		t.Fatal("LaunchAgent label exposes the profile name")
	}
	if got, want := iphone.plistPath, filepath.Join(homeDirectory, "Library", "LaunchAgents", iphone.label+".plist"); got != want {
		t.Fatalf("plist path = %q, want %q", got, want)
	}
	if got, want := iphone.serviceTarget, "gui/501/"+iphone.label; got != want {
		t.Fatalf("service target = %q, want %q", got, want)
	}
	if filepath.Dir(iphone.standardOut) != iphone.logsDirectory || filepath.Dir(iphone.standardError) != iphone.logsDirectory {
		t.Fatal("profile logs are outside the profile log directory")
	}
}

func TestLaunchAgentProfileIdentityIsStableForCleanPath(t *testing.T) {
	homeDirectory := filepath.Join(string(filepath.Separator), "Users", "developer")
	profile := filepath.Join(homeDirectory, "profiles", "iphone.json")
	uncleanProfile := filepath.Join(homeDirectory, "profiles", "nested", "..", "iphone.json")

	clean := buildLaunchAgentSpec(profile, homeDirectory, "gui/501")
	unclean := buildLaunchAgentSpec(uncleanProfile, homeDirectory, "gui/501")

	if clean.label != unclean.label {
		t.Fatalf("equivalent profile paths received different labels: %q and %q", clean.label, unclean.label)
	}
	if clean.plistPath != unclean.plistPath {
		t.Fatalf("equivalent profile paths received different plist paths: %q and %q", clean.plistPath, unclean.plistPath)
	}
}

func TestLaunchAgentContentsUsesScopedValuesAndEscapesPaths(t *testing.T) {
	homeDirectory := filepath.Join(string(filepath.Separator), "Users", "developer")
	profile := filepath.Join(homeDirectory, "Deploy & Link", "profiles", "iphone.json")
	binary := filepath.Join(homeDirectory, "Deploy & Link", "bin", "deploy-link")
	spec := buildLaunchAgentSpec(profile, homeDirectory, "gui/501")
	contents := launchAgentContents(spec, binary, profile)

	if !strings.Contains(contents, spec.label) {
		t.Fatalf("LaunchAgent plist does not contain label %q", spec.label)
	}
	if !strings.Contains(contents, "<string>supervise</string>") || strings.Contains(contents, "<string>serve</string>") {
		t.Fatal("LaunchAgent does not use the non-disruptive supervisor")
	}
	if !strings.Contains(contents, "<key>Umask</key>") || !strings.Contains(contents, "<integer>63</integer>") {
		t.Fatal("LaunchAgent does not enforce owner-only process output")
	}
	if !strings.Contains(contents, "<key>ProcessType</key>") || !strings.Contains(contents, "<string>Standard</string>") {
		t.Fatal("LaunchAgent does not use the standard resource policy")
	}
	if !strings.Contains(contents, "<key>GO_IOS_TUNNEL_STATS</key>") || !strings.Contains(contents, "<string></string>") {
		t.Fatal("LaunchAgent does not disable continuous tunnel telemetry")
	}
	for _, value := range []string{spec.standardOut, spec.standardError} {
		if !strings.Contains(contents, xmlEscape(value)) {
			t.Fatalf("LaunchAgent plist does not contain escaped path %q", value)
		}
	}
	if strings.Contains(contents, "Deploy & Link") {
		t.Fatal("LaunchAgent plist contains an unescaped path")
	}
	for _, value := range []string{
		strings.ReplaceAll(binary, "&", "&amp;"),
		strings.ReplaceAll(profile, "&", "&amp;"),
	} {
		if !strings.Contains(contents, value) {
			t.Fatalf("LaunchAgent plist does not contain escaped path %q", value)
		}
	}
}

func TestLaunchAgentSpecRejectsRelativeProfilePath(t *testing.T) {
	_, err := launchAgentSpecForProfile("profiles/iphone.json")
	if errorCode(err) != "launch_agent_invalid" {
		t.Fatalf("error code = %q, want launch_agent_invalid", errorCode(err))
	}
}

func TestPrepareLaunchAgentLogsCreatesOwnerOnlyFiles(t *testing.T) {
	homeDirectory := t.TempDir()
	if err := os.Chmod(homeDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(homeDirectory, "profiles", "iphone.json")
	spec := buildLaunchAgentSpec(profile, homeDirectory, "gui/501")
	if err := prepareLaunchAgentLogs(spec); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{spec.logsDirectory, spec.standardOut, spec.standardError} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		want := os.FileMode(0o600)
		if info.IsDir() {
			want = 0o700
		}
		if info.Mode().Perm() != want {
			t.Fatalf("mode for %s = %#o, want %#o", filepath.Base(path), info.Mode().Perm(), want)
		}
	}
}
