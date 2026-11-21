package monitor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testConfig(t *testing.T) Config {
	t.Helper()
	c, err := NewConfig("test-machine")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	c.CodexHome = filepath.Join(root, "codex")
	c.DataDir = filepath.Join(root, "data")
	if err := os.MkdirAll(filepath.Join(c.CodexHome, "sessions"), 0700); err != nil {
		t.Fatal(err)
	}
	return c
}

func jsonLine(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b) + "\n"
}
func metaLine() string {
	return jsonLine(map[string]any{"type": "session_meta", "payload": map[string]any{"id": "session-1", "creator_account_id": "account-1", "model_provider": "openai"}})
}
func tokenLine(at time.Time, total, last *Tokens, primary, secondary any) string {
	p := map[string]any{"type": "token_count", "rate_limits": map[string]any{"limit_id": "codex", "primary": primary, "secondary": secondary}}
	if total != nil {
		p["info"] = map[string]any{"total_token_usage": total, "last_token_usage": last}
	}
	return jsonLine(map[string]any{"type": "event_msg", "timestamp": at, "payload": p})
}
func window(used float64, minutes int64, reset time.Time) any {
	return map[string]any{"used_percent": used, "window_minutes": minutes, "resets_at": reset.Unix()}
}
func tokens(input, cached, output, reasoning int64) Tokens {
	return Tokens{Input: input, Cached: cached, Output: output, Reasoning: reasoning, Total: input + output}
}
func writeFile(t *testing.T, path, s string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(s), 0600); err != nil {
		t.Fatal(err)
	}
}
func appendFile(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err = f.WriteString(s); err != nil {
		t.Fatal(err)
	}
}

