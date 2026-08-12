package guard

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"runtime"
	"sort"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	"gopkg.in/yaml.v3"
)

func runCheck(parent context.Context, c Check) Result {
	start := time.Now()
	required := c.Required == nil || *c.Required
	r := Result{Name: c.Name, Type: c.Type, Status: "fail", Required: required, Evidence: map[string]any{}}
	if r.Name == "" {
		r.Name = c.Type + " check"
	}
	timeout := time.Duration(c.TimeoutSecs) * time.Second
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	var err error
	switch c.Type {
	case "command", "playwright":
		err = commandCheck(ctx, c, &r)
	case "http":
		err = httpCheck(ctx, c, &r)
	case "file":
		err = fileCheck(c, &r)
	case "json":
		err = jsonCheck(c, &r)
	case "sql":
		err = sqlCheck(ctx, c, &r)
	case "env":
		err = envCheck(c, &r)
	case "compose":
		err = composeCheck(c, &r)
	default:
		err = fmt.Errorf("unsupported check type %q", c.Type)
	}
	if err == nil {
		r.Status = "pass"
		r.Summary = "check passed"
	} else {
		r.Summary = err.Error()
	}
	r.DurationMS = time.Since(start).Milliseconds()
	return r
}

func envCheck(c Check, r *Result) error {
	before, err := readDotEnv(c.BeforePath)
	if err != nil {
		return fmt.Errorf("before env: %w", err)
	}
	after, err := readDotEnv(c.AfterPath)
	if err != nil {
		return fmt.Errorf("after env: %w", err)
	}
	return compareConfiguration(before, after, c, r)
}

func readDotEnv(file string) (map[string]any, error) {
	b, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	values := map[string]any{}
	for index, raw := range strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		key, value, ok := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !ok || key == "" {
			return nil, fmt.Errorf("line %d is not KEY=VALUE", index+1)
		}
		if _, exists := values[key]; exists {
			return nil, fmt.Errorf("line %d duplicates %s", index+1, key)
		}
		values[key] = strings.Trim(strings.TrimSpace(value), "\"'")
	}
	return values, nil
}

func composeCheck(c Check, r *Result) error {
	read := func(file string) (map[string]any, error) {
		b, err := os.ReadFile(file)
		if err != nil {
			return nil, err
		}
		var document map[string]any
		if err = yaml.Unmarshal(b, &document); err != nil {
			return nil, err
		}
		flat := map[string]any{}
		flattenConfig("", document, flat)
		return flat, nil
	}
	before, err := read(c.BeforePath)
	if err != nil {
		return fmt.Errorf("before compose: %w", err)
	}
	after, err := read(c.AfterPath)
	if err != nil {
		return fmt.Errorf("after compose: %w", err)
	}
	return compareConfiguration(before, after, c, r)
}

func flattenConfig(prefix string, value any, out map[string]any) {
	switch typed := value.(type) {
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			child := key
			if prefix != "" {
				child = prefix + "." + key
			}
			flattenConfig(child, typed[key], out)
		}
	case []any:
		for index, item := range typed {
			flattenConfig(fmt.Sprintf("%s[%d]", prefix, index), item, out)
		}
	default:
		out[prefix] = typed
	}
}

