package llm

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestJSONStoreAppendAssignsMonotonicSeq(t *testing.T) {
	store := mustNewStore(t, t.TempDir())
	ctx := context.Background()
	const id = "alpha"

	seq, err := store.Append(ctx, id, &Event{Kind: EventInit, Instructions: "sys"})
	if err != nil {
		t.Fatalf("Append init: %v", err)
	}
	if seq != 1 {
		t.Errorf("init Seq = %d, want 1", seq)
	}
	seq2, err := store.Append(ctx, id, &Event{Kind: EventAppendUser, Turn: &Turn{Role: RoleUser, Text: "hi"}})
	if err != nil {
		t.Fatalf("Append user: %v", err)
	}
	if seq2 != 2 {
		t.Errorf("user Seq = %d, want 2", seq2)
	}

	events, err := store.Load(ctx, id)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2", len(events))
	}
	if events[0].Kind != EventInit || events[0].Seq != 1 {
		t.Errorf("events[0] = %+v", events[0])
	}
	if events[1].Kind != EventAppendUser || events[1].Seq != 2 {
		t.Errorf("events[1] = %+v", events[1])
	}
	if events[0].Timestamp.IsZero() {
		t.Error("Append did not stamp Timestamp")
	}
}

func TestJSONStoreFirstAppendMustBeInit(t *testing.T) {
	store := mustNewStore(t, t.TempDir())
	_, err := store.Append(context.Background(), "a", &Event{Kind: EventAppendUser, Turn: &Turn{Role: RoleUser, Text: "x"}})
	if !errors.Is(err, ErrSessionNotInitialised) {
		t.Errorf("err = %v, want ErrSessionNotInitialised", err)
	}
}

func TestJSONStoreRejectsDoubleInit(t *testing.T) {
	store := mustNewStore(t, t.TempDir())
	ctx := context.Background()
	if _, err := store.Append(ctx, "a", &Event{Kind: EventInit}); err != nil {
		t.Fatalf("Append init: %v", err)
	}
	_, err := store.Append(ctx, "a", &Event{Kind: EventInit})
	if !errors.Is(err, ErrSessionAlreadyInitialised) {
		t.Errorf("err = %v, want ErrSessionAlreadyInitialised", err)
	}
}

func TestJSONStoreLoadMissing(t *testing.T) {
	store := mustNewStore(t, t.TempDir())
	_, err := store.Load(context.Background(), "nope")
	if !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("err = %v, want ErrSessionNotFound", err)
	}
}

