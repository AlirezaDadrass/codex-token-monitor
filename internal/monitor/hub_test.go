package monitor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const testToken = "test-secret-with-more-than-thirty-two-characters"

type bearerTransport struct{ base http.RoundTripper }

func (b bearerTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	copy := req.Clone(req.Context())
	copy.Header.Set("Authorization", "Bearer "+testToken)
	return b.base.RoundTrip(copy)
}

func TestHubThreeCollectorsAuthValidationAndMCP(t *testing.T) {
	c := testConfig(t)
	hub := httptest.NewServer(HubHandler(c, testToken))
	defer hub.Close()
	resp, err := http.Get(hub.URL + "/v1/report")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 401 {
		t.Fatal("unauthenticated access allowed")
	}
	t.Setenv("TOKEN_MONITOR_HUB_TOKEN", testToken)
	now := time.Now().UTC()
	var wg sync.WaitGroup
	for _, id := range []string{"a", "b", "c"} {
		wg.Add(1)
		go func(id string) {
			defer wg.Done()
			client := c
			client.HubURL = hub.URL
			snapshot := sampleSnapshot(id, int64(id[0]), now)
			snapshot.Events[0].AccountFingerprint = hash("retained-identity-" + id)
			var out map[string]bool
			for i := 0; i < 2; i++ {
				if err := hubRequest(context.Background(), client, "POST", "/v1/snapshots", snapshot, &out); err != nil {
					t.Error(err)
				}
			}
		}(id)
	}
	wg.Wait()
	client := c
	client.HubURL = hub.URL
	var report Report
	if err := hubRequest(context.Background(), client, "GET", "/v1/report", nil, &report); err != nil {
		t.Fatal(err)
	}
	if report.ReportingMachines != 3 || report.Totals.Total != 294 || report.Scope != "hub" || len(report.Sessions) != 3 || !report.AccountIdentityAmbiguous || report.WeeklyQuota != nil {
		t.Fatalf("incorrect shared report: %+v", report)
	}
	bad := sampleSnapshot("bad", 20, now)
	bad.Events[0].Tokens.Total = -1
	if err := hubRequest(context.Background(), client, "POST", "/v1/snapshots", bad, &map[string]bool{}); err == nil {
		t.Fatal("accepted invalid counters")
	}
	bad = sampleSnapshot("bad", 20, now)
	bad.Account = "another-account"
	if err := hubRequest(context.Background(), client, "POST", "/v1/snapshots", bad, &map[string]bool{}); err == nil {
		t.Fatal("accepted different account")
	}
	// Reject unexpected fields so prompts cannot be accidentally stored in snapshots.
	b, _ := json.Marshal(sampleSnapshot("bad", 20, now))
	b = append(b[:len(b)-1], []byte(`,"prompt":"secret"}`)...)
	req, _ := http.NewRequest("POST", hub.URL+"/v1/snapshots", bytes.NewReader(b))
	req.Header.Set("Authorization", "Bearer "+testToken)
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 400 {
		t.Fatal("accepted unexpected prompt field")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// A token-file override used to start the hub must also be used when signing
	// MCP reports, even when the process environment contains an older token.
	t.Setenv("TOKEN_MONITOR_HUB_TOKEN", "old-environment-token-with-thirty-two-plus-characters")
	mcpClient := mcp.NewClient(&mcp.Implementation{Name: "test"}, nil)
	session, err := mcpClient.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: hub.URL + "/mcp", HTTPClient: &http.Client{Transport: bearerTransport{http.DefaultTransport}}, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 3 {
		t.Fatalf("hub exposed local write tool: %+v", tools.Tools)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "get_weekly_usage", Arguments: map[string]any{}})
	if err != nil || result.IsError {
		t.Fatalf("HTTP MCP failed: %+v %v", result, err)
	}
	b, err = json.Marshal(result.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(b, &report); err != nil {
		t.Fatal(err)
	}
	if report.Totals.Total != 294 {
		t.Fatalf("MCP returned wrong totals: %s", b)
	}
	result, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "generate_usage_report", Arguments: map[string]any{}})
	if err != nil || result.IsError {
		t.Fatalf("HTML MCP failed: %+v %v", result, err)
	}
	b, _ = json.Marshal(result.StructuredContent)
	var artifact HTMLArtifact
	if err = json.Unmarshal(b, &artifact); err != nil {
		t.Fatal(err)
	}
	if artifact.URL == "" || artifact.LLMCalls != 0 || len(b) > 2000 {
		t.Fatalf("HTML output is missing or too large: %s", b)
	}
	response, err := http.Get(artifact.URL)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 200 {
		t.Fatalf("signed download failed: %d", response.StatusCode)
	}
	artifactURL, _ := url.Parse(artifact.URL)
	response, err = http.Get(hub.URL + artifactURL.Path)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if response.StatusCode != 401 {
		t.Fatal("report download allowed without any access token")
	}
}