func compareConfiguration(before, after map[string]any, c Check, r *Result) error {
	added, removed, changed := []string{}, []string{}, []string{}
	for key, beforeValue := range before {
		afterValue, ok := after[key]
		if !ok {
			removed = append(removed, key)
			continue
		}
		a, _ := json.Marshal(beforeValue)
		b, _ := json.Marshal(afterValue)
		if !bytes.Equal(a, b) {
			changed = append(changed, key)
		}
	}
	for key := range after {
		if _, ok := before[key]; !ok {
			added = append(added, key)
		}
	}
	sort.Strings(added)
	sort.Strings(removed)
	sort.Strings(changed)
	r.Evidence["before_path"] = c.BeforePath
	r.Evidence["after_path"] = c.AfterPath
	r.Evidence["added_keys"] = added
	r.Evidence["removed_keys"] = removed
	r.Evidence["changed_keys"] = changed
	missing := []string{}
	for _, key := range c.RequiredKeys {
		if _, ok := after[key]; !ok {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		r.Evidence["missing_required_keys"] = missing
		return fmt.Errorf("%d required configuration keys are missing", len(missing))
	}
	unexpected := []string{}
	for _, key := range append(append(added, removed...), changed...) {
		allowed := false
		for _, pattern := range c.AllowedChanges {
			if match, _ := path.Match(pattern, key); match || pattern == key {
				allowed = true
				break
			}
		}
		if !allowed {
			unexpected = append(unexpected, key)
		}
	}
	sort.Strings(unexpected)
	r.Evidence["unexpected_keys"] = unexpected
	if len(unexpected) > 0 {
		return fmt.Errorf("%d undeclared configuration keys changed", len(unexpected))
	}
	return nil
}
func commandCheck(ctx context.Context, c Check, r *Result) error {
	if strings.TrimSpace(c.Command) == "" {
		return errors.New("command is required")
	}
	var cmd *exec.Cmd
	if runtime.GOOS == "windows" {
		cmd = exec.CommandContext(ctx, "powershell", "-NoProfile", "-NonInteractive", "-Command", c.Command)
	} else {
		cmd = exec.CommandContext(ctx, "sh", "-c", c.Command)
	}
	if c.WorkingDir != "" {
		info, err := os.Stat(c.WorkingDir)
		if err != nil || !info.IsDir() {
			return errors.New("working_directory does not exist or is not a directory")
		}
		cmd.Dir = c.WorkingDir
	}
	prepareManagedCommand(cmd)
	output := &boundedTail{maximum: maxCommandEvidenceBytes}
	cmd.Stdout = output
	cmd.Stderr = output
	err := cmd.Run()
	r.Evidence["output"] = output.String()
	r.Evidence["command"] = c.Command
	r.Evidence["working_directory"] = c.WorkingDir
	r.Evidence["output_limit_bytes"] = maxCommandEvidenceBytes
	if err != nil {
		return fmt.Errorf("command failed: %w", err)
	}
	return nil
}
func httpCheck(ctx context.Context, c Check, r *Result) error {
	method := c.Method
	if method == "" {
		method = http.MethodGet
	}
	req, err := http.NewRequestWithContext(ctx, method, c.URL, nil)
	if err != nil {
		return err
	}
	for k, v := range c.Headers {
		req.Header.Set(k, os.ExpandEnv(v))
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	r.Evidence["status"] = resp.StatusCode
	r.Evidence["bytes"] = len(b)
	want := c.WantStatus
	if want == 0 {
		want = 200
	}
	if resp.StatusCode != want {
		return fmt.Errorf("HTTP status %d, want %d", resp.StatusCode, want)
	}
	if c.Contains != "" && !bytes.Contains(b, []byte(c.Contains)) {
		return errors.New("response does not contain expected text")
	}
	return nil
}
func fileCheck(c Check, r *Result) error {
	b, e := os.ReadFile(c.Path)
	if e != nil {
		return e
	}
	s := sha256.Sum256(b)
	got := hex.EncodeToString(s[:])
	r.Evidence["sha256"] = got
	r.Evidence["bytes"] = len(b)
	if c.SHA256 != "" && !strings.EqualFold(c.SHA256, got) {
		return errors.New("SHA256 mismatch")
	}
	return nil
}
func jsonCheck(c Check, r *Result) error {
	b, e := os.ReadFile(c.Path)
	if e != nil {
		return e
	}
	var v any
	if e = json.Unmarshal(b, &v); e != nil {
		return e
	}
	cur := v
	if c.JSONPath != "" {
		for _, p := range strings.Split(c.JSONPath, ".") {
			m, ok := cur.(map[string]any)
			if !ok {
				return fmt.Errorf("%s is not an object", p)
			}
			cur, ok = m[p]
			if !ok {
				return fmt.Errorf("path %s not found", c.JSONPath)
			}
		}
	}
	r.Evidence["value"] = cur
	a, _ := json.Marshal(cur)
	z, _ := json.Marshal(c.Equals)
	if !bytes.Equal(a, z) {
		return fmt.Errorf("value %s, want %s", a, z)
	}
	return nil
}
func sqlCheck(ctx context.Context, c Check, r *Result) error {
	q := strings.TrimSpace(c.Query)
	upper := strings.ToUpper(q)
	if !strings.HasPrefix(upper, "SELECT ") && !strings.HasPrefix(upper, "SHOW ") && !strings.HasPrefix(upper, "WITH ") {
		return errors.New("only read-only SELECT, SHOW, or WITH queries are allowed")
	}
	driver := c.Driver
	if driver == "postgres" {
		driver = "pgx"
	}
	if driver != "pgx" && driver != "mysql" {
		return errors.New("driver must be postgres or mysql")
	}
	if c.DSNEnv == "" {
		return errors.New("dsn_env is required")
	}
	dsn := os.Getenv(c.DSNEnv)
	if dsn == "" {
		return fmt.Errorf("environment variable %s is empty", c.DSNEnv)
	}
	db, err := sql.Open(driver, dsn)
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("start read-only transaction: %w", err)
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, q)
	if err != nil {
		return err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return err
	}
	count := 0
	preview := []map[string]any{}
	for rows.Next() {
		raw := make([]any, len(cols))
		ptr := make([]any, len(cols))
		for i := range raw {
			ptr[i] = &raw[i]
		}
		if err = rows.Scan(ptr...); err != nil {
			return err
		}
		count++
		if count > 10000 {
			return errors.New("query returned more than 10000 rows; narrow the release validation query")
		}
		if c.IncludeSQLPreview && count <= 20 {
			row := map[string]any{}
			for i, k := range cols {
				switch v := raw[i].(type) {
				case []byte:
					row[k] = string(v)
				default:
					row[k] = v
				}
			}
			preview = append(preview, row)
		}
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("finish read-only transaction: %w", err)
	}
	r.Evidence["row_count"] = count
	r.Evidence["columns"] = cols
	if c.IncludeSQLPreview {
		r.Evidence["preview"] = preview
	}
	return nil
}

func checkDrift(path string, expected []string) Result {
	start := time.Now()
	r := Result{Name: "infrastructure drift", Type: "drift", Status: "pass", Required: true, Evidence: map[string]any{}}
	b, e := os.ReadFile(path)
	if e != nil {
		r.Status = "fail"
		r.Summary = e.Error()
		return r
	}
	var d struct {
		Added, Removed []struct {
			ID         string `json:"id"`
			ResourceID string `json:"resourceId"`
		}
		Changed []struct {
			ID         string `json:"id"`
			ResourceID string `json:"resourceId"`
		}
	}
	if e = json.Unmarshal(b, &d); e != nil {
		r.Status = "fail"
		r.Summary = e.Error()
		return r
	}
	ids := []string{}
	appendID := func(id, legacy string) {
		if id == "" {
			id = legacy
		}
		if id != "" {
			ids = append(ids, id)
		}
	}
	for _, x := range d.Added {
		appendID(x.ID, x.ResourceID)
	}
	for _, x := range d.Removed {
		appendID(x.ID, x.ResourceID)
	}
	for _, x := range d.Changed {
		appendID(x.ID, x.ResourceID)
	}
	allowed := map[string]bool{}
	for _, id := range expected {
		allowed[id] = true
	}
	unexpected := []string{}
	for _, id := range ids {
		if !allowed[id] {
			unexpected = append(unexpected, id)
		}
	}
	r.Evidence["changed_resources"] = ids
	r.Evidence["unexpected_resources"] = unexpected
	if len(unexpected) > 0 {
		r.Status = "fail"
		r.Summary = fmt.Sprintf("%d unexpected infrastructure changes", len(unexpected))
	} else {
		r.Summary = "no unexpected infrastructure drift"
	}
	r.DurationMS = time.Since(start).Milliseconds()
	return r
}
