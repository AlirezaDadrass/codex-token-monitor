package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/AlirezaDadrass/codex-token-monitor/internal/monitor"
)

func TestInitAsksNameAndCreatesPrivateConnectionToken(t *testing.T) {
	root := t.TempDir()
	configPath := filepath.Join(root, "config.json")
	read, write, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = write.WriteString("لپ‌تاپ خانه\n"); err != nil {
		t.Fatal(err)
	}
	write.Close()
	original := os.Stdin
	os.Stdin = read
	t.Cleanup(func() { os.Stdin = original; read.Close() })
	if err := run(context.Background(), []string{"init", "--config", configPath, "--create-token"}); err != nil {
		t.Fatal(err)
	}
	c, err := monitor.LoadConfig(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if c.MachineName != "لپ‌تاپ خانه" || c.MachineID == "" {
		t.Fatalf("installation did not preserve chosen name: %+v", c)
	}
	secret, err := os.ReadFile(c.TokenFile)
	if err != nil {
		t.Fatal(err)
	}
	if len(strings.TrimSpace(string(secret))) != 64 {
		t.Fatal("connection token is not 256 bits")
	}
	// Windows uses ACLs; the native PowerShell installer tests verify those.
	if info, err := os.Stat(c.TokenFile); err != nil || (runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0) {
		t.Fatal("token file is accessible to other users")
	}
	if err := run(context.Background(), []string{"init", "--config", configPath, "--name", "Another"}); err == nil {
		t.Fatal("reinitialization overwrote machine identity")
	}
}

func TestInstallRegistersInIsolatedCodexHome(t *testing.T) {
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("Codex CLI is not installed")
	}
	root := t.TempDir()
	codexHome := filepath.Join(root, "isolated-codex")
	if err := os.MkdirAll(codexHome, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", codexHome)
	config := filepath.Join(root, "monitor.json")
	if err := run(context.Background(), []string{"install", "--config", config, "--name", "PC-3", "--local"}); err != nil {
		t.Fatal(err)
	}
	before, err := monitor.LoadConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	registered, err := os.ReadFile(filepath.Join(codexHome, "config.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(registered), "mcp_servers.token-monitor") || !strings.Contains(string(registered), config) {
		t.Fatal("MCP was not registered with the named device config")
	}
	if err := run(context.Background(), []string{"install", "--config", config, "--local"}); err != nil {
		t.Fatal(err)
	}
	after, err := monitor.LoadConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	if before.MachineID != after.MachineID || after.MachineName != "PC-3" {
		t.Fatal("reinstall changed the device identity")
	}
}

func TestHubRefusesMissingConnectionToken(t *testing.T) {
	t.Setenv("TOKEN_MONITOR_HUB_TOKEN", "")
	path := filepath.Join(t.TempDir(), "config.json")
	if err := run(context.Background(), []string{"init", "--config", path, "--name", "Hub"}); err != nil {
		t.Fatal(err)
	}
	if err := run(context.Background(), []string{"hub", "--config", path}); err == nil || !strings.Contains(err.Error(), "32+") {
		t.Fatalf("hub started without a connection token: %v", err)
	}
}
