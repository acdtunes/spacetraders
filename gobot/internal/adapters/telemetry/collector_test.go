package telemetry

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func nopReadCloser(s string) io.ReadCloser { return io.NopCloser(strings.NewReader(s)) }

func TestCollectAggregatesPerSessionAndSkipsMissingTranscripts(t *testing.T) {
	transcripts := map[string]string{
		"k-captain": strings.Join([]string{
			`{"type":"user","timestamp":"2026-07-07T10:00:00Z","message":{"content":"wake"}}`,
			`{"type":"assistant","timestamp":"2026-07-07T10:00:01Z","message":{"id":"m1","usage":{"input_tokens":100,"output_tokens":50,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}}`,
		}, "\n"),
		// k-shipwright has no transcript entry -> Open returns (nil, nil).
	}

	c := Collector{
		List: func(ctx context.Context) ([]Session, error) {
			return []Session{
				{Alias: "captain", SessionKey: "k-captain"},
				{Alias: "shipwright", SessionKey: "k-shipwright"},
			}, nil
		},
		Open: func(sessionKey string) (io.ReadCloser, error) {
			if body, ok := transcripts[sessionKey]; ok {
				return nopReadCloser(body), nil
			}
			return nil, nil
		},
	}

	usages, err := c.Collect(context.Background(), time.Time{})
	require.NoError(t, err)

	// Only the captain session produced usage; the transcript-less session is skipped.
	require.Len(t, usages, 1)
	require.Equal(t, "captain", usages[0].Alias)
	require.Equal(t, int64(150), usages[0].Usage.Total())
	require.Equal(t, 1, usages[0].Turns)
}

func TestCollectPropagatesListerError(t *testing.T) {
	c := Collector{
		List: func(ctx context.Context) ([]Session, error) { return nil, io.ErrUnexpectedEOF },
		Open: func(string) (io.ReadCloser, error) { return nil, nil },
	}
	_, err := c.Collect(context.Background(), time.Time{})
	require.Error(t, err)
}

func TestParseSessionListExtractsAliasKeyWorkdir(t *testing.T) {
	raw := `[
	  {"Alias":"captain","SessionKey":"c-1","WorkDir":"/city","State":"active"},
	  {"Alias":"shipwright","SessionKey":"s-2","WorkDir":"/city","State":"suspended"}
	]`
	sessions, err := parseSessionList([]byte(raw))
	require.NoError(t, err)
	require.Len(t, sessions, 2)
	require.Equal(t, "captain", sessions[0].Alias)
	require.Equal(t, "c-1", sessions[0].SessionKey)
	require.Equal(t, "/city", sessions[0].WorkDir)
}

func TestGlobTranscriptOpenerFindsBySessionKey(t *testing.T) {
	root := t.TempDir()
	projDir := filepath.Join(root, "-some-munged-project")
	require.NoError(t, os.MkdirAll(projDir, 0o755))
	key := "abc-123"
	require.NoError(t, os.WriteFile(filepath.Join(projDir, key+".jsonl"), []byte("{}"), 0o644))

	open := globTranscriptOpener(root)

	rc, err := open(key)
	require.NoError(t, err)
	require.NotNil(t, rc)
	rc.Close()

	// A key with no transcript file yields (nil, nil), not an error.
	rc2, err := open("no-such-key")
	require.NoError(t, err)
	require.Nil(t, rc2)
}
