package cli

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/andrescamacho/spacetraders-go/internal/domain/captain"
)

type fakeEventStore struct {
	unprocessed []*captain.Event
	marked      []int64
}

func (f *fakeEventStore) FindUnprocessed(ctx context.Context, playerID, limit int) ([]*captain.Event, error) {
	return f.unprocessed, nil
}

func (f *fakeEventStore) MarkProcessed(ctx context.Context, ids []int64, at time.Time) error {
	f.marked = append(f.marked, ids...)
	return nil
}

func TestCaptainEventsAckMarksParsedIDs(t *testing.T) {
	fs := &fakeEventStore{}
	err := runEventsAck(context.Background(), fs, "12,13,14")
	require.NoError(t, err)
	require.Equal(t, []int64{12, 13, 14}, fs.marked)
}

func TestCaptainEventsAckRejectsGarbage(t *testing.T) {
	fs := &fakeEventStore{}
	err := runEventsAck(context.Background(), fs, "12,abc")
	require.Error(t, err)
	require.Empty(t, fs.marked)
}
