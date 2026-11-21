package monitor

import (
	"context"
	"path/filepath"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func NewMCPServer(c Config, hubMode bool) *mcp.Server {
	return newMCPServer(c, hubMode, "")
}

func newMCPServer(c Config, hubMode bool, origin string) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "codex-token-monitor", Version: Version}, nil)
	type reportArgs struct {
		Metric  string `json:"metric,omitempty" jsonschema:"Ranking metric: total_tokens (default) or non_cached_tokens. Cached input is included in total_tokens."`
		Refresh *bool  `json:"refresh,omitempty" jsonschema:"Refresh local logs and push to hub first (default true). Hub MCP reads the latest uploaded snapshots."`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "generate_usage_report",
		Description: "DEFAULT for requests to see a Codex usage report: collect counters, compare named computers, and generate a complete beautiful offline Persian HTML file with charts, daily trends, quota, model breakdown, and coverage. No LLM is called to render it. Call this once and display only the returned display_markdown; do not read the HTML or rewrite its contents. Server-side download links require a signed expiring access token; the hub secret is never included.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: new(false), OpenWorldHint: new(false)},
	}, func(ctx context.Context, req *mcp.CallToolRequest, args reportArgs) (*mcp.CallToolResult, HTMLArtifact, error) {
		r, err := GetReport(ctx, c, hubMode, args.Metric, args.Refresh == nil || *args.Refresh)
		if err != nil {
			return nil, HTMLArtifact{}, err
		}
		artifact, err := WriteHTMLReport(c, r, "")
		if err != nil {
			return nil, artifact, err
		}
		if hubMode && origin != "" {
			token, err := c.Token()
			if err != nil {
				return nil, HTMLArtifact{}, err
			}
			artifact.URL = signedReportURL(origin, token, filepath.Base(artifact.Path), time.Now().UTC())
			artifact.DisplayMarkdown = "[مشاهدهٔ گزارش مصرف کدکس](" + artifact.URL + ")"
		}
		return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: artifact.DisplayMarkdown}}}, artifact, nil
	})
	mcp.AddTool(s, &mcp.Tool{
		Name: "get_weekly_usage", Description: "Read raw structured usage counters for numeric diagnostics. Prefer generate_usage_report when the user asks to see a report; it renders the HTML without model tokens. Device quota shares are estimates, not exact allowance usage.",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: hubMode, DestructiveHint: new(false), IdempotentHint: true, OpenWorldHint: new(false)},
	}, func(ctx context.Context, req *mcp.CallToolRequest, args reportArgs) (*mcp.CallToolResult, Report, error) {
		refresh := args.Refresh == nil || *args.Refresh
		r, err := GetReport(ctx, c, hubMode, args.Metric, refresh)
		return nil, r, err
	})
	type status struct {
		Mode            string    `json:"mode"`
		MachineID       string    `json:"machine_id"`
		MachineName     string    `json:"machine_name"`
		Account         string    `json:"account"`
		IntervalSeconds int       `json:"interval_seconds"`
		HubConfigured   bool      `json:"hub_configured"`
		Now             time.Time `json:"now"`
	}
	mcp.AddTool(s, &mcp.Tool{Name: "get_monitor_status", Description: "Read this monitor's device identity, account group, and mode. Device count is unrestricted. Does not reveal credentials or chat contents.", Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true, OpenWorldHint: new(false)}},
		func(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, status, error) {
			mode := "local"
			if hubMode {
				mode = "hub"
			}
			return nil, status{mode, c.MachineID, c.MachineName, c.Account, c.IntervalSeconds, c.HubURL != "", time.Now().UTC()}, nil
		})
	if !hubMode {
		type syncResult struct {
			MachineName string      `json:"machine_name"`
			CollectedAt time.Time   `json:"collected_at"`
			Events      int         `json:"retained_events"`
			Synced      bool        `json:"synced_to_hub"`
			Diagnostics Diagnostics `json:"diagnostics"`
		}
		mcp.AddTool(s, &mcp.Tool{Name: "sync_usage", Description: "Collect new local Codex token and rate-limit events, save locally, and upload sanitized counters to the configured hub. Does not upload prompts, responses, code, account IDs, or authentication files.", Annotations: &mcp.ToolAnnotations{DestructiveHint: new(false), IdempotentHint: true, OpenWorldHint: new(false)}},
			func(ctx context.Context, req *mcp.CallToolRequest, args struct{}) (*mcp.CallToolResult, syncResult, error) {
				snapshot, err := Refresh(ctx, c)
				return nil, syncResult{snapshot.MachineName, snapshot.CollectedAt, len(snapshot.Events), c.HubURL != "" && err == nil, snapshot.Diagnostics}, err
			})
	}
	return s
}
