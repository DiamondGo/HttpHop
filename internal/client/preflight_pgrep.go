package client

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// RunningClientPIDs returns PIDs of httphop-client processes (for preflight scripts).
func RunningClientPIDs() ([]int, error) {
	return listClientPIDs()
}

func listClientPIDs() ([]int, error) {
	out, err := exec.Command("pgrep", "-f", "httphop-client").Output()
	if err != nil {
		if exit, ok := err.(*exec.ExitError); ok && exit.ExitCode() == 1 {
			return nil, nil
		}
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	var pids []int
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		pid, err := strconv.Atoi(line)
		if err != nil {
			continue
		}
		pids = append(pids, pid)
	}
	return pids, nil
}

// LockFilePath returns the default single-instance lock file path.
func LockFilePath() string {
	return lockFilePath()
}

// LockHolderPID reads the PID recorded in the lock file, if any.
func LockHolderPID() (int, bool) {
	b, err := os.ReadFile(lockFilePath())
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(stringsTrim(b))
	if err != nil || pid <= 0 {
		return 0, false
	}
	return pid, true
}

func lockFileDir() string {
	return filepath.Dir(lockFilePath())
}
