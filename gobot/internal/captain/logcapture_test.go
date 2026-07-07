package watchkeeper

import (
	"bytes"
	"io"
	"os"
	"testing"
)

// captureOutput redirects os.Stdout for the duration of f and returns
// everything f printed to it. The supervisor surfaces operationally
// important, grep-able signals (wake-delivery outages, credits-fetch
// fallbacks, detector errors, startup channel preflight) via fmt.Printf to
// stdout, so tests assert on those strings by capturing them here. Not safe
// for t.Parallel(); package tests run sequentially, which is sufficient.
func captureOutput(t *testing.T, f func()) string {
	t.Helper()
	orig := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	done := make(chan string, 1)
	go func() {
		var buf bytes.Buffer
		_, _ = io.Copy(&buf, r)
		done <- buf.String()
	}()

	f()
	_ = w.Close()
	out := <-done
	_ = r.Close()
	return out
}
