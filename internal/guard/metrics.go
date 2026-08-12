package guard

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

func captureMetricBaseline(ctx context.Context, fleet FleetPolicy, policy MetricPolicy, end time.Time) (MetricEvidence, Result) {
	required := policy.Required == nil || *policy.Required
	evidence := MetricEvidence{Name: policy.Name, Metric: policy.Metric, Node: policy.Node, Aggregate: policy.Aggregate, SeriesReduce: policy.SeriesReduce, Direction: policy.Direction}
	result := Result{Name: "Metric baseline: " + policy.Name, Type: "metric_baseline", Status: "fail", Required: required, Evidence: map[string]any{}}
	start := end.Add(-time.Duration(policy.BaselineSeconds) * time.Second)
	window, err := queryMetricWindow(ctx, fleet, policy, start, end)
	if err != nil {
		result.Summary = err.Error()
		result.Evidence["metric"] = evidence
		return evidence, result
	}
	evidence.Before = window
	evidence.Summary = fmt.Sprintf("captured %d samples across %d series", window.Samples, window.Series)
	result.Status = "pass"
	result.Summary = evidence.Summary
	result.Evidence["metric"] = evidence
	return evidence, result
}

func compareMetricAfter(ctx context.Context, fleet FleetPolicy, policy MetricPolicy, evidence MetricEvidence, start, end time.Time) (MetricEvidence, Result) {
	required := policy.Required == nil || *policy.Required
	result := Result{Name: "Metric regression: " + policy.Name, Type: "metric_comparison", Status: "fail", Required: required, Evidence: map[string]any{}}
	after, err := queryMetricWindow(ctx, fleet, policy, start, end)
	if err != nil {
		evidence.Summary = err.Error()
		result.Summary = evidence.Summary
		result.Evidence["metric"] = evidence
		return evidence, result
	}
	evidence.After = after
	if evidence.Before == nil {
		evidence.Summary = "metric baseline is missing"
		result.Summary = evidence.Summary
		result.Evidence["metric"] = evidence
		return evidence, result
	}
	beforeValue := evidence.Before.Value
	delta := after.Value - beforeValue
	if policy.Direction == "higher" {
		delta = beforeValue - after.Value
	}
	if math.Abs(beforeValue) < 1e-12 {
		if delta > 0 {
			evidence.RegressionPercent = 100
		}
	} else {
		evidence.RegressionPercent = delta / math.Abs(beforeValue) * 100
	}
	reasons := []string{}
	if policy.MaxRegressionPercent != nil && evidence.RegressionPercent > *policy.MaxRegressionPercent {
		reasons = append(reasons, fmt.Sprintf("regression %.2f%% exceeds %.2f%%", evidence.RegressionPercent, *policy.MaxRegressionPercent))
	}
	if policy.MaxValue != nil && after.Value > *policy.MaxValue {
		reasons = append(reasons, fmt.Sprintf("value %.4g exceeds maximum %.4g", after.Value, *policy.MaxValue))
	}
	if policy.MinValue != nil && after.Value < *policy.MinValue {
		reasons = append(reasons, fmt.Sprintf("value %.4g is below minimum %.4g", after.Value, *policy.MinValue))
	}
	if len(reasons) == 0 {
		evidence.Pass = true
		evidence.Summary = fmt.Sprintf("before %.4g, after %.4g, regression %.2f%%", beforeValue, after.Value, evidence.RegressionPercent)
		result.Status = "pass"
		result.Summary = evidence.Summary
	} else {
		evidence.Summary = strings.Join(reasons, "; ")
		result.Summary = evidence.Summary
	}
	result.Evidence["metric"] = evidence
	return evidence, result
}

func queryMetricWindow(ctx context.Context, fleet FleetPolicy, policy MetricPolicy, start, end time.Time) (*MetricWindow, error) {
	if !start.Before(end) {
		return nil, errors.New("metric query window must be positive")
	}
	values := url.Values{}
	values.Set("metric", policy.Metric)
	values.Set("start", fmt.Sprint(start.UnixMilli()))
	values.Set("end", fmt.Sprint(end.UnixMilli()))
	values.Set("step", end.Sub(start).Round(time.Second).String())
	values.Set("aggregate", policy.Aggregate)
	if policy.Node != "" {
		values.Set("node", policy.Node)
	}
	if policy.Source != "" {
		values.Set("source", policy.Source)
	}
	keys := make([]string, 0, len(policy.Labels))
	for key := range policy.Labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		values.Add("label", key+"="+policy.Labels[key])
	}
	requestURL := strings.TrimRight(fleet.CenterURL, "/") + "/api/v1/telemetry/query?" + values.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, err
	}
	if fleet.TokenEnv != "" {
		token := os.Getenv(fleet.TokenEnv)
		if token == "" {
			return nil, fmt.Errorf("fleet token environment variable %s is empty", fleet.TokenEnv)
		}
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := (&http.Client{Timeout: 15 * time.Second}).Do(req)
	if err != nil {
		return nil, fmt.Errorf("query FleetScope metric %s: %w", policy.Metric, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, fmt.Errorf("FleetScope metric %s returned %s: %s", policy.Metric, resp.Status, strings.TrimSpace(string(body)))
	}
	var response struct {
		Series []struct {
			Points []struct {
				Value float64 `json:"value"`
			} `json:"points"`
		} `json:"series"`
	}
	decoder := json.NewDecoder(io.LimitReader(resp.Body, 4<<20))
	if err := decoder.Decode(&response); err != nil {
		return nil, fmt.Errorf("decode FleetScope metric %s: %w", policy.Metric, err)
	}
	window := &MetricWindow{StartMS: start.UnixMilli(), EndMS: end.UnixMilli(), Series: len(response.Series)}
	valuesSeen := []float64{}
	seriesValues := []float64{}
	for _, series := range response.Series {
		current := make([]float64, 0, len(series.Points))
		for _, point := range series.Points {
			valuesSeen = append(valuesSeen, point.Value)
			current = append(current, point.Value)
		}
		if len(current) > 0 {
			seriesValues = append(seriesValues, reduceMetricValues(current, policy.Aggregate))
		}
	}
	if len(valuesSeen) == 0 {
		return nil, fmt.Errorf("FleetScope metric %s returned no samples", policy.Metric)
	}
	window.Samples = len(valuesSeen)
	window.Minimum, window.Maximum = valuesSeen[0], valuesSeen[0]
	for _, value := range valuesSeen {
		window.Last = value
		window.Minimum = math.Min(window.Minimum, value)
		window.Maximum = math.Max(window.Maximum, value)
	}
	window.Value = reduceMetricValues(seriesValues, policy.SeriesReduce)
	return window, nil
}

func reduceMetricValues(values []float64, reducer string) float64 {
	if len(values) == 0 {
		return 0
	}
	result := values[0]
	switch reducer {
	case "min":
		for _, value := range values[1:] {
			result = math.Min(result, value)
		}
	case "max":
		for _, value := range values[1:] {
			result = math.Max(result, value)
		}
	case "sum":
		for _, value := range values[1:] {
			result += value
		}
	case "last":
		result = values[len(values)-1]
	default:
		for _, value := range values[1:] {
			result += value
		}
		result /= float64(len(values))
	}
	return result
}