func TestCollectIncrementalRepeatedPartialAndQuotaOnly(t *testing.T) {
	c := testConfig(t)
	now := time.Now().UTC()
	reset := now.Add(time.Hour).Truncate(time.Second)
	a := tokens(100, 60, 20, 10)
	b := tokens(50, 20, 10, 4)
	total := a.Add(b)
	p := filepath.Join(c.CodexHome, "sessions", "one.jsonl")
	initial := metaLine() + jsonLine(map[string]any{"type": "turn_context", "payload": map[string]any{"model": "gpt-test"}}) +
		tokenLine(now.Add(-time.Minute), &a, &a, nil, nil) + tokenLine(now.Add(-50*time.Second), &a, &a, nil, nil)
	writeFile(t, p, initial)
	s, err := Collect(context.Background(), c, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Events) != 1 || s.Events[0].Tokens != a || s.Events[0].Model != "gpt-test" {
		t.Fatalf("bad first scan: %+v", s)
	}
	second := tokenLine(now.Add(-30*time.Second), &total, &b, window(70, 10080, reset), nil)
	appendFile(t, p, second[:len(second)/2])
	s, err = Collect(context.Background(), c, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Events) != 1 {
		t.Fatal("partial line counted")
	}
	appendFile(t, p, second[len(second)/2:]+tokenLine(now.Add(-10*time.Second), nil, nil, window(73, 10080, reset), nil))
	s, err = Collect(context.Background(), c, now)
	if err != nil {
		t.Fatal(err)
	}
	var sum Tokens
	for _, e := range s.Events {
		sum = sum.Add(e.Tokens)
	}
	if sum != total || len(s.Events) != 2 || len(s.Quotas) != 1 || s.Quotas[0].UsedPercent != 73 {
		t.Fatalf("wrong delta/quota: %+v", s)
	}
	s2, err := Collect(context.Background(), c, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(s2.Events) != 2 {
		t.Fatal("rescan duplicated events")
	}
}

func TestCollectBootstrapResetArchivedAndCopiedLogs(t *testing.T) {
	c := testConfig(t)
	now := time.Now().UTC()
	last := tokens(10, 4, 3, 1)
	inherited := tokens(1000, 400, 300, 100)
	path := filepath.Join(c.CodexHome, "sessions", "one.jsonl")
	contents := metaLine() + tokenLine(now.Add(-time.Minute), &inherited, &last, nil, nil) + tokenLine(now.Add(-30*time.Second), &last, &last, nil, nil)
	writeFile(t, path, contents)
	archived := filepath.Join(c.CodexHome, "archived_sessions")
	if err := os.MkdirAll(archived, 0700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(archived, "copied.jsonl"), contents)
	s, err := Collect(context.Background(), c, now)
	if err != nil {
		t.Fatal(err)
	}
	var sum Tokens
	for _, e := range s.Events {
		sum = sum.Add(e.Tokens)
	}
	if sum != last.Add(last) || len(s.Events) != 2 || s.Diagnostics.BootstrapEvents != 2 || s.Diagnostics.CounterResets != 2 {
		t.Fatalf("bad bootstrap/reset/dedupe: %+v", s)
	}
	// Replacing the source must reset its cursor and still deduplicate known events.
	writeFile(t, path, contents+tokenLine(now.Add(-time.Second), nil, nil, nil, nil))
	s, err = Collect(context.Background(), c, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Events) != 2 {
		t.Fatal("replacement duplicated events")
	}
}

func TestCollectFilteringPrivacyAndMalformedLines(t *testing.T) {
	c := testConfig(t)
	now := time.Now().UTC()
	a := tokens(30, 20, 4, 2)
	contents := metaLine() + `{"type":"event_msg","payload":{"type":"token_count"},` + "\n" +
		jsonLine(map[string]any{"type": "response_item", "payload": map[string]string{"secret": "private-prompt-auth_token"}}) +
		tokenLine(now.Add(-time.Second), &a, &a, nil, nil)
	writeFile(t, filepath.Join(c.CodexHome, "sessions", "one.jsonl"), contents)
	s, err := Collect(context.Background(), c, now)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(s)
	for _, secret := range []string{"private-prompt-auth_token", "session-1", "account-1", c.CodexHome} {
		if strings.Contains(string(b), secret) {
			t.Fatalf("snapshot leaked %s", secret)
		}
	}
	if s.Diagnostics.MalformedLines != 1 {
		t.Fatalf("bad malformed count: %+v", s.Diagnostics)
	}
	c.AccountFingerprint = hash("account:another-account")
	s, err = Collect(context.Background(), c, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Events) != 0 {
		t.Fatal("account filter failed")
	}
}

func TestCollectSkipsOtherProvidersAndExpiredEvents(t *testing.T) {
	c := testConfig(t)
	now := time.Now().UTC()
	a := tokens(10, 0, 1, 0)
	writeFile(t, filepath.Join(c.CodexHome, "sessions", "old.jsonl"), metaLine()+tokenLine(now.Add(-20*24*time.Hour), &a, &a, nil, nil))
	other := strings.ReplaceAll(metaLine(), `"openai"`, `"other-provider"`) + tokenLine(now.Add(-time.Second), &a, &a, nil, nil)
	writeFile(t, filepath.Join(c.CodexHome, "sessions", "other.jsonl"), other)
	s, err := Collect(context.Background(), c, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Events) != 0 {
		t.Fatalf("unrelated or expired usage counted: %+v", s.Events)
	}
}

func TestContextSizeNotificationIsNotConsumption(t *testing.T) {
	c := testConfig(t)
	now := time.Now().UTC()
	a := tokens(100, 20, 10, 3)
	contextSize := Tokens{Total: 125543}
	b := tokens(30, 20, 3, 1)
	total := a.Add(b)
	writeFile(t, filepath.Join(c.CodexHome, "sessions", "context.jsonl"), metaLine()+
		tokenLine(now.Add(-time.Minute), &a, &a, nil, nil)+
		tokenLine(now.Add(-30*time.Second), &a, &contextSize, nil, nil)+
		tokenLine(now.Add(-time.Second), &total, &b, nil, nil))
	s, err := Collect(context.Background(), c, now)
	if err != nil {
		t.Fatal(err)
	}
	var sum Tokens
	for _, e := range s.Events {
		sum = sum.Add(e.Tokens)
	}
	if sum != total || len(s.Events) != 2 || s.Diagnostics.MalformedLines != 0 {
		t.Fatalf("context-size notification treated as usage or corruption: %+v", s)
	}
}

func TestCollectorSerializesConcurrentProcesses(t *testing.T) {
	c := testConfig(t)
	now := time.Now().UTC()
	a := tokens(30, 20, 3, 1)
	writeFile(t, filepath.Join(c.CodexHome, "sessions", "one.jsonl"), metaLine()+tokenLine(now.Add(-time.Second), &a, &a, nil, nil))
	errs := make(chan error, 3)
	for i := 0; i < 3; i++ {
		go func() { _, err := Refresh(context.Background(), c); errs <- err }()
	}
	for i := 0; i < 3; i++ {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
	s, err := Collect(context.Background(), c, now)
	if err != nil || len(s.Events) != 1 || s.Events[0].Tokens != a {
		t.Fatalf("concurrent collection lost or duplicated data: %+v %v", s, err)
	}
}