func TestHubUnavailableNeverReturnsLocalReport(t *testing.T) {
	c := testConfig(t)
	now := time.Now().UTC()
	c.HubURL = "http://127.0.0.1:1"
	t.Setenv("TOKEN_MONITOR_HUB_TOKEN", testToken)
	_, err := (Store{Dir: filepath.Join(c.DataDir, "snapshots")}).Put(context.Background(), sampleSnapshot(c.MachineID, 100, now))
	if err != nil {
		t.Fatal(err)
	}
	r, err := GetReport(context.Background(), c, false, "", false)
	if err == nil || r.Totals.Total != 0 || r.Scope != "" {
		t.Fatalf("hub outage was replaced with a local report: %+v %v", r, err)
	}
	if _, err := GenerateReport(context.Background(), c, "", false); err == nil {
		t.Fatal("offline HTML report succeeded")
	}
	c.HubURL = ""
	if _, err := GetReport(context.Background(), c, false, "", false); err == nil {
		t.Fatal("standalone report still available")
	}
}

func TestCollectorsUploadAllSessionsToOneHub(t *testing.T) {
	hubConfig := testConfig(t)
	hub := httptest.NewServer(HubHandler(hubConfig, testToken))
	defer hub.Close()
	t.Setenv("TOKEN_MONITOR_HUB_TOKEN", testToken)
	now := time.Now().UTC()
	var clients []Config
	for device := 1; device <= 3; device++ {
		c := testConfig(t)
		c.MachineID, c.MachineName = fmt.Sprintf("pc-%d", device), fmt.Sprintf("Computer %d", device)
		c.HubURL = hub.URL
		for session := 1; session <= 2; session++ {
			meta := jsonLine(map[string]any{"type": "session_meta", "payload": map[string]any{"id": fmt.Sprintf("pc-%d-session-%d", device, session), "creator_account_id": fmt.Sprintf("retained-metadata-%d", device%2), "model_provider": "openai"}})
			first, last := tokens(int64(100*device), 0, 0, 0), tokens(int64(50*device), 0, 0, 0)
			total := first.Add(last)
			writeFile(t, filepath.Join(c.CodexHome, "sessions", fmt.Sprintf("session-%d.jsonl", session)), meta+tokenLine(now.Add(-2*time.Hour), &first, &first, nil, nil)+tokenLine(now.Add(-time.Hour), &total, &last, nil, window(60, 10080, now.Add(time.Hour))))
		}
		if _, err := Refresh(context.Background(), c); err != nil {
			t.Fatal(err)
		}
		// Incremental retry must replace the device snapshot, never add it again.
		if _, err := Refresh(context.Background(), c); err != nil {
			t.Fatal(err)
		}
		clients = append(clients, c)
	}
	r, err := GetReport(context.Background(), clients[0], false, "", false)
	if err != nil || r.Scope != "hub" || r.ReportingMachines != 3 || len(r.Sessions) != 6 || r.Totals.Total != 1800 || !r.AccountIdentityAmbiguous {
		t.Fatalf("collectors did not produce one shared session report: %+v %v", r, err)
	}
	for _, m := range r.Machines {
		if m.Sessions != 2 {
			t.Fatalf("missing sessions for %s", m.Name)
		}
	}
	a, err := GenerateReport(context.Background(), clients[0], "", false)
	if err != nil || a.ReportingMachines != 3 || a.Path != "" || a.URL == "" {
		t.Fatalf("hub HTML failed: %+v %v", a, err)
	}
}

