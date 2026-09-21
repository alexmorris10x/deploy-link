package main

import (
	"context"
	"crypto/ed25519"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/zjz-connect/xcode-deploy-link-deploy-beyond-local-network/internal/linkcore"
	"howett.net/plist"
)

const pairingHelperVersion = "11.15.5"

func pairingError(code, message string) error {
	return &linkcore.CodedError{Code: code, Message: message}
}

type repairPairRecord struct {
	HostIdentifier      string `plist:"host_identifier"`
	HostAltIRK          []byte `plist:"host_alt_irk"`
	PrivateKey          []byte `plist:"private_key"`
	PublicKey           []byte `plist:"public_key"`
	RemoteUnlockHostKey any    `plist:"remote_unlock_host_key"`
}

func pair(arguments []string) error {
	if len(arguments) == 0 || arguments[0] != "repair" {
		return errors.New("pair requires repair")
	}
	flags := flag.NewFlagSet("pair repair", flag.ContinueOnError)
	profile := flags.String("profile", "", "absolute profile path")
	if err := flags.Parse(arguments[1:]); err != nil {
		return err
	}
	if err := required(*profile, "--profile"); err != nil {
		return err
	}
	return repairPairing(*profile)
}

func pairingRuntimeRoot() (string, error) {
	binary, err := os.Executable()
	if err != nil {
		return "", err
	}
	binary, err = filepath.EvalSymlinks(binary)
	if err != nil {
		return "", err
	}
	return filepath.Dir(filepath.Dir(binary)), nil
}

func repairPairing(profilePath string) error {
	config, err := linkcore.LoadConfig(profilePath)
	if err != nil {
		return err
	}
	if statusIsActive(profilePath) {
		return pairingError("pair_repair_not_needed", fmt.Sprintf("%s is already connected", config.DeviceLabel))
	}
	runtimeRoot, err := pairingRuntimeRoot()
	if err != nil {
		return pairingError("pair_helper_missing", "could not locate Deploy Link runtime")
	}
	helper := filepath.Join(runtimeRoot, "tools", "pymobiledevice3-"+pairingHelperVersion, "bin", "pymobiledevice3")
	if info, statErr := os.Stat(helper); statErr != nil || !info.Mode().IsRegular() || info.Mode()&0o111 == 0 {
		return pairingError("pair_helper_missing", "reinstall Deploy Link to add the USB pairing helper")
	}
	stagingRoot, err := os.MkdirTemp(filepath.Join(runtimeRoot, "build"), ".pair-repair-")
	if err != nil {
		return pairingError("pair_repair_failed", "could not create private pairing workspace")
	}
	defer os.RemoveAll(stagingRoot)
	if err := os.Chmod(stagingRoot, 0o700); err != nil {
		return pairingError("pair_repair_failed", "could not protect pairing workspace")
	}
	home := filepath.Join(stagingRoot, "home")
	dataHome := filepath.Join(stagingRoot, "data")
	if err := os.MkdirAll(home, 0o700); err != nil {
		return pairingError("pair_repair_failed", "could not prepare pairing workspace")
	}
	cmd := exec.Command(helper, "lockdown", "remotepairing", "--udid", config.RemoteIdentifier, "--pair")
	cmd.Env = isolatedPairingEnvironment(os.Environ(), home, dataHome)
	if err := cmd.Run(); err != nil {
		return pairingError("pair_repair_failed", fmt.Sprintf("connect %s by USB, unlock it, and confirm it is trusted", config.DeviceLabel))
	}
	source := filepath.Join(dataHome, "pymobiledevice3", "remote_"+config.RemoteIdentifier+".plist")
	recordData, err := os.ReadFile(source)
	if err != nil {
		return pairingError("pair_repair_failed", "the USB helper did not create a pairing record")
	}
	recordData, err = normalizePairRecord(recordData)
	if err != nil {
		return err
	}
	backupPath, hadOriginal, err := replacePairRecord(config.PairRecordPath, recordData)
	if err != nil {
		return err
	}
	rollback := func() {
		if hadOriginal {
			_ = restorePairRecord(config.PairRecordPath, backupPath)
		} else {
			_ = os.Remove(config.PairRecordPath)
		}
	}
	binary, err := os.Executable()
	if err != nil {
		rollback()
		return err
	}
	binary, err = filepath.EvalSymlinks(binary)
	if err != nil {
		rollback()
		return err
	}
	if err := linkcore.RemoveLaunchAgent(profilePath); err != nil {
		rollback()
		return err
	}
	if err := linkcore.InstallLaunchAgent(binary, profilePath); err != nil {
		rollback()
		_ = linkcore.InstallLaunchAgent(binary, profilePath)
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if err := waitForActive(ctx, profilePath); err != nil {
		_ = linkcore.RemoveLaunchAgent(profilePath)
		rollback()
		_ = linkcore.InstallLaunchAgent(binary, profilePath)
		return pairingError("pair_repair_failed", "the repaired bridge did not become active; the previous record was restored")
	}
	_ = os.Remove(backupPath)
	emit(map[string]any{"ok": true, "event": "pair_repaired", "device_label": config.DeviceLabel})
	return nil
}

func isolatedPairingEnvironment(environment []string, home, dataHome string) []string {
	filtered := make([]string, 0, len(environment)+3)
	for _, value := range environment {
		if strings.HasPrefix(value, "HOME=") || strings.HasPrefix(value, "XDG_DATA_HOME=") || strings.HasPrefix(value, "SUDO_USER=") {
			continue
		}
		filtered = append(filtered, value)
	}
	return append(filtered, "HOME="+home, "XDG_DATA_HOME="+dataHome, "SUDO_USER=")
}

func normalizePairRecord(data []byte) ([]byte, error) {
	var record repairPairRecord
	if _, err := plist.Unmarshal(data, &record); err != nil {
		return nil, pairingError("pair_repair_failed", "the USB helper created an unreadable pairing record")
	}
	if len(record.PrivateKey) != ed25519.SeedSize || len(record.PublicKey) != ed25519.PublicKeySize {
		return nil, pairingError("pair_repair_failed", "the USB helper created an incomplete pairing record")
	}
	derived := ed25519.NewKeyFromSeed(record.PrivateKey)[ed25519.SeedSize:]
	if !equalBytes(derived, record.PublicKey) {
		return nil, pairingError("pair_repair_failed", "the USB pairing keys do not match")
	}
	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		return nil, pairingError("pair_repair_failed", "could not determine the Mac pairing identity")
	}
	record.HostIdentifier = strings.ToUpper(uuid.NewMD5(uuid.NameSpaceDNS, []byte(hostname)).String())
	record.HostAltIRK = make([]byte, 16)
	encoded, err := plist.MarshalIndent(record, plist.XMLFormat, "  ")
	if err != nil {
		return nil, pairingError("pair_repair_failed", "could not encode the pairing record")
	}
	return encoded, nil
}

