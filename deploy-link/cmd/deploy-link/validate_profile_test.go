package main

import (
	"github.com/zjz-connect/xcode-deploy-link-deploy-beyond-local-network/internal/linkcore"
	"os"
	"path/filepath"
	"testing"
)

func TestValidateProfileOffline(t *testing.T) {
	directory := t.TempDir()
	if err := os.Chmod(directory, 0700); err != nil {
		t.Fatal(err)
	}
	pair := filepath.Join(directory, "pair.plist")
	if err := os.WriteFile(pair, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	config := linkcore.DefaultConfig()
	config.DeviceLabel = "fixture phone"
	config.RemoteIdentifier = "fixture-identifier"
	config.TargetTailnetIP = "100.91.2.3"
	config.PairRecordPath = pair
	path := filepath.Join(directory, "phone.json")
	if err := linkcore.WriteConfig(path, config); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(path)
	if err := validateProfile([]string{"--profile", path}); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("validation changed the profile")
	}
	if err := os.Remove(pair); err != nil {
		t.Fatal(err)
	}
	if err := validateProfile([]string{"--profile", path}); err == nil {
		t.Fatal("missing pairing record accepted")
	}
	if err := os.WriteFile(path, []byte("{broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := validateProfile([]string{"--profile", path}); err == nil {
		t.Fatal("malformed profile accepted")
	}
}
