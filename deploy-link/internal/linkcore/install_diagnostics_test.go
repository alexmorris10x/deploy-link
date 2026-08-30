package linkcore

import "testing"

func TestAllowedDeviceInstallStatus(t *testing.T) {
	allowed := []string{
		"CreatingStagingDirectory",
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
		"DataComplete",
	}
	for _, status := range allowed {
		if got := allowedDeviceInstallStatus(status); got != status {
			t.Fatalf("allowedDeviceInstallStatus(%q) = %q, want %q", status, got, status)
		}
	}

	if got := allowedDeviceInstallStatus(""); got != "unspecified" {
		t.Fatalf("empty status = %q, want unspecified", got)
	}
	if got := allowedDeviceInstallStatus("UnfilteredDeviceText"); got != "other" {
		t.Fatalf("unknown status = %q, want other", got)
	}
}
