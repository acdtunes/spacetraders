package pidfile

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	// killGracePeriod bounds how long KillExisting waits for a SIGTERM'd daemon to
	// drain before escalating to SIGKILL. Sized to allow an orderly container
	// shutdown; a redeploy must never leave the old writer alive (sp-wrh84).
	killGracePeriod = 12 * time.Second
	// killPollInterval is how often KillExisting re-checks whether the signalled
	// process has actually exited.
	killPollInterval = 200 * time.Millisecond
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

// KillExisting stops the daemon referenced in the PID file and removes the file.
// It sends SIGTERM, waits up to killGracePeriod for the process to exit, and
// escalates to SIGKILL if it does not. The PID file is removed only AFTER the
// process is confirmed gone; if the process cannot be killed, an error is
// returned so the caller fails closed rather than start a second writer beside a
// still-draining old daemon (sp-wrh84 root cause A).
func (p *PIDFile) KillExisting() error {
	return p.killExisting(killGracePeriod, killPollInterval)
}

// killExisting is KillExisting with an injectable grace window and poll interval,
// so tests can shrink the timings.
func (p *PIDFile) killExisting(gracePeriod, pollInterval time.Duration) error {
	data, err := os.ReadFile(p.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // No PID file to kill
		}
		return fmt.Errorf("failed to read PID file: %w", err)
	}

	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		// Invalid PID - just remove the file
		_ = os.Remove(p.path)
		return nil
	}

	process, err := os.FindProcess(pid)
	if err != nil {
		// Process doesn't exist - remove stale PID file
		_ = os.Remove(p.path)
		return nil
	}

	// Graceful stop: SIGTERM, then wait out the grace window for a clean drain.
	if err := process.Signal(syscall.SIGTERM); err != nil {
		if isAlreadyGone(err) {
			return p.removeStaleFile() // already gone
		}
		return fmt.Errorf("failed to send SIGTERM to process %d: %w", pid, err)
	}
	if waitForExit(pid, gracePeriod, pollInterval) {
		return p.removeStaleFile()
	}

	// Escalate: a daemon that outlives SIGTERM would keep writing game state and
	// holding the metrics port. Force it down so the new instance can bind.
	if err := process.Signal(syscall.SIGKILL); err != nil {
		if isAlreadyGone(err) {
			return p.removeStaleFile()
		}
		return fmt.Errorf("failed to send SIGKILL to process %d: %w", pid, err)
	}
	if waitForExit(pid, gracePeriod, pollInterval) {
		return p.removeStaleFile()
	}

	return fmt.Errorf("process %d survived SIGKILL and a %s grace period; refusing to start a second daemon", pid, gracePeriod)
}

// isAlreadyGone reports whether a signal error means the target process no
// longer exists. A reaped process reports ESRCH on some platforms and
// os.ErrProcessDone on others (notably darwin), so both count as "already gone".
func isAlreadyGone(err error) bool {
	return errors.Is(err, syscall.ESRCH) || errors.Is(err, os.ErrProcessDone)
}

// removeStaleFile removes the PID file once its process is confirmed gone.
func (p *PIDFile) removeStaleFile() error {
	if err := os.Remove(p.path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to remove PID file after killing process: %w", err)
	}
	return nil
}

// waitForExit polls until the process is gone or the timeout elapses, returning
// true if the process exited within the window.
func waitForExit(pid int, timeout, pollInterval time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for {
		if !isProcessRunning(pid) {
			return true
		}
		if !time.Now().Before(deadline) {
			return false
		}
		time.Sleep(pollInterval)
	}
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
