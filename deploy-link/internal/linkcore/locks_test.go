package linkcore

import (
	"os"
	"path/filepath"
	"testing"
)

func TestAdvisoryLockRejectsSecondOwnerAndCanBeReacquired(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := ensurePrivateDirectory(directory); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "profile.lock")
	first, err := acquireAdvisoryLock(path, "already_owned", "already owned")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := acquireAdvisoryLock(path, "already_owned", "already owned"); errorCode(err) != "already_owned" {
		t.Fatalf("second error = %v, want already_owned", err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := acquireAdvisoryLock(path, "already_owned", "already owned")
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestForwardPortLockIsSharedBySiblingProfiles(t *testing.T) {
	first := forwardPortLockPath(61015)
	second := forwardPortLockPath(61015)
	if first != second {
		t.Fatalf("same forward port received different locks: %q and %q", first, second)
	}
	if first == forwardPortLockPath(61016) {
		t.Fatal("different forward ports received the same lock")
	}
}
