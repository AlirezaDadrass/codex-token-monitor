//go:build !windows

package main

import (
	"context"
	"os/exec"
)

func codexRegistrationCommand(ctx context.Context, executable, configPath string) (*exec.Cmd, error) {
	return exec.CommandContext(ctx, "codex", "mcp", "add", "token-monitor", "--", executable, "mcp", "--config", configPath), nil
}