func equalBytes(left, right []byte) bool {
	if len(left) != len(right) {
		return false
	}
	var difference byte
	for index := range left {
		difference |= left[index] ^ right[index]
	}
	return difference == 0
}

func replacePairRecord(path string, data []byte) (string, bool, error) {
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, 0o700); err != nil {
		return "", false, pairingError("pair_repair_failed", "could not prepare the pairing directory")
	}
	backup := path + ".backup"
	hadOriginal := false
	if current, err := os.ReadFile(path); err == nil {
		hadOriginal = true
		if err := writePrivateFile(backup, current); err != nil {
			return "", false, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", false, pairingError("pair_repair_failed", "could not preserve the previous pairing record")
	}
	if err := writePrivateFile(path, data); err != nil {
		return "", false, err
	}
	return backup, hadOriginal, nil
}

func restorePairRecord(path, backup string) error {
	data, err := os.ReadFile(backup)
	if err != nil {
		return err
	}
	return writePrivateFile(path, data)
}

func writePrivateFile(path string, data []byte) error {
	parent := filepath.Dir(path)
	temporary, err := os.CreateTemp(parent, ".pair-record-")
	if err != nil {
		return pairingError("pair_repair_failed", "could not create the pairing record")
	}
	temporaryPath := temporary.Name()
	committed := false
	defer func() {
		_ = temporary.Close()
		if !committed {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o600); err != nil {
		return pairingError("pair_repair_failed", "could not protect the pairing record")
	}
	if _, err := temporary.Write(data); err != nil {
		return pairingError("pair_repair_failed", "could not write the pairing record")
	}
	if err := temporary.Sync(); err != nil {
		return pairingError("pair_repair_failed", "could not sync the pairing record")
	}
	if err := temporary.Close(); err != nil {
		return pairingError("pair_repair_failed", "could not close the pairing record")
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return pairingError("pair_repair_failed", "could not replace the pairing record")
	}
	committed = true
	return nil
}

func statusIsActive(profilePath string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	active := false
	_ = linkcore.Call(ctx, profilePath, linkcore.Request{Command: "status"}, func(response linkcore.Response) {
		if response.Final && response.OK && response.State == "active" {
			active = true
		}
	})
	return active
}

func waitForActive(ctx context.Context, profilePath string) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if statusIsActive(profilePath) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
