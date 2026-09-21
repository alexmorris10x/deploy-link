package main

import (
	"crypto/ed25519"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"howett.net/plist"
)

func TestNormalizePairRecordAddsCompatibleHostIdentity(t *testing.T) {
	seed := make([]byte, ed25519.SeedSize)
	for index := range seed {
		seed[index] = byte(index + 1)
	}
	public := ed25519.NewKeyFromSeed(seed)[ed25519.SeedSize:]
	input, err := plist.Marshal(map[string]any{
		"private_key":            seed,
		"public_key":             public,
		"remote_unlock_host_key": "unlock",
	}, plist.XMLFormat)
	if err != nil {
		t.Fatal(err)
	}
	output, err := normalizePairRecord(input)
	if err != nil {
		t.Fatal(err)
	}
	var record repairPairRecord
	if _, err := plist.Unmarshal(output, &record); err != nil {
		t.Fatal(err)
	}
	if record.HostIdentifier == "" || record.HostIdentifier != strings.ToUpper(record.HostIdentifier) {
		t.Fatalf("host identifier = %q", record.HostIdentifier)
	}
	if len(record.HostAltIRK) != 16 {
		t.Fatalf("host alt IRK length = %d", len(record.HostAltIRK))
	}
	if !equalBytes(record.PrivateKey, seed) || !equalBytes(record.PublicKey, public) {
		t.Fatal("pairing keys changed during normalization")
	}
}

func TestNormalizePairRecordRejectsMismatchedKeys(t *testing.T) {
	input, err := plist.Marshal(map[string]any{
		"private_key": make([]byte, ed25519.SeedSize),
		"public_key":  make([]byte, ed25519.PublicKeySize),
	}, plist.XMLFormat)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := normalizePairRecord(input); err == nil {
		t.Fatal("normalizePairRecord accepted mismatched keys")
	}
}

func TestIsolatedPairingEnvironmentReplacesSensitiveRoots(t *testing.T) {
	environment := isolatedPairingEnvironment(
		[]string{"PATH=/bin", "HOME=/Users/example", "XDG_DATA_HOME=/tmp/old", "SUDO_USER=root"},
		"/private/home",
		"/private/data",
	)
	joined := strings.Join(environment, "\n")
	for _, expected := range []string{"PATH=/bin", "HOME=/private/home", "XDG_DATA_HOME=/private/data", "SUDO_USER="} {
		if !strings.Contains(joined, expected) {
			t.Fatalf("environment missing %q: %v", expected, environment)
		}
	}
	if strings.Contains(joined, "/Users/example") || strings.Contains(joined, "/tmp/old") || strings.Contains(joined, "SUDO_USER=root") {
		t.Fatalf("environment retained an unsafe root: %v", environment)
	}
}

func TestWritePrivateFileIsOwnerOnlyAndAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "record.plist")
	if err := writePrivateFile(path, []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := writePrivateFile(path, []byte("second")); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("permissions = %o", info.Mode().Perm())
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "second" {
		t.Fatalf("contents = %q", data)
	}
}
