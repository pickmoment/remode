package sqlite_test

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pickmoment/remode/internal/core"
	"github.com/pickmoment/remode/internal/store/sqlite"
)

func newTestStore(t *testing.T) *sqlite.Store {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "remode-*.db")
	require.NoError(t, err)
	f.Close()
	store, err := sqlite.Open(f.Name())
	require.NoError(t, err)
	t.Cleanup(func() { store.Close() })
	return store
}

func testSession(name string) *core.Session {
	return &core.Session{
		Name:      name,
		TmuxName:  "tc-claude-" + name,
		SessionID: "sess-abc",
		CWD:       "/tmp/myproject",
		JSONLPath: "/tmp/out.jsonl",
		ChatID:    42,
		CreatedAt: time.Now().Truncate(time.Second),
		Level:     core.LevelAll,
		AgentType: "claude_code",
	}
}

func TestSaveAndGet(t *testing.T) {
	s := newTestStore(t)
	sess := testSession("alpha")
	require.NoError(t, s.Save(sess))

	got, err := s.Get("alpha")
	require.NoError(t, err)
	assert.Equal(t, sess.Name, got.Name)
	assert.Equal(t, sess.SessionID, got.SessionID)
	assert.Equal(t, sess.ChatID, got.ChatID)
	assert.Equal(t, sess.Level, got.Level)
	assert.Equal(t, sess.CreatedAt.UTC(), got.CreatedAt.UTC())
}

func TestSaveUpsert(t *testing.T) {
	s := newTestStore(t)
	sess := testSession("beta")
	require.NoError(t, s.Save(sess))
	sess.SessionID = "sess-new"
	require.NoError(t, s.Save(sess))

	got, err := s.Get("beta")
	require.NoError(t, err)
	assert.Equal(t, "sess-new", got.SessionID)
}

func TestList(t *testing.T) {
	s := newTestStore(t)
	require.NoError(t, s.Save(testSession("a")))
	require.NoError(t, s.Save(testSession("b")))

	list, err := s.List()
	require.NoError(t, err)
	assert.Len(t, list, 2)
}

func TestDelete(t *testing.T) {
	s := newTestStore(t)
	require.NoError(t, s.Save(testSession("gamma")))
	require.NoError(t, s.Delete("gamma"))

	list, err := s.List()
	require.NoError(t, err)
	assert.Empty(t, list)
}

func TestUpdateOffset(t *testing.T) {
	s := newTestStore(t)
	require.NoError(t, s.Save(testSession("delta")))
	require.NoError(t, s.UpdateOffset("delta", 1234))

	got, err := s.Get("delta")
	require.NoError(t, err)
	assert.Equal(t, int64(1234), got.JSONLOffset)
}

func TestUpdateMessageLevel(t *testing.T) {
	s := newTestStore(t)
	require.NoError(t, s.Save(testSession("eps")))
	require.NoError(t, s.UpdateMessageLevel("eps", "interactive"))

	got, err := s.Get("eps")
	require.NoError(t, err)
	assert.Equal(t, core.LevelInteractive, got.Level)
}

func TestUpdateChatID(t *testing.T) {
	s := newTestStore(t)
	require.NoError(t, s.Save(testSession("zeta")))
	require.NoError(t, s.UpdateChatID("zeta", 99))

	got, err := s.Get("zeta")
	require.NoError(t, err)
	assert.Equal(t, int64(99), got.ChatID)
}

func TestGetNotFound(t *testing.T) {
	s := newTestStore(t)
	_, err := s.Get("nope")
	assert.Error(t, err)
}
