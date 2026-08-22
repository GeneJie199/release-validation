package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/GeneJie199/release-validation/internal/guard"
	"github.com/GeneJie199/release-validation/internal/runstore"
	webui "github.com/GeneJie199/release-validation/internal/web"
)

var version = "dev"

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "init":
		initialize(os.Args[2:])
	case "doctor":
		doctor(os.Args[2:])
	case "check":
		check(os.Args[2:])
	case "serve":
		serve(os.Args[2:])
	case "confirm":
		confirm(os.Args[2:])
	case "runs":
		runs(os.Args[2:])
	case "version", "--version":
		fmt.Println(version)
	default:
		usage()
		os.Exit(2)
	}
}
func usage() {
	fmt.Fprintln(os.Stderr, "Usage: releaseguard init [--repository .] [--version VERSION] [--out release-plan.json]\n       releaseguard check --plan release-plan.json [--out report.json] [--state releaseguard-runs.db] [--force]\n       releaseguard doctor [--report release-report.json] [--state releaseguard-runs.db] [--url http://127.0.0.1:8771]\n       releaseguard runs [--state releaseguard-runs.db] [--limit 100] [--abandon RUN_ID --reason TEXT]\n       releaseguard confirm --report release-report.json --decision GO --by NAME [--state releaseguard-runs.db] [--out approval.json]\n       releaseguard serve --report release-report.json [--state releaseguard-runs.db] [--addr 127.0.0.1:8771] [--open] [--allow-remote]")
}

