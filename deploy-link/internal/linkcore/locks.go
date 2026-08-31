package linkcore

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

type advisoryLock struct {
	file *os.File
}

func acquireAdvisoryLock(path string, conflictCode string, conflictMessage string) (*advisoryLock, error) {
	if err := ensurePrivateDirectory(filepath.Dir(path)); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, coded("lock_failed", "could not open a Deploy Link lock", err)
	}
	if err := file.Chmod(0o600); err != nil {
		_ = file.Close()
		return nil, coded("lock_failed", "could not protect a Deploy Link lock", err)
	}
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, coded(conflictCode, conflictMessage, nil)
		}
		return nil, coded("lock_failed", "could not acquire a Deploy Link lock", err)
	}
	return &advisoryLock{file: file}, nil
}

func (l *advisoryLock) Close() error {
	if l == nil || l.file == nil {
		return nil
	}
	_ = unix.Flock(int(l.file.Fd()), unix.LOCK_UN)
	return l.file.Close()
}

func daemonLockPath(socketPath string) string {
	return socketPath + ".lock"
}

func forwardPortLockPath(port int) string {
	root := filepath.Join(os.TempDir(), fmt.Sprintf("deploy-link-%d-locks", os.Getuid()))
	return filepath.Join(root, fmt.Sprintf("forward-%d.lock", port))
}
