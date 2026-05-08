package sqlite

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	runnerpkg "github.com/tsumina/dango/internal/engine/runner"
)

func TestRunnerStoreAppendAssignsMonotonicSeq(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "dango.db")
	dbStore, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	runnerStore := NewRunnerStore(dbStore)
	ctx := context.Background()
	const id = "alpha"

	seq, err := runnerStore.Append(ctx, id, &runnerpkg.RunnerRecord{Kind: runnerpkg.RunnerRecordInit})
	if err != nil {
		t.Fatalf("Append init: %v", err)
	}
	if seq != 1 {
		t.Fatalf("init seq = %d, want 1", seq)
	}

	seq2, err := runnerStore.Append(ctx, id, &runnerpkg.RunnerRecord{Kind: runnerpkg.RunnerRecordStatus, Status: runnerpkg.RunnerStatusRunning})
	if err != nil {
		t.Fatalf("Append status: %v", err)
	}
	if seq2 != 2 {
		t.Fatalf("status seq = %d, want 2", seq2)
	}

	seq3, err := runnerStore.Append(ctx, id, &runnerpkg.RunnerRecord{Kind: runnerpkg.RunnerRecordEvent, Event: &runnerpkg.StoredRunnerEvent{Type: runnerpkg.EventNodeCompleted.String(), NodeID: "n1", DataEncoding: "json", DataJSON: []byte("10")}})
	if err != nil {
		t.Fatalf("Append event: %v", err)
	}
	if seq3 != 3 {
		t.Fatalf("event seq = %d, want 3", seq3)
	}

	if err := dbStore.Close(); err != nil {
		t.Fatalf("Close before reopen: %v", err)
	}
	reopened, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open(reopen): %v", err)
	}
	t.Cleanup(func() {
		if err := reopened.Close(); err != nil {
			t.Fatalf("Close(reopen): %v", err)
		}
	})

	records, err := NewRunnerStore(reopened).Load(ctx, id)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(records) != 3 {
		t.Fatalf("records = %d, want 3", len(records))
	}
	if records[0].Kind != runnerpkg.RunnerRecordInit || records[0].Seq != 1 {
		t.Fatalf("records[0] = %+v", records[0])
	}
	if records[0].Timestamp.IsZero() {
		t.Fatal("init record was not timestamped")
	}
	if records[2].Event == nil || records[2].Event.Type != runnerpkg.EventNodeCompleted.String() {
		t.Fatalf("records[2].Event = %+v, want NodeCompleted", records[2].Event)
	}
	if string(records[2].Event.DataJSON) != "10" {
		t.Fatalf("records[2].Event.DataJSON = %s, want 10", string(records[2].Event.DataJSON))
	}
}

func TestRunnerStoreFirstAppendMustBeInit(t *testing.T) {
	t.Parallel()

	runnerStore, cleanup := mustNewSQLiteRunnerStore(t)
	defer cleanup()

	_, err := runnerStore.Append(context.Background(), "a", &runnerpkg.RunnerRecord{Kind: runnerpkg.RunnerRecordStatus, Status: runnerpkg.RunnerStatusRunning})
	if !errors.Is(err, runnerpkg.ErrRunnerLogNotInitialised) {
		t.Fatalf("err = %v, want ErrRunnerLogNotInitialised", err)
	}
}

func TestRunnerStoreRejectsDoubleInit(t *testing.T) {
	t.Parallel()

	runnerStore, cleanup := mustNewSQLiteRunnerStore(t)
	defer cleanup()

	ctx := context.Background()
	if _, err := runnerStore.Append(ctx, "a", &runnerpkg.RunnerRecord{Kind: runnerpkg.RunnerRecordInit}); err != nil {
		t.Fatalf("Append init: %v", err)
	}
	_, err := runnerStore.Append(ctx, "a", &runnerpkg.RunnerRecord{Kind: runnerpkg.RunnerRecordInit})
	if !errors.Is(err, runnerpkg.ErrRunnerLogAlreadyInitialised) {
		t.Fatalf("err = %v, want ErrRunnerLogAlreadyInitialised", err)
	}
}

func TestRunnerStoreLoadMissing(t *testing.T) {
	t.Parallel()

	runnerStore, cleanup := mustNewSQLiteRunnerStore(t)
	defer cleanup()

	_, err := runnerStore.Load(context.Background(), "missing")
	if !errors.Is(err, runnerpkg.ErrRunnerLogNotFound) {
		t.Fatalf("err = %v, want ErrRunnerLogNotFound", err)
	}
}

func TestRunnerStoreDelete(t *testing.T) {
	t.Parallel()

	runnerStore, cleanup := mustNewSQLiteRunnerStore(t)
	defer cleanup()

	ctx := context.Background()
	if _, err := runnerStore.Append(ctx, "a", &runnerpkg.RunnerRecord{Kind: runnerpkg.RunnerRecordInit}); err != nil {
		t.Fatalf("Append init: %v", err)
	}
	if err := runnerStore.Delete(ctx, "a"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if err := runnerStore.Delete(ctx, "a"); !errors.Is(err, runnerpkg.ErrRunnerLogNotFound) {
		t.Fatalf("Delete missing err = %v, want ErrRunnerLogNotFound", err)
	}
}

func TestRunnerStoreConcurrentAppendSerialises(t *testing.T) {
	t.Parallel()

	runnerStore, cleanup := mustNewSQLiteRunnerStore(t)
	defer cleanup()

	ctx := context.Background()
	const id = "parallel"
	if _, err := runnerStore.Append(ctx, id, &runnerpkg.RunnerRecord{Kind: runnerpkg.RunnerRecordInit}); err != nil {
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
				if _, err := runnerStore.Append(ctx, id, &runnerpkg.RunnerRecord{Kind: runnerpkg.RunnerRecordStatus, Status: runnerpkg.RunnerStatusRunning}); err != nil {
					t.Errorf("Append: %v", err)
					return
				}
			}
		}()
	}
	wg.Wait()

	records, err := runnerStore.Load(ctx, id)
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