func TestReportUsesConfiguredPublicAddressBehindProxy(t *testing.T) {
	c := testConfig(t)
	c.PublicURL = "https://monitor.example.com"
	t.Setenv("TOKEN_MONITOR_HUB_TOKEN", testToken)
	hub := httptest.NewServer(HubHandler(c, testToken))
	defer hub.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	client := mcp.NewClient(&mcp.Implementation{Name: "proxy-test"}, nil)
	session, err := client.Connect(ctx, &mcp.StreamableClientTransport{Endpoint: hub.URL + "/mcp", HTTPClient: &http.Client{Transport: bearerTransport{http.DefaultTransport}}, DisableStandaloneSSE: true}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "generate_usage_report", Arguments: map[string]any{}})
	if err != nil || result.IsError {
		t.Fatalf("report failed: %+v %v", result, err)
	}
	b, _ := json.Marshal(result.StructuredContent)
	var artifact HTMLArtifact
	if err := json.Unmarshal(b, &artifact); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(artifact.URL, c.PublicURL+"/reports/") {
		t.Fatalf("report link used internal HTTP origin: %s", artifact.URL)
	}
}

func TestHubAcceptsMoreThanOneHundredDevices(t *testing.T) {
	c := testConfig(t)
	server := httptest.NewServer(HubHandler(c, testToken))
	defer server.Close()
	c.HubURL = server.URL
	t.Setenv("TOKEN_MONITOR_HUB_TOKEN", testToken)
	now := time.Now().UTC()
	for i := 0; i < 101; i++ {
		var response map[string]bool
		if err := hubRequest(context.Background(), c, http.MethodPost, "/v1/snapshots", sampleSnapshot(fmt.Sprintf("pc-%d", i), 1, now), &response); err != nil {
			t.Fatalf("device %d rejected: %v", i, err)
		}
		if !response["accepted"] {
			t.Fatalf("device %d was not accepted", i)
		}
	}
	var report Report
	if err := hubRequest(context.Background(), c, http.MethodGet, "/v1/report", nil, &report); err != nil {
		t.Fatal(err)
	}
	if report.ReportingMachines != 101 || report.Totals.Total != 101 || !report.CoverageComplete {
		t.Fatalf("hub restricts device count or misreports data: %+v", report)
	}
}

func TestStdioMCPBinary(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	bin := filepath.Join(t.TempDir(), "token-monitor")
	if runtime.GOOS == "windows" {
		bin += ".exe"
	}
	build := exec.CommandContext(ctx, "go", "build", "-o", bin, "../../cmd/token-monitor")
	if b, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, b)
	}
	c := testConfig(t)
	hub := httptest.NewServer(HubHandler(testConfig(t), testToken))
	defer hub.Close()
	c.HubURL = hub.URL
	t.Setenv("TOKEN_MONITOR_HUB_TOKEN", testToken)
	now := time.Now().UTC()
	a := tokens(123, 100, 10, 5)
	writeFile(t, filepath.Join(c.CodexHome, "sessions", "one.jsonl"), metaLine()+tokenLine(now.Add(-time.Second), &a, &a, nil, window(50, 10080, now.Add(time.Hour))))
	config := filepath.Join(t.TempDir(), "config.json")
	if err := atomicJSON(config, c); err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "stdio-test"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: exec.CommandContext(ctx, bin, "mcp", "--config", config)}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()
	tools, err := session.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(tools.Tools) != 4 {
		t.Fatalf("wrong tools: %+v", tools.Tools)
	}
	result, err := session.CallTool(ctx, &mcp.CallToolParams{Name: "get_weekly_usage", Arguments: map[string]any{}})
	if err != nil || result.IsError {
		t.Fatalf("stdio MCP failed: %+v %v", result, err)
	}
	b, _ := json.Marshal(result.StructuredContent)
	var r Report
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	if r.Totals != a || r.WeeklyQuota == nil || r.WeeklyQuota.UsedPercent != 50 {
		t.Fatalf("wrong stdio result: %s", b)
	}
	result, err = session.CallTool(ctx, &mcp.CallToolParams{Name: "generate_usage_report", Arguments: map[string]any{}})
	if err != nil || result.IsError {
		t.Fatalf("stdio HTML MCP failed: %+v %v", result, err)
	}
	b, _ = json.Marshal(result.StructuredContent)
	var artifact HTMLArtifact
	if err := json.Unmarshal(b, &artifact); err != nil {
		t.Fatal(err)
	}
	if artifact.Path != "" || artifact.URL == "" || artifact.LLMCalls != 0 || artifact.ReportingMachines != 1 {
		t.Fatalf("bad HTML artifact: %s", b)
	}
	files, _ := filepath.Glob(filepath.Join(c.DataDir, "reports", "*.html"))
	if len(files) != 0 {
		t.Fatal("stdio client created a local HTML report")
	}
}
