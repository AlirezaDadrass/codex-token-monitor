package main

import (
	"bytes"
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/AlirezaDadrass/codex-token-monitor/internal/monitor"
)

func setupTestOptions(t *testing.T) setupOptions {
	t.Helper()
	root := t.TempDir()
	t.Setenv("TOKEN_MONITOR_HUB_TOKEN", "")
	return setupOptions{ConfigPath: filepath.Join(root, "config.json"), Account: "personal", DataDir: filepath.Join(root, "data"), Install: true}
}

func TestInstallerPromptsNameAddressAndSecretAndVerifiesHub(t *testing.T) {
	o := setupTestOptions(t)
	hubConfig, err := monitor.NewConfig("hub")
	if err != nil {
		t.Fatal(err)
	}
	hubConfig.DataDir = t.TempDir()
	secret := strings.Repeat("test-secret-", 4)
	server := httptest.NewServer(monitor.HubHandler(hubConfig, secret))
	defer server.Close()
	var output bytes.Buffer
	input := "کامپیوتر دفتر\n" + server.URL + "\n" + secret + "\n"
	c, err := setupConfig(context.Background(), o, newPrompter(strings.NewReader(input), &output))
	if err != nil {
		t.Fatal(err)
	}
	if c.MachineName != "کامپیوتر دفتر" || c.HubURL != server.URL || c.TokenFile == "" {
		t.Fatalf("missing installation values: %+v", c)
	}
	if strings.Contains(output.String(), secret) {
		t.Fatal("installer echoed the hub secret")
	}
	for _, label := range []string{"نام این کامپیوتر", "آدرس سرور", "توکن اتصال"} {
		if !strings.Contains(output.String(), label) {
			t.Fatalf("missing prompt: %s", label)
		}
	}
	b, err := os.ReadFile(c.TokenFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(b)) != secret {
		t.Fatal("entered token was not persisted")
	}
	b, err = os.ReadFile(o.ConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), secret) {
		t.Fatal("secret was embedded in public configuration")
	}
}

func TestInstallerRejectsWrongTokenAndLeavesNoInstalledProfile(t *testing.T) {
	o := setupTestOptions(t)
	o.Name = "PC"
	o.NonInteractive = true
	o.TokenStdin = true
	hubConfig, _ := monitor.NewConfig("hub")
	hubConfig.DataDir = t.TempDir()
	server := httptest.NewServer(monitor.HubHandler(hubConfig, strings.Repeat("correct", 6)))
	defer server.Close()
	o.HubURL = server.URL
	_, err := setupConfig(context.Background(), o, newPrompter(strings.NewReader(strings.Repeat("wrong", 9)+"\n"), &bytes.Buffer{}))
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("wrong token accepted: %v", err)
	}
	for _, p := range []string{o.ConfigPath, filepath.Join(filepath.Dir(o.ConfigPath), "hub-token")} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("failed installation left credentials/profile at %s", p)
		}
	}
}

func TestInstallerNonInteractiveNeedsAllThreeValues(t *testing.T) {
	for _, missing := range []string{"name", "address", "token"} {
		t.Run(missing, func(t *testing.T) {
			o := setupTestOptions(t)
			o.NonInteractive = true
			o.SkipCheck = true
			o.Name = "PC"
			o.HubURL = "https://monitor.example.com"
			if missing != "token" {
				t.Setenv("TOKEN_MONITOR_HUB_TOKEN", strings.Repeat("secret", 7))
			}
			if missing == "name" {
				o.Name = ""
			}
			if missing == "address" {
				o.HubURL = ""
			}
			_, err := setupConfig(context.Background(), o, newPrompter(strings.NewReader(""), &bytes.Buffer{}))
			if err == nil {
				t.Fatalf("installed without %s", missing)
			}
		})
	}
}

func TestServerInstallerDryRunKeepsSecretsOutOfOutput(t *testing.T) {
	if _, err := os.Stat("/bin/bash"); err != nil {
		t.Skip("bash unavailable")
	}
	secret := strings.Repeat("private-secret", 3)
	command := exec.Command("bash", "../../scripts/install-server.sh", "--dry-run")
	command.Stdin = strings.NewReader("مرکز دفتر\n127.0.0.1\n8787\nhttp://127.0.0.1:8787\n" + secret + "\n\n")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("dry run: %v %s", err, output)
	}
	if strings.Contains(string(output), secret) || !strings.Contains(string(output), "no files, users, services") {
		t.Fatal("dry run leaked a secret or missed dry-run status")
	}
}
