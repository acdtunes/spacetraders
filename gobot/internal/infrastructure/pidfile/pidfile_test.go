package pidfile

import (
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"testing"
	"time"
)

// TestHelperProcess is the subprocess entry point re-executed by spawnHelper. It
// is inert during a normal test run (no PIDFILE_HELPER_MODE), and only when
// re-executed does it install a SIGTERM disposition and idle. Using a real Go
// child with signal.Ignore/Notify makes SIGTERM handling deterministic, unlike a
// shell whose trap semantics vary by platform.
func TestHelperProcess(t *testing.T) {
	mode := os.Getenv("PIDFILE_HELPER_MODE")
	if mode == "" {
		return
	}
	markerPath := os.Getenv("PIDFILE_HELPER_MARKER")

	switch mode {
	case "ignore-term":
		// Truly ignore SIGTERM at the runtime level: only SIGKILL can end us.
		signal.Ignore(syscall.SIGTERM)
	case "handle-term":
		ch := make(chan os.Signal, 1)
		signal.Notify(ch, syscall.SIGTERM)
		go func() {
			<-ch
			if markerPath != "" {
				_ = os.WriteFile(markerPath, []byte("term"), 0644)
			}
			os.Exit(0)
		}()
	}

	// Announce readiness ONLY after the disposition is installed, so the parent
	// never races a SIGTERM in before we are set up.
	if readyPath := os.Getenv("PIDFILE_HELPER_READY"); readyPath != "" {
		_ = os.WriteFile(readyPath, []byte("ready"), 0644)
	}
	time.Sleep(30 * time.Second)
	os.Exit(0)
}

// spawnHelper re-executes the test binary in TestHelperProcess with the given
// mode, blocks until the child signals readiness, and returns its PID plus a
// cleanup func. A goroutine reaps the child so isProcessRunning observes its true
// exit: an un-reaped child lingers as a zombie that still answers signal 0.
func spawnHelper(t *testing.T, mode, markerPath string) (int, func()) {
	t.Helper()
	readyPath := filepath.Join(t.TempDir(), "ready")

	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperProcess$")
	cmd.Env = append(os.Environ(),
		"PIDFILE_HELPER_MODE="+mode,
		"PIDFILE_HELPER_READY="+readyPath,
		"PIDFILE_HELPER_MARKER="+markerPath,
	)
	if err := cmd.Start(); err != nil {
		t.Fatalf("failed to start helper process: %v", err)
	}
	pid := cmd.Process.Pid

	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()

	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(readyPath); err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = cmd.Process.Kill()
			t.Fatal("helper process never signalled readiness")
		}
		time.Sleep(5 * time.Millisecond)
	}

	var once sync.Once
	return pid, func() {
		once.Do(func() {
			_ = cmd.Process.Kill()
			<-done
		})
	}
}

func writePIDFile(t *testing.T, path string, pid int) {
	t.Helper()
	if err := os.WriteFile(path, []byte(strconv.Itoa(pid)+"\n"), 0644); err != nil {
		t.Fatalf("failed to write PID file: %v", err)
	}
}

func waitUntilDead(t *testing.T, pid int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for isProcessRunning(pid) {
		if time.Now().After(deadline) {
			t.Fatalf("process %d never exited", pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Behavior 1: a daemon that ignores SIGTERM must be escalated to SIGKILL, and
// KillExisting must not report success until the process is actually gone.
func TestKillExistingEscalatesToSIGKILLWhenSIGTERMIgnored(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.pid")

	pid, cleanup := spawnHelper(t, "ignore-term", "")
	defer cleanup()
	writePIDFile(t, path, pid)

	grace := 300 * time.Millisecond
	start := time.Now()
	err := New(path).killExisting(grace, 10*time.Millisecond)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("killExisting returned error: %v", err)
	}
	// The child ignored SIGTERM, so its death proves SIGKILL was sent.
	if isProcessRunning(pid) {
		t.Fatalf("process %d still running after killExisting — SIGKILL escalation did not happen", pid)
	}
	// It can only have died after waiting out the SIGTERM grace window.
	if elapsed < grace {
		t.Fatalf("killExisting returned in %s, before the %s grace window — it did not wait for SIGTERM to drain before escalating", elapsed, grace)
	}
	// The PID file must be removed only after confirmed exit.
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("PID file still present after kill (stat err = %v)", statErr)
	}
}

// Behavior 2: a daemon that exits on SIGTERM must NOT be escalated to SIGKILL.
func TestKillExistingStopsGracefullyWithoutSIGKILL(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "daemon.pid")
	marker := filepath.Join(dir, "term-handled")

	pid, cleanup := spawnHelper(t, "handle-term", marker)
	defer cleanup()
	writePIDFile(t, path, pid)

	// A generous grace window: a fast return proves SIGKILL (only sent after the
	// window) was never reached.
	grace := 2 * time.Second
	start := time.Now()
	err := New(path).killExisting(grace, 10*time.Millisecond)
	elapsed := time.Since(start)

	if err != nil {
		t.Fatalf("killExisting returned error: %v", err)
	}
	if isProcessRunning(pid) {
		t.Fatalf("process %d still running after graceful stop", pid)
	}
	if elapsed >= grace {
		t.Fatalf("killExisting took %s (>= grace %s) — it escalated to SIGKILL instead of stopping on SIGTERM", elapsed, grace)
	}
	if _, statErr := os.Stat(marker); statErr != nil {
		t.Fatalf("SIGTERM handler marker missing — the graceful path did not run: %v", statErr)
	}
}

// Behavior 3: a missing PID file is a no-op success.
func TestKillExistingNoopWhenPIDFileMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nonexistent.pid")
	if err := New(path).killExisting(500*time.Millisecond, 10*time.Millisecond); err != nil {
		t.Fatalf("expected nil for a missing PID file, got %v", err)
	}
}

// Behavior 3: an unparseable PID file is removed and returns success.
func TestKillExistingRemovesInvalidPIDFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.pid")
	if err := os.WriteFile(path, []byte("not-a-pid\n"), 0644); err != nil {
		t.Fatalf("failed to write PID file: %v", err)
	}
	if err := New(path).killExisting(500*time.Millisecond, 10*time.Millisecond); err != nil {
		t.Fatalf("expected nil for an invalid PID file, got %v", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("invalid PID file was not removed")
	}
}

// Behavior 3: a PID file that points at a dead process is stale — removed, success.
func TestKillExistingRemovesStalePIDFileForDeadProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "daemon.pid")

	pid, cleanup := spawnHelper(t, "handle-term", "")
	cleanup() // SIGKILL + reap so the PID is dead before we act
	waitUntilDead(t, pid)

	writePIDFile(t, path, pid)
	if err := New(path).killExisting(500*time.Millisecond, 10*time.Millisecond); err != nil {
		t.Fatalf("expected nil for a stale PID file, got %v", err)
	}
	if _, statErr := os.Stat(path); !os.IsNotExist(statErr) {
		t.Fatalf("stale PID file was not removed")
	}
}
