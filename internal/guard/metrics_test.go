package guard

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func floatPointer(value float64) *float64 { return &value }

func TestMetricBaselineAndRegressionComparison(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/telemetry/query" || r.URL.Query().Get("metric") != "request_latency_ms" || r.URL.Query().Get("node") != "node-a" {
			t.Fatalf("unexpected request %s", r.URL.String())
		}
		value := 100.0
		if requests.Add(1) > 1 {
			value = 135
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"start_ms":1,"end_ms":2,"series":[{"points":[{"value":%g}]}]}`, value)
	}))
	defer server.Close()

	policy := MetricPolicy{Name: "API latency", Metric: "request_latency_ms", Node: "node-a", Aggregate: "avg", BaselineSeconds: 300, Direction: "lower", MaxRegressionPercent: floatPointer(20)}
	fleet := FleetPolicy{CenterURL: server.URL}
	baseline, result := captureMetricBaseline(context.Background(), fleet, policy, time.Now().UTC())
	if result.Status != "pass" || baseline.Before == nil || baseline.Before.Value != 100 {
		t.Fatalf("baseline=%+v result=%+v", baseline, result)
	}
	compared, result := compareMetricAfter(context.Background(), fleet, policy, baseline, time.Now().Add(-time.Minute), time.Now())
	if result.Status != "fail" || compared.After == nil || compared.RegressionPercent != 35 || compared.Pass {
		t.Fatalf("comparison=%+v result=%+v", compared, result)
	}
}

func TestMetricComparisonHigherIsBetter(t *testing.T) {
	baseline := MetricEvidence{Before: &MetricWindow{Value: 99}}
	policy := MetricPolicy{Name: "success rate", Metric: "success_rate", Aggregate: "avg", Direction: "higher", MinValue: floatPointer(98)}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"series":[{"points":[{"value":98.5}]}]}`))
	}))
	defer server.Close()
	compared, result := compareMetricAfter(context.Background(), FleetPolicy{CenterURL: server.URL}, policy, baseline, time.Now().Add(-time.Minute), time.Now())
	if result.Status != "pass" || !compared.Pass || compared.RegressionPercent <= 0 {
		t.Fatalf("comparison=%+v result=%+v", compared, result)
	}
}
