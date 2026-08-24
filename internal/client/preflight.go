package client

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// AcquireProcessLock ensures only one httphop-client holds the lock file.
// allowDuplicate skips the lock (manual testing). Returns a release function.
func AcquireProcessLock(allowDuplicate bool) (func(), error) {
	if allowDuplicate {
		return func() {}, nil
	}
	path := lockFilePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("httphop-client: lock dir: %w", err)
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("httphop-client: lock file: %w", err)
	}

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if err == syscall.EWOULDBLOCK {
			holder, _ := os.ReadFile(path)
			return nil, fmt.Errorf(
				"another httphop-client is already running (lock %s held by PID %s); "+
					"stop it before starting a second copy",
				path, stringsTrim(holder),
			)
		}
		return nil, fmt.Errorf("httphop-client: flock: %w", err)
	}

	_ = f.Truncate(0)
	_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())

	release := func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
		_ = os.Remove(path)
	}
	return release, nil
}

func lockFilePath() string {
	if p := os.Getenv("HTTPHOP_CLIENT_LOCK"); p != "" {
		return p
	}
	return filepath.Join(os.TempDir(), "httphop-client.lock")
}

func stringsTrim(b []byte) string {
	for i, c := range b {
		if c == '\n' || c == ' ' {
			return string(b[:i])
		}
	}
	return string(b)
}
