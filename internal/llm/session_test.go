package llm

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestConversationJSONRoundTrip(t *testing.T) {
	c := NewConversation(nil, "be brief", []ToolSpec{{
		Name:        "echo",
		Description: "repeat",
		Parameters:  map[string]any{"type": "object"},
	}})
	c.SetAutoShrink(AutoShrinkConfig{ContextWindow: 8000, Threshold: 0.8, KeepToolExchanges: 3, KeepTurns: 5})
	c.AppendUser("hi")
	c.AppendToolCall(ToolCall{CallID: "c1", Name: "echo", Arguments: `{"x":1}`})
	c.AppendToolOutput("c1", "ok", nil)
	c.AppendAssistantText("done")
	if err := c.recordUsage(context.Background(), TokenUsage{Input: 10, Output: 4, Total: 14}); err != nil {
		t.Fatalf("recordUsage: %v", err)
	}

	data, err := json.Marshal(c)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var restored Conversation
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if got, want := restored.Instructions(), "be brief"; got != want {
		t.Errorf("instructions = %q, want %q", got, want)
	}
	if got := restored.Tools(); len(got) != 1 || got[0].Name != "echo" {
		t.Errorf("tools = %+v", got)
	}
	if got := restored.Turns(); len(got) != 4 {
		t.Fatalf("turns = %d, want 4", len(got))
	} else {
		if got[1].Role != RoleToolCall || got[1].Tool == nil || got[1].Tool.CallID != "c1" {
			t.Errorf("tool_call turn = %+v", got[1])
		}
		if got[2].Role != RoleToolOutput || got[2].Tool == nil || got[2].Tool.Output != "ok" {
			t.Errorf("tool_output turn = %+v", got[2])
		}
	}
	if got, want := restored.Usage().Total, 14; got != want {
		t.Errorf("usage.total = %d, want %d", got, want)
	}
	if got := restored.autoShrink; got.ContextWindow != 8000 || got.KeepTurns != 5 {
		t.Errorf("autoShrink = %+v", got)
	}
}

func TestJSONStoreSaveLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	store, err := NewJSONStore(dir)
	if err != nil {
		t.Fatalf("NewJSONStore: %v", err)
	}
	ctx := context.Background()

	conv := NewConversation(nil, "sys", nil)
	conv.AppendUser("hello")
	conv.AppendAssistantText("hi")
	sess := NewSession("alpha", conv)

	if err := store.Save(ctx, sess); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if sess.CreatedAt.IsZero() || sess.UpdatedAt.IsZero() {
		t.Errorf("timestamps not populated: %+v", sess)
	}
	first := sess.CreatedAt

	// Confirm the file exists with the expected name and no stray temps.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if len(names) != 1 || names[0] != "alpha.json" {
		t.Errorf("dir entries = %v, want [alpha.json]", names)
	}

	loaded, err := store.Load(ctx, "alpha")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if loaded.ID != "alpha" {
		t.Errorf("id = %q", loaded.ID)
	}
	if !loaded.CreatedAt.Equal(first) {
		t.Errorf("createdAt = %v, want %v", loaded.CreatedAt, first)
	}
	if loaded.Conv == nil || loaded.Conv.Len() != 2 {
		t.Fatalf("conv not restored: %+v", loaded.Conv)
	}

	// Second save preserves CreatedAt but bumps UpdatedAt.
	time.Sleep(2 * time.Millisecond)
	loaded.Conv.AppendUser("again")
	if err := store.Save(ctx, loaded); err != nil {
		t.Fatalf("Save 2: %v", err)
	}
	if !loaded.CreatedAt.Equal(first) {
		t.Errorf("createdAt mutated: %v, want %v", loaded.CreatedAt, first)
	}
	if !loaded.UpdatedAt.After(first) {
		t.Errorf("updatedAt did not advance: %v", loaded.UpdatedAt)
	}
}

func TestJSONStoreLoadMissing(t *testing.T) {
	store, err := NewJSONStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONStore: %v", err)
	}
	_, err = store.Load(context.Background(), "nope")
	if !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("err = %v, want ErrSessionNotFound", err)
	}
}

func TestJSONStoreDelete(t *testing.T) {
	dir := t.TempDir()
	store, err := NewJSONStore(dir)
	if err != nil {
		t.Fatalf("NewJSONStore: %v", err)
	}
	ctx := context.Background()
	sess := NewSession("gone", NewConversation(nil, "x", nil))
	if err := store.Save(ctx, sess); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if err := store.Delete(ctx, "gone"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := store.Delete(ctx, "gone"); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("second Delete err = %v, want ErrSessionNotFound", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "gone.json")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("file still present after Delete: %v", err)
	}
}

func TestJSONStoreRejectsUnsafeIDs(t *testing.T) {
	store, err := NewJSONStore(t.TempDir())
	if err != nil {
		t.Fatalf("NewJSONStore: %v", err)
	}
	ctx := context.Background()
	for _, id := range []string{"", "../escape", "a/b", ".hidden", "a..b"} {
		if _, err := store.Load(ctx, id); err == nil {
			t.Errorf("Load(%q) accepted unsafe id", id)
		}
		sess := &Session{ID: id, Conv: NewConversation(nil, "x", nil)}
		if err := store.Save(ctx, sess); err == nil {
			t.Errorf("Save(%q) accepted unsafe id", id)
		}
	}
}

func TestJSONStoreAtomicWriteLeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	store, err := NewJSONStore(dir)
	if err != nil {
		t.Fatalf("NewJSONStore: %v", err)
	}
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		sess := NewSession("stress", NewConversation(nil, "x", nil))
		if err := store.Save(ctx, sess); err != nil {
			t.Fatalf("Save: %v", err)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("leftover temp file: %s", e.Name())
		}
	}
}
