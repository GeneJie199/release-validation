package runstore

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"time"

	"github.com/GeneJie199/release-validation/internal/guard"
)

func TestVersionTwoStoreMigratesLeaseColumns(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs-v2.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Exec(`CREATE TABLE runs (
  id TEXT PRIMARY KEY,
  release_id TEXT NOT NULL,
  plan_sha256 TEXT NOT NULL,
  stage TEXT NOT NULL,
  plan_json TEXT NOT NULL,
  report_json TEXT,
  started_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  completed_at TEXT
);
CREATE INDEX idx_runs_release ON runs(release_id, updated_at DESC);
CREATE INDEX idx_runs_active ON runs(release_id, plan_sha256, stage);
CREATE UNIQUE INDEX idx_runs_one_active ON runs(release_id, plan_sha256) WHERE stage IN ('checking','observing','finalizing');
PRAGMA user_version=2;`)
	if err != nil {
		t.Fatal(err)
	}
	if err = db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var version int
	if err = store.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil || version != SchemaVersion {
		t.Fatalf("schema version=%d err=%v", version, err)
	}
	plan := guard.Plan{ReleaseID: "migrated", Version: "1", Rollback: []string{"undo"}}
	record, err := store.Create(context.Background(), plan.ReleaseID, "sha", plan)
	if err != nil {
		t.Fatal(err)
	}
	owner, _ := NewLeaseOwner()
	if acquired, acquireErr := store.AcquireLease(context.Background(), record.ID, owner); acquireErr != nil || !acquired {
		t.Fatalf("lease acquired=%v err=%v", acquired, acquireErr)
	}
}

func TestRunLifecyclePersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.db")
	store, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	plan := guard.Plan{ReleaseID: "release-1", Version: "1.0", Rollback: []string{"undo"}}
	record, err := store.Create(ctx, plan.ReleaseID, "sha", plan)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now().UTC()
	report := guard.Report{ReleaseID: plan.ReleaseID, Decision: "HOLD", Observation: &guard.ObservationState{Status: "observing", StartedAt: started, DeadlineAt: started.Add(time.Minute), Samples: []guard.FleetEvidence{}}}
	if err := store.Update(ctx, record.ID, "observing", report); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store, err = Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	active, err := store.FindActive(ctx, plan.ReleaseID, "sha")
	if err != nil || active.Report == nil || active.Report.Observation == nil || active.Stage != "observing" {
		t.Fatalf("active=%+v err=%v", active, err)
	}
	active.Report.Decision = "GO"
	if err := store.Update(ctx, active.ID, "finalizing", *active.Report); err != nil {
		t.Fatal(err)
	}
	active, err = store.FindActive(ctx, plan.ReleaseID, "sha")
	if err != nil || active.Stage != "finalizing" || active.Report == nil {
		t.Fatalf("finalizing run = %+v, %v", active, err)
	}
	if err := store.Update(ctx, active.ID, "completed", *active.Report); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FindActive(ctx, plan.ReleaseID, "sha"); err == nil {
		t.Fatal("completed run must not be active")
	}
	if err := store.Update(ctx, active.ID, "checking", *active.Report); err == nil {
		t.Fatal("completed run must not return to an active stage")
	}
	items, err := store.List(ctx, 10)
	if err != nil || len(items) != 1 || items[0].CompletedAt == nil || items[0].Report.Decision != "GO" {
		t.Fatalf("items=%+v err=%v", items, err)
	}
}

