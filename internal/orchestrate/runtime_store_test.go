package orchestrate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func mustNewRuntimeStore(t *testing.T, dir string) *JSONRuntimeStore {
	t.Helper()
	store, err := NewJSONRuntimeStore(dir)
	if err != nil {
		t.Fatalf("NewJSONRuntimeStore: %v", err)
	}
	return store
}

func TestJSONRuntimeStoreAppendAssignsMonotonicSeq(t *testing.T) {
	store := mustNewRuntimeStore(t, t.TempDir())
	ctx := context.Background()
	const id = "alpha"

	seq, err := store.Append(ctx, id, &RuntimeRecord{Kind: RuntimeRecordInit})
	if err != nil {
		t.Fatalf("Append init: %v", err)
	}
	if seq != 1 {
		t.Fatalf("init seq = %d, want 1", seq)
	}

	seq2, err := store.Append(ctx, id, &RuntimeRecord{Kind: RuntimeRecordStatus, Status: RuntimeStatusRunning})
	if err != nil {
		t.Fatalf("Append status: %v", err)
	}
	if seq2 != 2 {
		t.Fatalf("status seq = %d, want 2", seq2)
	}

	seq3, err := store.Append(ctx, id, &RuntimeRecord{Kind: RuntimeRecordEvent, Event: newStoredRuntimeEvent(RuntimeEvent{Type: EventNodeCompleted, NodeID: "n1", Data: 10})})
	if err != nil {
		t.Fatalf("Append event: %v", err)
	}
	if seq3 != 3 {
		t.Fatalf("event seq = %d, want 3", seq3)
	}

	records, err := store.Load(ctx, id)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("records = %d, want 3", len(records))
	}
	if records[0].Kind != RuntimeRecordInit || records[0].Seq != 1 {
		t.Fatalf("records[0] = %+v", records[0])
	}
	if records[0].Timestamp.IsZero() {
		t.Fatal("init record was not timestamped")
	}
	if records[2].Event == nil || records[2].Event.Type != EventNodeCompleted.String() {
		t.Fatalf("records[2].Event = %+v, want NodeCompleted", records[2].Event)
	}
	if string(records[2].Event.DataJSON) != "10" {
		t.Fatalf("records[2].Event.DataJSON = %s, want 10", string(records[2].Event.DataJSON))
	}
}

func TestJSONRuntimeStoreFirstAppendMustBeInit(t *testing.T) {
	store := mustNewRuntimeStore(t, t.TempDir())
	_, err := store.Append(context.Background(), "a", &RuntimeRecord{Kind: RuntimeRecordStatus, Status: RuntimeStatusRunning})
	if !errors.Is(err, ErrRuntimeLogNotInitialised) {
		t.Fatalf("err = %v, want ErrRuntimeLogNotInitialised", err)
	}
}

func TestJSONRuntimeStoreRejectsDoubleInit(t *testing.T) {
	store := mustNewRuntimeStore(t, t.TempDir())
	ctx := context.Background()
	if _, err := store.Append(ctx, "a", &RuntimeRecord{Kind: RuntimeRecordInit}); err != nil {
		t.Fatalf("Append init: %v", err)
	}
	_, err := store.Append(ctx, "a", &RuntimeRecord{Kind: RuntimeRecordInit})
	if !errors.Is(err, ErrRuntimeLogAlreadyInitialised) {
		t.Fatalf("err = %v, want ErrRuntimeLogAlreadyInitialised", err)
	}
}

func TestJSONRuntimeStoreLoadMissing(t *testing.T) {
	store := mustNewRuntimeStore(t, t.TempDir())
	_, err := store.Load(context.Background(), "missing")
	if !errors.Is(err, ErrRuntimeLogNotFound) {
		t.Fatalf("err = %v, want ErrRuntimeLogNotFound", err)
	}
}

func TestJSONRuntimeStoreLoadToleratesPartialTrailingLine(t *testing.T) {
	dir := t.TempDir()
	store := mustNewRuntimeStore(t, dir)
	ctx := context.Background()
	if _, err := store.Append(ctx, "a", &RuntimeRecord{Kind: RuntimeRecordInit}); err != nil {
		t.Fatalf("Append init: %v", err)
	}

	f, err := os.OpenFile(filepath.Join(dir, "a.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open partial file: %v", err)
	}
	if _, err := f.WriteString(`{"seq":2,"kind":"event","event":`); err != nil {
		t.Fatalf("write partial: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close partial file: %v", err)
	}

	records, err := store.Load(ctx, "a")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(records) != 1 || records[0].Kind != RuntimeRecordInit {
		t.Fatalf("records = %+v, want only init", records)
	}
}

func TestJSONRuntimeStoreAppendAfterPartialLineContinuesSeq(t *testing.T) {
	dir := t.TempDir()
	store := mustNewRuntimeStore(t, dir)
	ctx := context.Background()
	if _, err := store.Append(ctx, "a", &RuntimeRecord{Kind: RuntimeRecordInit}); err != nil {
		t.Fatalf("Append init: %v", err)
	}

	f, err := os.OpenFile(filepath.Join(dir, "a.jsonl"), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open partial file: %v", err)
	}
	if _, err := f.WriteString(`{"seq":2,"kind":"`); err != nil {
		t.Fatalf("write partial: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("close partial file: %v", err)
	}

	seq, err := store.Append(ctx, "a", &RuntimeRecord{Kind: RuntimeRecordStatus, Status: RuntimeStatusRunning})
	if err != nil {
		t.Fatalf("Append after partial: %v", err)
	}
	if seq != 2 {
		t.Fatalf("seq = %d, want 2", seq)
	}
}

func TestJSONRuntimeStoreDelete(t *testing.T) {
	store := mustNewRuntimeStore(t, t.TempDir())
	ctx := context.Background()
	if _, err := store.Append(ctx, "a", &RuntimeRecord{Kind: RuntimeRecordInit}); err != nil {
		t.Fatalf("Append init: %v", err)
	}
	if err := store.Delete(ctx, "a"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := store.Delete(ctx, "a"); !errors.Is(err, ErrRuntimeLogNotFound) {
		t.Fatalf("Delete missing err = %v, want ErrRuntimeLogNotFound", err)
	}
}

func TestJSONRuntimeStoreConcurrentAppendSerialises(t *testing.T) {
	store := mustNewRuntimeStore(t, t.TempDir())
	ctx := context.Background()
	const id = "parallel"
	if _, err := store.Append(ctx, id, &RuntimeRecord{Kind: RuntimeRecordInit}); err != nil {
		t.Fatalf("Append init: %v", err)
	}

	const writers = 8
	const perWriter = 20
	var wg sync.WaitGroup
	wg.Add(writers)
	for i := 0; i < writers; i++ {
		go func() {
			defer wg.Done()
			for j := 0; j < perWriter; j++ {
				if _, err := store.Append(ctx, id, &RuntimeRecord{Kind: RuntimeRecordStatus, Status: RuntimeStatusRunning}); err != nil {
					t.Errorf("Append: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	records, err := store.Load(ctx, id)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := 1 + writers*perWriter
	if len(records) != want {
		t.Fatalf("records = %d, want %d", len(records), want)
	}
	for i, rec := range records {
		if rec.Seq != int64(i+1) {
			t.Fatalf("records[%d].Seq = %d, want %d", i, rec.Seq, i+1)
		}
	}
}
