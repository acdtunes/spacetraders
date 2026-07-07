package buildinfo

import "testing"

func TestInfoShortCommitTruncatesToTwelve(t *testing.T) {
	i := Info{Commit: "abcdef0123456789abcdef"}
	if got := i.ShortCommit(); got != "abcdef012345" {
		t.Fatalf("ShortCommit() = %q, want %q", got, "abcdef012345")
	}
}

func TestInfoShortCommitLeavesShortValuesUntouched(t *testing.T) {
	// A commit shorter than the truncation length (e.g. a 7-char abbrev) must
	// survive verbatim, and the "unknown" sentinel must not be mangled.
	cases := map[string]string{
		"abc1234": "abc1234",
		"unknown": "unknown",
		"":        "",
	}
	for in, want := range cases {
		if got := (Info{Commit: in}).ShortCommit(); got != want {
			t.Errorf("ShortCommit(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestInfoBanner(t *testing.T) {
	i := Info{Version: "v1.2.3", Commit: "abcdef0123456789abcdef", BuildTime: "2026-07-07T12:00:00Z"}
	want := "Watchkeeper v1.2.3 (commit abcdef012345, built 2026-07-07T12:00:00Z)"
	if got := i.Banner("Watchkeeper"); got != want {
		t.Fatalf("Banner() = %q, want %q", got, want)
	}
}

func TestInfoShort(t *testing.T) {
	i := Info{Version: "v1.2.3", Commit: "abcdef0123456789abcdef", BuildTime: "2026-07-07T12:00:00Z"}
	want := "v1.2.3 (commit abcdef012345, built 2026-07-07T12:00:00Z)"
	if got := i.Short(); got != want {
		t.Fatalf("Short() = %q, want %q", got, want)
	}
	if i.String() != i.Short() {
		t.Fatalf("String() = %q, want it to equal Short() = %q", i.String(), i.Short())
	}
}

// A binary built without the -ldflags stamp (go run, go test, plain go build)
// must still be self-describing rather than emit empty strings, so the defaults
// are part of the contract.
func TestGetDefaultsAreNonEmptySentinels(t *testing.T) {
	got := Get()
	if got.Version != "dev" {
		t.Errorf("default Version = %q, want %q", got.Version, "dev")
	}
	if got.Commit != "unknown" {
		t.Errorf("default Commit = %q, want %q", got.Commit, "unknown")
	}
	if got.BuildTime != "unknown" {
		t.Errorf("default BuildTime = %q, want %q", got.BuildTime, "unknown")
	}
}
