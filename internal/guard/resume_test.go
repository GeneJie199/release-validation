package guard

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestObservationCanResumeWithoutRepeatingPrechecks(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/nodes/node-a":
			_, _ = fmt.Fprintf(w, `{"summary":{"health":"healthy","observed_at":%q},"report":{"labels":{"version":"1.2.3"}}}`, time.Now().UTC().Format(time.RFC3339))
		case "/api/v1/alerts":
			_, _ = w.Write([]byte(`[]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	plan := Plan{ReleaseID: "resume-release", Version: "1.2.3", Fleet: &FleetPolicy{CenterURL: server.URL, Nodes: []string{"node-a"}}, Observation: &ObservationPlan{Seconds: 3, PollSeconds: 1}, RecoveryChecks: []Check{{Name: "previous fleet reachable", Type: "http", URL: server.URL + "/api/v1/nodes/node-a"}}, Rollback: []string{"undo"}}
	ctx, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
	defer cancel()
	var checkpoint Report
	report := RunWithProgress(ctx, plan, []byte("plan"), func(_ string, value Report) error { checkpoint = value; return nil })
	if report.Observation == nil || report.Observation.Status != "observing" || len(report.Observation.Samples) < 1 || checkpoint.Observation == nil {
		t.Fatalf("interrupted report=%+v checkpoint=%+v", report.Observation, checkpoint.Observation)
	}
	precheckCount := len(report.Results)
	report = ResumeObservation(context.Background(), plan, checkpoint, nil)
	if report.Observation.Status != "completed" || report.Decision != "GO" || len(report.Results) != precheckCount+1 {
		t.Fatalf("resumed report=%+v", report)
	}
}
