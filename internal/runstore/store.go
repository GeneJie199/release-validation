package runstore

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/GeneJie199/release-validation/internal/guard"
	_ "modernc.org/sqlite"
)

const (
	SchemaVersion = 3
	LeaseTTL      = 30 * time.Second
)

type Store struct {
	db       *sql.DB
	path     string
	readOnly bool
}

type RunRecord struct {
	ID          string        `json:"id"`
	ReleaseID   string        `json:"release_id"`
	PlanSHA256  string        `json:"plan_sha256"`
	Stage       string        `json:"stage"`
	Plan        guard.Plan    `json:"plan"`
	Report      *guard.Report `json:"report,omitempty"`
	StartedAt   time.Time     `json:"started_at"`
	UpdatedAt   time.Time     `json:"updated_at"`
	CompletedAt *time.Time    `json:"completed_at,omitempty"`
}

func Open(path string) (*Store, error) {
	return open(path, false)
}

func OpenReadOnly(path string) (*Store, error) {
	return open(path, true)
}

func open(path string, readOnly bool) (*Store, error) {
	if path == "" {
		return nil, errors.New("run store path is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if readOnly {
		if _, err := os.Stat(abs); err != nil {
			return nil, err
		}
	} else {
		if err := os.MkdirAll(filepath.Dir(abs), 0o750); err != nil {
			return nil, err
		}
	}
	dsn := abs
	if readOnly {
		dsn = "file:" + filepath.ToSlash(abs) + "?mode=ro"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	pragmas := `PRAGMA busy_timeout=5000;`
	if !readOnly {
		pragmas += ` PRAGMA journal_mode=WAL; PRAGMA foreign_keys=ON;`
	} else {
		pragmas += ` PRAGMA query_only=ON;`
	}
	if _, err = db.Exec(pragmas); err != nil {
		_ = db.Close()
		return nil, err
	}
	store := &Store{db: db, path: abs, readOnly: readOnly}
	if readOnly {
		var version int
		err = db.QueryRow(`PRAGMA user_version`).Scan(&version)
		if err == nil && (version < 1 || version > SchemaVersion) {
			err = fmt.Errorf("run store schema %d is not supported", version)
		}
	} else {
		err = store.migrate()
		if err == nil {
			err = store.secureFiles()
		}
	}
	if err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (s *Store) Close() error {
	if s.db == nil {
		return nil
	}
	var checkpointErr error
	if !s.readOnly {
		_, checkpointErr = s.db.Exec(`PRAGMA wal_checkpoint(TRUNCATE);`)
		_ = s.secureFiles()
	}
	closeErr := s.db.Close()
	s.db = nil
	if checkpointErr != nil {
		return checkpointErr
	}
	return closeErr
}

func (s *Store) secureFiles() error {
	for _, path := range []string{s.path, s.path + "-wal", s.path + "-shm"} {
		if err := os.Chmod(path, 0o600); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

func (s *Store) migrate() error {
	var version int
	if err := s.db.QueryRow(`PRAGMA user_version`).Scan(&version); err != nil {
		return err
	}
	if version > SchemaVersion {
		return fmt.Errorf("run store schema %d is newer than supported %d", version, SchemaVersion)
	}
	if version == 0 {
		tx, err := s.db.Begin()
		if err != nil {
			return err
		}
		defer tx.Rollback()
		if _, err = tx.Exec(`CREATE TABLE runs (
  id TEXT PRIMARY KEY,
  release_id TEXT NOT NULL,
  plan_sha256 TEXT NOT NULL,
  stage TEXT NOT NULL,
  plan_json TEXT NOT NULL,
  report_json TEXT,
  started_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  completed_at TEXT,
  lease_owner TEXT,
  lease_expires_at TEXT
);
CREATE INDEX idx_runs_release ON runs(release_id, updated_at DESC);
CREATE INDEX idx_runs_active ON runs(release_id, plan_sha256, stage);`); err != nil {
			return err
		}
		if _, err = tx.Exec(`CREATE UNIQUE INDEX idx_runs_one_active ON runs(release_id, plan_sha256) WHERE stage IN ('checking','observing','finalizing'); PRAGMA user_version=3`); err != nil {
			return err
		}
		return tx.Commit()
	}
	if version == 1 {
		var duplicates int
		if err := s.db.QueryRow(`SELECT COUNT(*) FROM (SELECT 1 FROM runs WHERE stage IN ('checking','observing','finalizing') GROUP BY release_id, plan_sha256 HAVING COUNT(*) > 1)`).Scan(&duplicates); err != nil {
			return err
		}
		if duplicates > 0 {
			return errors.New("run store contains duplicate active runs; abandon duplicates before upgrading")
		}
		if _, err := s.db.Exec(`CREATE UNIQUE INDEX idx_runs_one_active ON runs(release_id, plan_sha256) WHERE stage IN ('checking','observing','finalizing'); PRAGMA user_version=2`); err != nil {
			return err
		}
		version = 2
	}
	if version == 2 {
		if _, err := s.db.Exec(`ALTER TABLE runs ADD COLUMN lease_owner TEXT; ALTER TABLE runs ADD COLUMN lease_expires_at TEXT; PRAGMA user_version=3`); err != nil {
			return err
		}
	}
	return nil
}

func NewLeaseOwner() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return "lease_" + hex.EncodeToString(value), nil
}

// AcquireLease gives one process exclusive execution rights for an active run.
func (s *Store) AcquireLease(ctx context.Context, id, owner string) (bool, error) {
	if s.readOnly || strings.TrimSpace(owner) == "" {
		return false, errors.New("a writable store and lease owner are required")
	}
	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `UPDATE runs SET lease_owner=?, lease_expires_at=?
WHERE id=? AND stage IN ('checking','observing','finalizing')
AND (lease_owner IS NULL OR lease_owner='' OR lease_expires_at IS NULL OR lease_expires_at < ? OR lease_owner=?)`,
		owner, formatTime(now.Add(LeaseTTL)), id, formatTime(now), owner)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

func (s *Store) RenewLease(ctx context.Context, id, owner string) error {
	result, err := s.db.ExecContext(ctx, `UPDATE runs SET lease_expires_at=? WHERE id=? AND lease_owner=? AND stage IN ('checking','observing','finalizing')`, formatTime(time.Now().UTC().Add(LeaseTTL)), id, owner)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count != 1 {
		return errors.New("run lease is no longer owned by this process")
	}
	return s.secureFiles()
}

func (s *Store) ReleaseLease(ctx context.Context, id, owner string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE runs SET lease_owner=NULL, lease_expires_at=NULL WHERE id=? AND lease_owner=?`, id, owner)
	return err
}

func (s *Store) Create(ctx context.Context, releaseID, planSHA string, plan guard.Plan) (RunRecord, error) {
	id, err := randomID()
	if err != nil {
		return RunRecord{}, err
	}
	planJSON, err := json.Marshal(plan)
	if err != nil {
		return RunRecord{}, err
	}
	now := time.Now().UTC()
	record := RunRecord{ID: id, ReleaseID: releaseID, PlanSHA256: planSHA, Stage: "checking", Plan: plan, StartedAt: now, UpdatedAt: now}
	_, err = s.db.ExecContext(ctx, `INSERT INTO runs(id, release_id, plan_sha256, stage, plan_json, started_at, updated_at) VALUES(?,?,?,?,?,?,?)`, id, releaseID, planSHA, record.Stage, string(planJSON), formatTime(now), formatTime(now))
	if err != nil {
		if existing, findErr := s.FindActive(ctx, releaseID, planSHA); findErr == nil {
			return existing, nil
		}
		return record, err
	}
	if err = s.secureFiles(); err != nil {
		return record, err
	}
	return record, nil
}

func (s *Store) Update(ctx context.Context, id, stage string, report guard.Report) error {
	return s.update(ctx, id, "", stage, report)
}

func (s *Store) UpdateOwned(ctx context.Context, id, owner, stage string, report guard.Report) error {
	if strings.TrimSpace(owner) == "" {
		return errors.New("lease owner is required")
	}
	return s.update(ctx, id, owner, stage, report)
}

func (s *Store) update(ctx context.Context, id, owner, stage string, report guard.Report) error {
	if stage != "checking" && stage != "observing" && stage != "finalizing" && stage != "completed" {
		return fmt.Errorf("invalid run stage %q", stage)
	}
	reportJSON, err := json.Marshal(report)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	var completed any
	if stage == "completed" {
		completed = formatTime(now)
	}
	var current string
	var currentOwner, leaseExpires sql.NullString
	if err := s.db.QueryRowContext(ctx, `SELECT stage, lease_owner, lease_expires_at FROM runs WHERE id=?`, id).Scan(&current, &currentOwner, &leaseExpires); err != nil {
		return err
	}
	if owner == "" {
		if currentOwner.Valid && currentOwner.String != "" {
			return errors.New("run is owned by another process")
		}
	} else {
		if !currentOwner.Valid || currentOwner.String != owner {
			return errors.New("run lease is no longer owned by this process")
		}
		expires, parseErr := parseTime("lease_expires_at", leaseExpires.String)
		if parseErr != nil {
			return parseErr
		}
		if !expires.After(now) {
			return errors.New("run lease expired")
		}
	}
	allowed := map[string]map[string]bool{
		"checking":   {"checking": true, "observing": true, "finalizing": true},
		"observing":  {"observing": true, "finalizing": true},
		"finalizing": {"finalizing": true, "completed": true},
	}
	if !allowed[current][stage] {
		return fmt.Errorf("invalid run stage transition %s -> %s", current, stage)
	}
	result, err := s.db.ExecContext(ctx, `UPDATE runs SET stage=?, report_json=?, updated_at=?, completed_at=?, lease_owner=CASE WHEN ?='completed' THEN NULL ELSE lease_owner END, lease_expires_at=CASE WHEN ?='completed' THEN NULL ELSE lease_expires_at END WHERE id=? AND stage=? AND COALESCE(lease_owner,'')=?`, stage, string(reportJSON), formatTime(now), completed, stage, stage, id, current, owner)
	if err != nil {
		return err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return errors.New("run stage changed concurrently")
	}
	return s.secureFiles()
}

func (s *Store) Abandon(ctx context.Context, id, reason string) (RunRecord, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" || len(reason) > 2000 {
		return RunRecord{}, errors.New("abandon reason is required and must not exceed 2000 characters")
	}
	record, err := s.Get(ctx, id)
	if err != nil {
		return record, err
	}
	if record.Stage != "checking" && record.Stage != "observing" && record.Stage != "finalizing" {
		return record, fmt.Errorf("run in stage %s cannot be abandoned", record.Stage)
	}
	report := record.Report
	if report == nil {
		report = &guard.Report{SchemaVersion: "releaseguard.report/v1", ReleaseID: record.ReleaseID, Version: record.Plan.Version, Decision: "HOLD", DecisionReason: "run abandoned: " + reason, GeneratedAt: time.Now().UTC(), PlanSHA256: record.PlanSHA256, Manifest: guard.Manifest{ReleaseID: record.ReleaseID, Version: record.Plan.Version, CreatedAt: time.Now().UTC()}, Rollback: record.Plan.Rollback}
	} else {
		report.Decision = "HOLD"
		report.DecisionReason = "run abandoned: " + reason
	}
	reportJSON, err := json.Marshal(report)
	if err != nil {
		return record, err
	}
	now := time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `UPDATE runs SET stage='abandoned', report_json=?, updated_at=?, completed_at=?, lease_owner=NULL, lease_expires_at=NULL WHERE id=? AND stage=?`, string(reportJSON), formatTime(now), formatTime(now), id, record.Stage)
	if err != nil {
		return record, err
	}
	if count, _ := result.RowsAffected(); count == 0 {
		return record, errors.New("run stage changed concurrently")
	}
	if err = s.secureFiles(); err != nil {
		return record, err
	}
	return s.Get(ctx, id)
}

func (s *Store) FindActive(ctx context.Context, releaseID, planSHA string) (RunRecord, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, release_id, plan_sha256, stage, plan_json, COALESCE(report_json,''), started_at, updated_at, completed_at FROM runs WHERE release_id=? AND plan_sha256=? AND stage IN ('checking','observing','finalizing') ORDER BY updated_at DESC LIMIT 1`, releaseID, planSHA)
	return scanRun(row)
}

func (s *Store) Get(ctx context.Context, id string) (RunRecord, error) {
	return scanRun(s.db.QueryRowContext(ctx, `SELECT id, release_id, plan_sha256, stage, plan_json, COALESCE(report_json,''), started_at, updated_at, completed_at FROM runs WHERE id=?`, id))
}

func (s *Store) List(ctx context.Context, limit int) ([]RunRecord, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id, release_id, plan_sha256, stage, plan_json, COALESCE(report_json,''), started_at, updated_at, completed_at FROM runs ORDER BY updated_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := []RunRecord{}
	for rows.Next() {
		record, err := scanRun(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, record)
	}
	return result, rows.Err()
}

type scanner interface {
	Scan(...any) error
}

func scanRun(row scanner) (RunRecord, error) {
	var record RunRecord
	var planJSON, reportJSON, started, updated string
	var completed sql.NullString
	if err := row.Scan(&record.ID, &record.ReleaseID, &record.PlanSHA256, &record.Stage, &planJSON, &reportJSON, &started, &updated, &completed); err != nil {
		return record, err
	}
	if err := json.Unmarshal([]byte(planJSON), &record.Plan); err != nil {
		return record, err
	}
	if reportJSON != "" {
		var report guard.Report
		if err := json.Unmarshal([]byte(reportJSON), &report); err != nil {
			return record, err
		}
		record.Report = &report
	}
	var err error
	if record.StartedAt, err = parseTime("started_at", started); err != nil {
		return record, err
	}
	if record.UpdatedAt, err = parseTime("updated_at", updated); err != nil {
		return record, err
	}
	if completed.Valid {
		value, err := parseTime("completed_at", completed.String)
		if err != nil {
			return record, err
		}
		record.CompletedAt = &value
	}
	return record, nil
}

func randomID() (string, error) {
	value := make([]byte, 12)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return "run_" + hex.EncodeToString(value), nil
}

func formatTime(value time.Time) string { return value.UTC().Format(time.RFC3339Nano) }

func parseTime(field, value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse run %s: %w", field, err)
	}
	return parsed, nil
}
