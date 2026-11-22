package monitor

import (
	"context"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func NewMCPServer(c Config, hubMode bool) *mcp.Server {
	token := ""
	if hubMode {
		token, _ = c.Token()
	}
	return newMCPServer(c, hubMode, c.PublicURL, token)
}

func newMCPServer(c Config, hubMode bool, origin, token string) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "codex-token-monitor", Version: Version}, nil)
	type reportArgs struct {
		Metric  string `json:"metric,omitempty" jsonschema:"Ranking metric: total_tokens (default) or non_cached_tokens. Cached input is included in total_tokens."`
		Refresh *bool  `json:"refresh,omitempty" jsonschema:"Refresh local logs and push to hub first (default true). Hub MCP reads the latest uploaded snapshots."`
	}
	mcp.AddTool(s, &mcp.Tool{
		Name:        "generate_usage_report",
		Description: "DEFAULT for Codex usage reports: refresh this collector and request one standalone Persian HTML report on the hub, including all registered devices and per-session counters, rankings, charts, daily trends, quota, models, and coverage. Returns a signed hub link only; no local HTML or local fallback. Differing log account identities do not block token counts; uncertain shared quota is withheld. No LLM renders it. Call once and display only display_markdown; never read/rewrite the HTML. The hub secret is never in the link.",
		Annotations: &mcp.ToolAnnotations{DestructiveHint: new(false), OpenWorldHint: new(false)},
	}, func(ctx context.Context, req *mcp.CallToolRequest, args reportArgs) (*mcp.CallToolResult, HTMLArtifact, error) {
		var artifact HTMLArtifact
		var err error
		if hubMode {
			artifact, err = createHubArtifact(ctx, c, token, origin, args.Metric)
		} else {
			artifact, err = GenerateReport(ctx, c, args.Metric, args.Refresh == nil || *args.Refresh)
		}
		if err != nil {
			return nil, HTMLArtifact{}, err
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
