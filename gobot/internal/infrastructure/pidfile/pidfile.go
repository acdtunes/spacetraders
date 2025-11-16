package pidfile

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
)

// PIDFile manages a process ID file for daemon single-instance enforcement
type PIDFile struct {
	path string
}

// New creates a new PIDFile manager
func New(path string) *PIDFile {
	return &PIDFile{path: path}
}

// Acquire attempts to acquire the PID file lock
// Returns an error if another instance is already running
func (p *PIDFile) Acquire() error {
	// Check if PID file already exists
	if _, err := os.Stat(p.path); err == nil {
		// PID file exists - check if process is still running
		data, err := os.ReadFile(p.path)
		if err != nil {
			return fmt.Errorf("failed to read existing PID file: %w", err)
		}

		pidStr := strings.TrimSpace(string(data))
		pid, err := strconv.Atoi(pidStr)
		if err != nil {
			// Invalid PID file - remove it and continue
			_ = os.Remove(p.path)
		} else {
			// Check if process is still running
			if isProcessRunning(pid) {
				return fmt.Errorf("daemon is already running (PID %d)", pid)
			}
			// Process is dead - remove stale PID file
			_ = os.Remove(p.path)
		}
	}

	// Write current process ID to PID file
	pid := os.Getpid()
	pidData := fmt.Sprintf("%d\n", pid)

	if err := os.WriteFile(p.path, []byte(pidData), 0644); err != nil {
		return fmt.Errorf("failed to write PID file: %w", err)
	}

	return nil
}

// Release removes the PID file
func (p *PIDFile) Release() error {
	if err := os.Remove(p.path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove PID file: %w", err)
	}
	return nil
}

// isProcessRunning checks if a process with the given PID is running
func isProcessRunning(pid int) bool {
	// Send signal 0 to check if process exists
	// Signal 0 doesn't actually send a signal, just checks permissions
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}

	// On Unix systems, FindProcess always succeeds
	// We need to send signal 0 to actually check if the process exists
	err = process.Signal(syscall.Signal(0))
	if err == nil {
		return true
	}

	// Check for specific errors
	if err == syscall.ESRCH {
		// Process doesn't exist
		return false
	}
	if err == syscall.EPERM {
		// Process exists but we don't have permission (still running)
		return true
	}

	// Other error - assume not running
	return false
}