func runs(args []string) {
	f := flag.NewFlagSet("runs", flag.ExitOnError)
	statePath := f.String("state", "releaseguard-runs.db", "persistent run database")
	limit := f.Int("limit", 100, "maximum runs to list")
	abandon := f.String("abandon", "", "mark one interrupted active run as abandoned")
	reason := f.String("reason", "", "required audit reason for --abandon")
	_ = f.Parse(args)
	var store *runstore.Store
	var err error
	if *abandon != "" {
		store, err = runstore.Open(*statePath)
	} else {
		store, err = runstore.OpenReadOnly(*statePath)
	}
	if err != nil {
		log.Fatal(err)
	}
	defer store.Close()
	if *abandon != "" {
		record, err := store.Abandon(context.Background(), *abandon, *reason)
		if err != nil {
			log.Fatal(err)
		}
		encoder := json.NewEncoder(os.Stdout)
		encoder.SetIndent("", "  ")
		_ = encoder.Encode(record)
		return
	}
	items, err := store.List(context.Background(), *limit)
	if err != nil {
		log.Fatal(err)
	}
	type summary struct {
		ID          string     `json:"id"`
		ReleaseID   string     `json:"release_id"`
		Version     string     `json:"version"`
		Stage       string     `json:"stage"`
		Decision    string     `json:"decision,omitempty"`
		Samples     int        `json:"samples"`
		StartedAt   time.Time  `json:"started_at"`
		UpdatedAt   time.Time  `json:"updated_at"`
		CompletedAt *time.Time `json:"completed_at,omitempty"`
	}
	out := make([]summary, 0, len(items))
	for _, item := range items {
		value := summary{ID: item.ID, ReleaseID: item.ReleaseID, Version: item.Plan.Version, Stage: item.Stage, StartedAt: item.StartedAt, UpdatedAt: item.UpdatedAt, CompletedAt: item.CompletedAt}
		if item.Report != nil {
			value.Decision = item.Report.Decision
			if item.Report.Observation != nil {
				value.Samples = len(item.Report.Observation.Samples)
			}
		}
		out = append(out, value)
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	_ = encoder.Encode(out)
}
func check(args []string) {
	f := flag.NewFlagSet("check", flag.ExitOnError)
	plan := f.String("plan", "release-plan.json", "release plan JSON")
	out := f.String("out", "release-report.json", "report output")
	statePath := f.String("state", "releaseguard-runs.db", "persistent run database (empty disables resume)")
	force := f.Bool("force", false, "replace an existing report (disabled by default for immutability)")
	_ = f.Parse(args)
	p, b, e := guard.LoadPlan(*plan)
	if e != nil {
		log.Fatal(e)
	}
	signalCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	ctx, cancelRun := context.WithCancel(signalCtx)
	defer stop()
	defer cancelRun()
	var store *runstore.Store
	var record runstore.RunRecord
	var lease *runLease
	if *statePath != "" {
		store, e = runstore.Open(*statePath)
		if e != nil {
			log.Fatal(e)
		}
		digest := fmt.Sprintf("%x", sha256.Sum256(b))
		record, e = store.FindActive(ctx, p.ReleaseID, digest)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			_ = store.Close()
			log.Fatal(e)
		}
		if errors.Is(e, sql.ErrNoRows) {
			record, e = store.Create(ctx, p.ReleaseID, digest, p)
			if e != nil {
				_ = store.Close()
				log.Fatal(e)
			}
		}
		lease, e = acquireRunLease(ctx, store, record.ID, cancelRun)
		if e != nil {
			_ = store.Close()
			log.Fatal(e)
		}
		fmt.Fprintf(os.Stderr, "releaseguard run %s (%s)\n", record.ID, record.Stage)
	}
	closeStore := func(releaseLease bool) error {
		var leaseErr error
		if lease != nil {
			leaseErr = lease.close(releaseLease)
		}
		if store != nil {
			return errors.Join(leaseErr, store.Close())
		}
		return leaseErr
	}
	progress := func(stage string, report guard.Report) error {
		if store == nil {
			return nil
		}
		saveCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if stage == "completed" {
			stage = "finalizing"
		}
		return store.UpdateOwned(saveCtx, record.ID, lease.owner, stage, report)
	}
	var r guard.Report
	if record.Report != nil && record.Stage == "observing" {
		r = guard.ResumeObservation(ctx, p, *record.Report, progress)
	} else if record.Report != nil && record.Stage == "finalizing" {
		r = *record.Report
	} else {
		r = guard.RunWithProgress(ctx, p, b, progress)
	}
	if ctx.Err() != nil {
		if err := closeStore(true); err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(r)
		os.Exit(3)
	}
	if r.Observation != nil && r.Observation.Status == "observing" {
		if err := closeStore(true); err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(r)
		os.Exit(3)
	}
	if e = guard.WriteReportWithOverwrite(*out, r, *force); e != nil && !guard.ArtifactMatches(*out, r) {
		_ = closeStore(true)
		log.Fatal(e)
	}
	if store != nil {
		saveCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		e = store.UpdateOwned(saveCtx, record.ID, lease.owner, "completed", r)
		cancel()
		if closeErr := closeStore(e != nil); e == nil {
			e = closeErr
		}
		if e != nil {
			log.Fatal(e)
		}
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	_ = enc.Encode(r)
	if r.Decision != "GO" {
		os.Exit(1)
	}
}

func confirm(args []string) {
	f := flag.NewFlagSet("confirm", flag.ExitOnError)
	report := f.String("report", "release-report.json", "immutable validation report")
	decision := f.String("decision", "", "human decision: GO, HOLD, or NO-GO")
	by := f.String("by", "", "approver identity")
	note := f.String("note", "", "approval note")
	out := f.String("out", "", "approval output (default: a digest-bound sidecar next to REPORT)")
	statePath := f.String("state", "releaseguard-runs.db", "persistent run database used to reject approval during an active run (empty disables)")
	_ = f.Parse(args)
	if *out == "" {
		resolved, err := guard.ApprovalOutputPath(*report)
		if err != nil {
			log.Fatal(err)
		}
		*out = resolved
	}
	loaded, _, err := guard.LoadReport(*report)
	if err != nil {
		log.Fatal(err)
	}
	if *statePath != "" {
		if _, statErr := os.Stat(*statePath); statErr == nil {
			store, openErr := runstore.OpenReadOnly(*statePath)
			if openErr != nil {
				log.Fatal(openErr)
			}
			_, activeErr := store.FindActive(context.Background(), loaded.ReleaseID, loaded.PlanSHA256)
			closeErr := store.Close()
			if activeErr == nil {
				log.Fatal("approval is blocked while a matching validation run is active")
			}
			if !errors.Is(activeErr, sql.ErrNoRows) {
				log.Fatal(activeErr)
			}
			if closeErr != nil {
				log.Fatal(closeErr)
			}
		} else if !errors.Is(statErr, os.ErrNotExist) {
			log.Fatal(statErr)
		}
	}
	if err := guard.CreateApproval(*report, *out, *decision, *by, *note); err != nil {
		log.Fatal(err)
	}
	fmt.Println(*out)
}
func serve(args []string) {
	f := flag.NewFlagSet("serve", flag.ExitOnError)
	report := f.String("report", "release-report.json", "report JSON")
	addr := f.String("addr", "127.0.0.1:8771", "listen address")
	allowRemote := f.Bool("allow-remote", false, "allow binding to a non-loopback address")
	open := f.Bool("open", false, "open the decision console in the default browser")
	statePath := f.String("state", "releaseguard-runs.db", "persistent run database for live status (empty disables)")
	_ = f.Parse(args)
	if *allowRemote {
		log.Print("WARNING: remote report serving has no built-in TLS or viewer authentication; use only behind an authenticated TLS reverse proxy and restrictive network policy")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if *open {
		go openWhenReady(ctx, "http://"+*addr+"/api/v1/health", "http://"+*addr+"/")
	}
	if e := webui.ServeWithState(ctx, *addr, *report, *allowRemote, os.Getenv("RELEASEGUARD_APPROVAL_TOKEN"), *statePath); e != nil {
		log.Fatal(e)
	}
}