func TestJSONStoreLoadToleratesPartialTrailingLine(t *testing.T) {
	dir := t.TempDir()
	store := mustNewStore(t, dir)
	ctx := context.Background()
	if _, err := store.Append(ctx, "a", &Event{Kind: EventInit}); err != nil {
		t.Fatalf("Append init: %v", err)
	}
	// Simulate a crash mid-write by appending an unterminated JSON fragment.
	f, err := os.OpenFile(filepath.Join(dir, "a.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := f.WriteString(`{"seq":2,"kind":"append_user","turn":`); err != nil {
		t.Fatalf("write partial: %v", err)
	}
	f.Close()

	events, err := store.Load(ctx, "a")
	if err != nil {
		t.Fatalf("Load with partial tail: %v", err)
	}
	if len(events) != 1 || events[0].Kind != EventInit {
		t.Errorf("events = %+v, want [init]", events)
	}
}

func TestJSONStoreAppendAfterPartialLineContinuesSeq(t *testing.T) {
	dir := t.TempDir()
	store := mustNewStore(t, dir)
	ctx := context.Background()
	if _, err := store.Append(ctx, "a", &Event{Kind: EventInit}); err != nil {
		t.Fatalf("Append init: %v", err)
	}
	f, err := os.OpenFile(filepath.Join(dir, "a.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	f.WriteString(`{"seq":2,"kind":"`)
	f.Close()

	seq, err := store.Append(ctx, "a", &Event{Kind: EventAppendUser, Turn: &Turn{Role: RoleUser, Text: "x"}})
	if err != nil {
		t.Fatalf("Append after partial: %v", err)
	}
	if seq != 2 {
		t.Errorf("seq = %d, want 2 (partial line must not consume a seq)", seq)
	}
}

func TestJSONStoreTruncateKeepsPrefix(t *testing.T) {
	store := mustNewStore(t, t.TempDir())
	ctx := context.Background()
	const id = "tr"
	if _, err := store.Append(ctx, id, &Event{Kind: EventInit}); err != nil {
		t.Fatalf("Append init: %v", err)
	}
	for i := 0; i < 3; i++ {
		if _, err := store.Append(ctx, id, &Event{Kind: EventAppendUser, Turn: &Turn{Role: RoleUser, Text: "x"}}); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
	if err := store.Truncate(ctx, id, 2); err != nil {
		t.Fatalf("Truncate: %v", err)
	}
	events, err := store.Load(ctx, id)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(events) != 2 {
		t.Fatalf("events = %d, want 2", len(events))
	}
	// Next Append continues from Seq 3.
	seq, err := store.Append(ctx, id, &Event{Kind: EventAppendAssistant, Turn: &Turn{Role: RoleAssistant, Text: "y"}})
	if err != nil {
		t.Fatalf("Append after truncate: %v", err)
	}
	if seq != 3 {
		t.Errorf("Seq = %d, want 3 after truncate", seq)
	}
}

func TestJSONStoreTruncateMissing(t *testing.T) {
	store := mustNewStore(t, t.TempDir())
	err := store.Truncate(context.Background(), "nope", 0)
	if !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("err = %v, want ErrSessionNotFound", err)
	}
}

func TestJSONStoreDelete(t *testing.T) {
	dir := t.TempDir()
	store := mustNewStore(t, dir)
	ctx := context.Background()
	if _, err := store.Append(ctx, "a", &Event{Kind: EventInit}); err != nil {
		t.Fatalf("Append init: %v", err)
	}
	if err := store.Delete(ctx, "a"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := store.Delete(ctx, "a"); !errors.Is(err, ErrSessionNotFound) {
		t.Errorf("second Delete err = %v, want ErrSessionNotFound", err)
	}
}

func TestJSONStoreRejectsUnsafeIDs(t *testing.T) {
	store := mustNewStore(t, t.TempDir())
	ctx := context.Background()
	for _, id := range []string{"", "../escape", "a/b", ".hidden", "a..b"} {
		if _, err := store.Load(ctx, id); err == nil {
			t.Errorf("Load(%q) accepted unsafe id", id)
		}
		if _, err := store.Append(ctx, id, &Event{Kind: EventInit}); err == nil {
			t.Errorf("Append(%q) accepted unsafe id", id)
		}
	}
}

func TestJSONStoreConcurrentAppendSerialises(t *testing.T) {
	store := mustNewStore(t, t.TempDir())
	ctx := context.Background()
	const id = "par"
	if _, err := store.Append(ctx, id, &Event{Kind: EventInit}); err != nil {
		t.Fatalf("Append init: %v", err)
	}
	const writers = 8
	const perWriter = 25
	var wg sync.WaitGroup
	wg.Add(writers)
	for w := 0; w < writers; w++ {
		go func() {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				_, err := store.Append(ctx, id, &Event{Kind: EventAppendUser, Turn: &Turn{Role: RoleUser, Text: "x"}})
				if err != nil {
					t.Errorf("Append: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	events, err := store.Load(ctx, id)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := 1 + writers*perWriter
	if len(events) != want {
		t.Fatalf("events = %d, want %d", len(events), want)
	}
	for i, ev := range events {
		if ev.Seq != int64(i+1) {
			t.Fatalf("events[%d].Seq = %d, want %d", i, ev.Seq, i+1)
		}
	}
}

func TestJSONStoreTruncateRewritesCleanly(t *testing.T) {
	dir := t.TempDir()
	store := mustNewStore(t, dir)
	ctx := context.Background()
	const id = "rw"
	if _, err := store.Append(ctx, id, &Event{Kind: EventInit}); err != nil {
		t.Fatalf("Append init: %v", err)
	}
	if _, err := store.Append(ctx, id, &Event{Kind: EventAppendUser, Turn: &Turn{Role: RoleUser, Text: "x"}}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := store.Truncate(ctx, id, 1); err != nil {
		t.Fatalf("Truncate: %v", err)
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

func mustNewStore(t *testing.T, dir string) *JSONStore {
	t.Helper()
	s, err := NewJSONStore(dir)
	if err != nil {
		t.Fatalf("NewJSONStore: %v", err)
	}
	return s
}