func TestRunnerStoreConcurrentAppendAcrossStoresSerialises(t *testing.T) {
	t.Parallel()

	dbPath := filepath.Join(t.TempDir(), "shared.db")
	dbStoreA, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open(store A): %v", err)
	}
	t.Cleanup(func() {
		if err := dbStoreA.Close(); err != nil {
			t.Fatalf("Close(store A): %v", err)
		}
	})
	dbStoreB, err := Open(dbPath)
	if err != nil {
		t.Fatalf("Open(store B): %v", err)
	}
	t.Cleanup(func() {
		if err := dbStoreB.Close(); err != nil {
			t.Fatalf("Close(store B): %v", err)
		}
	})

	storeA := NewRunnerStore(dbStoreA)
	storeB := NewRunnerStore(dbStoreB)
	ctx := context.Background()
	const id = "shared-runner"
	if _, err := storeA.Append(ctx, id, &runnerpkg.RunnerRecord{Kind: runnerpkg.RunnerRecordInit}); err != nil {
		t.Fatalf("Append init: %v", err)
	}

	stores := []*RunnerStore{storeA, storeB}
	const writers = 8
	const perWriter = 20
	var wg sync.WaitGroup
	wg.Add(writers)
	for i := 0; i < writers; i++ {
		store := stores[i%len(stores)]
		go func(store *RunnerStore) {
			defer wg.Done()
			for j := 0; j < perWriter; j++ {
				if _, err := store.Append(ctx, id, &runnerpkg.RunnerRecord{Kind: runnerpkg.RunnerRecordStatus, Status: runnerpkg.RunnerStatusRunning}); err != nil {
					t.Errorf("Append: %v", err)
					return
				}
			}
		}(store)
	}
	wg.Wait()

	records, err := storeA.Load(ctx, id)
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

func TestRunnerStoreRoundTripsMarkdownEventData(t *testing.T) {
	t.Parallel()

	runnerStore, cleanup := mustNewSQLiteRunnerStore(t)
	defer cleanup()

	ctx := context.Background()
	const markdown = "# Exchange\n\n```yaml\ntitle: demo\n```\n"
	if _, err := runnerStore.Append(ctx, "markdown", &runnerpkg.RunnerRecord{Kind: runnerpkg.RunnerRecordInit}); err != nil {
		t.Fatalf("Append init: %v", err)
	}
	if _, err := runnerStore.Append(ctx, "markdown", &runnerpkg.RunnerRecord{Kind: runnerpkg.RunnerRecordEvent, Event: &runnerpkg.StoredRunnerEvent{Type: runnerpkg.EventNodeCompleted.String(), DataEncoding: "markdown", DataText: markdown}}); err != nil {
		t.Fatalf("Append event: %v", err)
	}

	records, err := runnerStore.Load(ctx, "markdown")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(records) != 2 || records[1].Event == nil {
		t.Fatalf("records = %+v, want init + event", records)
	}
	if records[1].Event.DataEncoding != "markdown" {
		t.Fatalf("data encoding = %q, want markdown", records[1].Event.DataEncoding)
	}
	if records[1].Event.DataText != markdown {
		t.Fatalf("data text = %q, want %q", records[1].Event.DataText, markdown)
	}
}

func TestRunnerStoreLoadCorruptRecordIncludesRunnerIDAndSequence(t *testing.T) {
	t.Parallel()

	runnerStore, dbStore, cleanup := mustNewSQLiteRunnerStoreWithDB(t)
	defer cleanup()

	ctx := context.Background()
	if _, err := runnerStore.Append(ctx, "broken", &runnerpkg.RunnerRecord{Kind: runnerpkg.RunnerRecordInit, Timestamp: time.Unix(1_700_000_000, 0).UTC()}); err != nil {
		t.Fatalf("Append init: %v", err)
	}
	if _, err := dbStore.db.ExecContext(ctx, `
		INSERT INTO runner_records (runner_id, sequence_number, kind, timestamp, record_json)
		VALUES (?, ?, ?, ?, ?)
	`, "broken", 2, "status", time.Unix(1_700_000_001, 0).UTC().Format(time.RFC3339Nano), `{"seq":2,"kind":`); err != nil {
		t.Fatalf("insert corrupt row: %v", err)
	}

	_, err := runnerStore.Load(ctx, "broken")
	if err == nil {
		t.Fatal("Load() error = nil, want corrupt row error")
	}
	if !strings.Contains(err.Error(), `"broken"/2`) {
		t.Fatalf("Load() error = %v, want runner id and sequence", err)
	}
}

func mustNewSQLiteRunnerStore(t *testing.T) (*RunnerStore, func()) {
	t.Helper()
	_, dbStore, cleanup := mustNewSQLiteRunnerStoreWithDB(t)
	return NewRunnerStore(dbStore), cleanup
}

func mustNewSQLiteRunnerStoreWithDB(t *testing.T) (*RunnerStore, *Store, func()) {
	t.Helper()
	dbStore, err := Open(filepath.Join(t.TempDir(), "dango.db"))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	cleanup := func() {
		if err := dbStore.Close(); err != nil {
			t.Fatalf("Close: %v", err)
		}
	}
	return NewRunnerStore(dbStore), dbStore, cleanup
}