func TestConcurrentCreateReusesSingleActiveRun(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.db")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	plan := guard.Plan{ReleaseID: "same-release", Version: "1", Rollback: []string{"undo"}}
	type result struct {
		record RunRecord
		err    error
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	for _, store := range []*Store{first, second} {
		go func(current *Store) {
			<-start
			record, createErr := current.Create(context.Background(), plan.ReleaseID, "same-sha", plan)
			results <- result{record, createErr}
		}(store)
	}
	close(start)
	a, b := <-results, <-results
	if a.err != nil || b.err != nil || a.record.ID != b.record.ID {
		t.Fatalf("creates = %+v, %+v", a, b)
	}
	items, err := first.List(context.Background(), 10)
	if err != nil || len(items) != 1 {
		t.Fatalf("items = %+v, %v", items, err)
	}
}

func TestLeasePreventsDuplicateExecutionAndCanBeReclaimed(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "runs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	plan := guard.Plan{ReleaseID: "leased-release", Version: "1", Rollback: []string{"undo"}}
	record, err := store.Create(ctx, plan.ReleaseID, "sha", plan)
	if err != nil {
		t.Fatal(err)
	}
	ownerA, _ := NewLeaseOwner()
	ownerB, _ := NewLeaseOwner()
	if acquired, err := store.AcquireLease(ctx, record.ID, ownerA); err != nil || !acquired {
		t.Fatalf("first lease acquired=%v err=%v", acquired, err)
	}
	if acquired, err := store.AcquireLease(ctx, record.ID, ownerB); err != nil || acquired {
		t.Fatalf("second lease acquired=%v err=%v", acquired, err)
	}
	report := guard.Report{ReleaseID: plan.ReleaseID, Decision: "HOLD"}
	if err := store.Update(ctx, record.ID, "checking", report); err == nil {
		t.Fatal("unowned writer updated a leased run")
	}
	if err := store.UpdateOwned(ctx, record.ID, ownerA, "checking", report); err != nil {
		t.Fatal(err)
	}
	if _, err := store.db.Exec(`UPDATE runs SET lease_expires_at=? WHERE id=?`, formatTime(time.Now().UTC().Add(-time.Second)), record.ID); err != nil {
		t.Fatal(err)
	}
	if acquired, err := store.AcquireLease(ctx, record.ID, ownerB); err != nil || !acquired {
		t.Fatalf("expired lease reclaimed=%v err=%v", acquired, err)
	}
	if err := store.UpdateOwned(ctx, record.ID, ownerA, "checking", report); err == nil {
		t.Fatal("previous owner updated a reclaimed run")
	}
	if err := store.RenewLease(ctx, record.ID, ownerB); err != nil {
		t.Fatal(err)
	}
	if err := store.ReleaseLease(ctx, record.ID, ownerB); err != nil {
		t.Fatal(err)
	}
}

func TestAbandonClosesStaleRunWithAuditReason(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "runs.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	plan := guard.Plan{ReleaseID: "stale-release", Version: "1", Rollback: []string{"keep old version"}}
	record, err := store.Create(context.Background(), plan.ReleaseID, "plan-sha", plan)
	if err != nil {
		t.Fatal(err)
	}
	record, err = store.Abandon(context.Background(), record.ID, "deployment window closed")
	if err != nil || record.Stage != "abandoned" || record.Report == nil || record.Report.Decision != "HOLD" || record.CompletedAt == nil {
		t.Fatalf("abandoned = %+v, %v", record, err)
	}
	if _, err := store.FindActive(context.Background(), plan.ReleaseID, "plan-sha"); err == nil {
		t.Fatal("abandoned run remained active")
	}
	if _, err := store.Abandon(context.Background(), record.ID, "again"); err == nil {
		t.Fatal("terminal run was abandoned twice")
	}
}

func TestOpenReadOnlyListsWithoutAllowingWrites(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.db")
	writable, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	plan := guard.Plan{ReleaseID: "read-only", Version: "1", Rollback: []string{"undo"}}
	if _, err := writable.Create(context.Background(), plan.ReleaseID, "sha", plan); err != nil {
		t.Fatal(err)
	}
	if err := writable.Close(); err != nil {
		t.Fatal(err)
	}
	readOnly, err := OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer readOnly.Close()
	if items, err := readOnly.List(context.Background(), 10); err != nil || len(items) != 1 {
		t.Fatalf("items=%v err=%v", items, err)
	}
	if _, err := readOnly.Create(context.Background(), "blocked", "sha", plan); err == nil {
		t.Fatal("read-only store accepted a write")
	}
}

func TestOpenReadOnlySeesLiveWALCheckpoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "runs.db")
	writer, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	plan := guard.Plan{ReleaseID: "live-wal", Version: "1", Rollback: []string{"undo"}}
	if _, err := writer.Create(context.Background(), plan.ReleaseID, "sha", plan); err != nil {
		t.Fatal(err)
	}
	reader, err := OpenReadOnly(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	items, err := reader.List(context.Background(), 10)
	if err != nil || len(items) != 1 || items[0].ReleaseID != plan.ReleaseID {
		t.Fatalf("live WAL items=%+v err=%v", items, err)
	}
}
