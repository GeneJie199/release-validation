package web

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/GeneJie199/release-validation/internal/guard"
	"github.com/GeneJie199/release-validation/internal/runstore"
)

//go:embed static/*
var assets embed.FS

func Handler(reportPath string) http.Handler {
	return HandlerWithApprovalToken(reportPath, "")
}

func HandlerWithApprovalToken(reportPath, approvalToken string) http.Handler {
	return handler(reportPath, approvalToken, nil)
}

func handler(reportPath, approvalToken string, runs *runstore.Store) http.Handler {
	mux := http.NewServeMux()
	targetIdentity := func() (string, string) {
		served, _, err := guard.LoadReport(reportPath)
		if err != nil {
			return "", ""
		}
		return served.ReleaseID, served.PlanSHA256
	}
	authFailures := newFailureLimiter(5, time.Minute)
	latestActive := func(ctx context.Context) (*runstore.RunRecord, error) {
		if runs == nil {
			return nil, nil
		}
		items, err := runs.List(ctx, 100)
		if err != nil {
			return nil, err
		}
		for index := range items {
			active := items[index].Stage == "checking" || items[index].Stage == "observing" || items[index].Stage == "finalizing"
			releaseID, planSHA := targetIdentity()
			matches := releaseID == "" || (items[index].ReleaseID == releaseID && (planSHA == "" || items[index].PlanSHA256 == planSHA))
			if active && matches {
				return &items[index], nil
			}
		}
		return nil, nil
	}
	mux.HandleFunc("GET /api/v1/health", func(w http.ResponseWriter, _ *http.Request) { write(w, map[string]string{"status": "ok"}) })
	mux.HandleFunc("GET /api/v1/capabilities", func(w http.ResponseWriter, r *http.Request) {
		active, err := latestActive(r.Context())
		write(w, map[string]bool{"approval_write": approvalToken != "" && err == nil && active == nil, "live_runs": runs != nil, "approval_blocked_by_active_run": active != nil})
	})
	mux.HandleFunc("GET /api/v1/report", func(w http.ResponseWriter, r *http.Request) {
		if runs != nil {
			active, err := latestActive(r.Context())
			if err == nil && active != nil && active.Report != nil {
				write(w, active.Report)
				return
			}
		}
		b, e := os.ReadFile(reportPath)
		if e != nil {
			log.Printf("read report: %v", e)
			http.Error(w, "report not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(b)
	})
	if runs != nil {
		mux.HandleFunc("GET /api/v1/runs", func(w http.ResponseWriter, r *http.Request) {
			items, err := runs.List(r.Context(), 100)
			if err != nil {
				log.Printf("list runs: %v", err)
				http.Error(w, "run history is unavailable", http.StatusInternalServerError)
				return
			}
			type summary struct {
				ID          string     `json:"id"`
				ReleaseID   string     `json:"release_id"`
				Stage       string     `json:"stage"`
				Decision    string     `json:"decision,omitempty"`
				Samples     int        `json:"samples"`
				StartedAt   time.Time  `json:"started_at"`
				UpdatedAt   time.Time  `json:"updated_at"`
				CompletedAt *time.Time `json:"completed_at,omitempty"`
			}
			out := make([]summary, 0, len(items))
			for _, item := range items {
				value := summary{ID: item.ID, ReleaseID: item.ReleaseID, Stage: item.Stage, StartedAt: item.StartedAt, UpdatedAt: item.UpdatedAt, CompletedAt: item.CompletedAt}
				if item.Report != nil {
					value.Decision = item.Report.Decision
					if item.Report.Observation != nil {
						value.Samples = len(item.Report.Observation.Samples)
					}
				}
				out = append(out, value)
			}
			write(w, out)
		})
		mux.HandleFunc("GET /api/v1/runs/{id}", func(w http.ResponseWriter, r *http.Request) {
			record, err := runs.Get(r.Context(), r.PathValue("id"))
			if err != nil || record.Report == nil {
				http.Error(w, "run report not found", http.StatusNotFound)
				return
			}
			write(w, record.Report)
		})
	}
	mux.HandleFunc("GET /api/v1/approval", func(w http.ResponseWriter, _ *http.Request) {
		approval, _, err := guard.LoadBoundApproval(reportPath)
		if err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				log.Printf("ignore unbound approval: %v", err)
			}
			http.Error(w, "approval not recorded", http.StatusNotFound)
			return
		}
		write(w, approval)
	})
	mux.HandleFunc("POST /api/v1/approval", func(w http.ResponseWriter, r *http.Request) {
		if approvalToken == "" {
			http.Error(w, "approval writing is disabled", http.StatusMethodNotAllowed)
			return
		}
		client := clientAddress(r)
		if !authFailures.Allow(client) {
			http.Error(w, "too many failed approval attempts; retry later", http.StatusTooManyRequests)
			return
		}
		gotHash := sha256.Sum256([]byte(r.Header.Get("Authorization")))
		wantHash := sha256.Sum256([]byte("Bearer " + approvalToken))
		if subtle.ConstantTimeCompare(gotHash[:], wantHash[:]) != 1 {
			authFailures.Fail(client)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		authFailures.Success(client)
		if active, err := latestActive(r.Context()); err != nil {
			http.Error(w, "cannot verify active validation state", http.StatusServiceUnavailable)
			return
		} else if active != nil {
			http.Error(w, "approval is blocked while a validation run is active", http.StatusConflict)
			return
		}
		var in struct {
			Decision   string `json:"decision"`
			ApprovedBy string `json:"approved_by"`
			Note       string `json:"note"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 32<<10)
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			http.Error(w, "invalid approval JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		if err := dec.Decode(&struct{}{}); err != io.EOF {
			if err == nil {
				err = errors.New("multiple JSON values are not allowed")
			}
			http.Error(w, "invalid approval JSON: "+err.Error(), http.StatusBadRequest)
			return
		}
		out, err := guard.ApprovalOutputPath(reportPath)
		if err != nil {
			log.Printf("resolve approval output: %v", err)
			http.Error(w, "cannot bind approval to the current report", http.StatusInternalServerError)
			return
		}
		if err := guard.CreateApproval(reportPath, out, in.Decision, strings.TrimSpace(in.ApprovedBy), strings.TrimSpace(in.Note)); err != nil {
			log.Printf("create approval: %v", err)
			status := http.StatusBadRequest
			if strings.Contains(err.Error(), "refusing to overwrite") {
				status = http.StatusConflict
			}
			message := "approval request is invalid"
			if status == http.StatusConflict {
				message = "approval is already recorded"
			}
			http.Error(w, message, status)
			return
		}
		approval, b, err := guard.LoadBoundApproval(reportPath)
		if err != nil {
			log.Printf("read created approval: %v", err)
			http.Error(w, "approval was recorded but cannot be read", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.WriteHeader(http.StatusCreated)
		if len(b) > 0 {
			_, _ = w.Write(b)
			return
		}
		_ = json.NewEncoder(w).Encode(approval)
	})
	sub, _ := fs.Sub(assets, "static")
	static := http.FileServer(http.FS(sub))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		static.ServeHTTP(w, r)
	})
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self'; script-src 'self'; connect-src 'self'")
		w.Header().Set("Cache-Control", "no-store")
		mux.ServeHTTP(w, r)
	})
}
func Serve(ctx context.Context, addr, report string) error {
	return ServeWithOptions(ctx, addr, report, false)
}

func ServeWithOptions(ctx context.Context, addr, report string, allowRemote bool) error {
	return ServeWithApproval(ctx, addr, report, allowRemote, os.Getenv("RELEASEGUARD_APPROVAL_TOKEN"))
}

func ServeWithApproval(ctx context.Context, addr, report string, allowRemote bool, approvalToken string) error {
	return ServeWithState(ctx, addr, report, allowRemote, approvalToken, "")
}

func ServeWithState(ctx context.Context, addr, report string, allowRemote bool, approvalToken, statePath string) error {
	if approvalToken != "" && len(approvalToken) < 24 {
		return errors.New("approval token must contain at least 24 characters")
	}
	if !allowRemote {
		host, _, err := net.SplitHostPort(addr)
		if err != nil {
			return err
		}
		if host == "" {
			return errors.New("refusing to listen on a non-loopback address (use --allow-remote to override)")
		}
		ips, err := net.LookupIP(host)
		if err != nil || len(ips) == 0 {
			return fmt.Errorf("resolve listen address %q: %w", host, err)
		}
		for _, ip := range ips {
			if !ip.IsLoopback() {
				return errors.New("refusing to listen on a non-loopback address (use --allow-remote to override)")
			}
		}
	}
	var runs *runstore.Store
	if statePath != "" {
		var err error
		runs, err = runstore.OpenReadOnly(statePath)
		if err != nil {
			return err
		}
		defer runs.Close()
	}
	s := &http.Server{Addr: addr, Handler: handler(report, approvalToken, runs), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 1 << 20}
	done := make(chan error, 1)
	go func() { done <- s.ListenAndServe() }()
	select {
	case <-ctx.Done():
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return s.Shutdown(c)
	case e := <-done:
		if errors.Is(e, http.ErrServerClosed) {
			return nil
		}
		return e
	}
}
func write(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

type failureWindow struct {
	count int
	until time.Time
}

type failureLimiter struct {
	mu      sync.Mutex
	entries map[string]failureWindow
	limit   int
	window  time.Duration
}

func newFailureLimiter(limit int, window time.Duration) *failureLimiter {
	return &failureLimiter{entries: make(map[string]failureWindow), limit: limit, window: window}
}

func (l *failureLimiter) Allow(client string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry := l.entries[client]
	if !entry.until.IsZero() && time.Now().After(entry.until) {
		delete(l.entries, client)
		return true
	}
	return entry.count < l.limit
}

func (l *failureLimiter) Fail(client string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	entry := l.entries[client]
	if entry.until.IsZero() || time.Now().After(entry.until) {
		entry = failureWindow{until: time.Now().Add(l.window)}
	}
	entry.count++
	l.entries[client] = entry
}

func (l *failureLimiter) Success(client string) {
	l.mu.Lock()
	delete(l.entries, client)
	l.mu.Unlock()
}

func clientAddress(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil {
		return host
	}
	return r.RemoteAddr
}
