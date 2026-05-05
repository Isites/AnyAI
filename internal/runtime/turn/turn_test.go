package turn

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestTurnTouchExtendsIdleTimeout(t *testing.T) {
	tu := New(context.Background(), Config{
		SessionID:   "session-touch",
		IdleTimeout: 50 * time.Millisecond,
	})
	require.NotNil(t, tu)

	time.Sleep(30 * time.Millisecond)
	tu.Touch()

	select {
	case <-tu.Done():
		t.Fatal("turn timed out before refreshed idle window elapsed")
	case <-time.After(35 * time.Millisecond):
	}

	select {
	case <-tu.Done():
	case <-time.After(120 * time.Millisecond):
		t.Fatal("turn did not time out after idle window elapsed")
	}

	assert.Equal(t, StateTimedOut, tu.State())
	assert.ErrorIs(t, tu.Err(), context.Canceled)
}

func TestStoreCreateAndLookup(t *testing.T) {
	store := NewStore()
	created := store.Create(context.Background(), Config{SessionID: "session-1"})
	require.NotNil(t, created)

	loaded, ok := store.Get(created.ID)
	require.True(t, ok)
	assert.Same(t, created, loaded)
	assert.Len(t, store.GetBySession("session-1"), 1)

	store.Remove(created.ID)
	_, ok = store.Get(created.ID)
	assert.False(t, ok)
}
