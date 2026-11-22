package main

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode/utf16"
)

func codexRegistrationCommand(ctx context.Context, executable, configPath string) (*exec.Cmd, error) {
	path, err := exec.LookPath("codex")
	if err != nil {
		return nil, err
	}
	args := []string{"mcp", "add", "token-monitor", "--", executable, "mcp", "--config", configPath}
	if strings.EqualFold(filepath.Ext(path), ".exe") {
		return exec.CommandContext(ctx, path, args...), nil
	}
	// npm commonly exposes codex.cmd. PowerShell executes batch launchers while
	// single-quoted literals keep paths out of shell syntax. No credential is used.
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	parts := []string{"&", quote(path)}
	for _, arg := range args {
		parts = append(parts, quote(arg))
	}
	script := strings.Join(parts, " ") + "; exit $LASTEXITCODE"
	units := utf16.Encode([]rune(script))
	data := make([]byte, len(units)*2)
	for i, v := range units {
		binary.LittleEndian.PutUint16(data[i*2:], v)
	}
	return exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-EncodedCommand", base64.StdEncoding.EncodeToString(data)), nil
}
