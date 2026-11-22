package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/AlirezaDadrass/codex-token-monitor/internal/monitor"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := run(ctx, os.Args[1:]); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 1 {
		switch args[0] {
		case "--version":
			fmt.Println("token-monitor " + monitor.Version)
			return nil
		case "--help", "-h", "help":
			fmt.Println("Usage: token-monitor <install|init|collect|watch|report|mcp|hub> [flags]\nUse token-monitor COMMAND --help for command options.")
			return nil
		}
	}
	if len(args) == 0 {
		return errors.New("usage: token-monitor <install|init|collect|watch|report|mcp|hub> [flags]")
	}
	command := args[0]
	f := flag.NewFlagSet(command, flag.ContinueOnError)
	configPath := f.String("config", monitor.DefaultConfigPath(), "configuration file")
	var name, account, codexHome, dataDir, hubURL, publicURL, tokenFile *string
	var createToken, local, nonInteractive, tokenStdin, skipCheck *bool
	var metric, listen, cert, key, output *string
	var htmlReport *bool
	var noRefresh *bool
	switch command {
	case "init", "install":
		name = f.String("name", "", "display name for this computer")
		account = f.String("account", "personal", "same account group on all computers")
		codexHome = f.String("codex-home", "", "Codex data directory (default CODEX_HOME or ~/.codex)")
		dataDir = f.String("data-dir", "", "local state directory")
		hubURL = f.String("hub-url", "", "shared hub URL")
		publicURL = f.String("public-url", "", "public hub URL for report links behind a reverse proxy")
		local = f.Bool("local", false, "explicit standalone install without a hub")
		nonInteractive = f.Bool("non-interactive", false, "fail instead of prompting for missing values")
		tokenStdin = f.Bool("token-stdin", false, "read hub token from stdin without putting it in process arguments")
		skipCheck = f.Bool("skip-check", false, "defer the hub connection check for an offline install")
		tokenFile = f.String("token-file", "", "file containing the hub secret")
		createToken = f.Bool("create-token", false, "generate hub-token next to config, without printing it")
	case "report":
		metric = f.String("metric", "total_tokens", "total_tokens or non_cached_tokens")
		noRefresh = f.Bool("no-refresh", false, "read cached counters without scanning local logs")
		htmlReport = f.Bool("html", false, "generate an offline HTML report without an LLM")
		output = f.String("output", "", "HTML output path (default: reports directory)")
	case "hub":
		listen = f.String("listen", "127.0.0.1:8787", "HTTP listen address")
		cert = f.String("tls-cert", "", "optional TLS certificate")
		key = f.String("tls-key", "", "optional TLS private key")
	case "collect", "watch", "mcp":
	default:
		return fmt.Errorf("unknown command %q", command)
	}
	if err := f.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if len(f.Args()) != 0 {
		return errors.New("unexpected positional arguments")
	}
	if command == "init" || command == "install" {
		_, err := setupConfig(ctx, setupOptions{
			ConfigPath: *configPath, Name: *name, Account: *account, CodexHome: *codexHome,
			DataDir: *dataDir, HubURL: *hubURL, PublicURL: *publicURL, TokenFile: *tokenFile,
			Install: command == "install", CreateToken: *createToken, Local: *local,
			NonInteractive: *nonInteractive, TokenStdin: *tokenStdin, SkipCheck: *skipCheck,
		}, newPrompter(os.Stdin, os.Stderr))
		if err != nil {
			return err
		}
		if command == "install" {
			return registerMCP(ctx, *configPath)
		}
		return nil
	}
	c, err := monitor.LoadConfig(*configPath)
	if err != nil {
		return err
	}
	switch command {
	case "collect":
		s, err := monitor.Refresh(ctx, c)
		if err != nil {
			return err
		}
		return printJSON(map[string]any{"machine": s.MachineName, "events": len(s.Events), "collected_at": s.CollectedAt, "diagnostics": s.Diagnostics, "synced_to_hub": c.HubURL != ""})
	case "watch":
		return watch(ctx, c)
	case "report":
		r, err := monitor.GetReport(ctx, c, false, *metric, !*noRefresh)
		if err != nil {
			return err
		}
		if *htmlReport || *output != "" {
			artifact, err := monitor.WriteHTMLReport(c, r, *output)
			if err != nil {
				return err
			}
			return printJSON(artifact)
		}
		return printJSON(r)
	case "mcp":
		// Background collection keeps this computer visible while Codex is open.
		go func() {
			if err := watch(ctx, c); err != nil && ctx.Err() == nil {
				log.Print(err)
			}
		}()
		return monitor.NewMCPServer(c, false).Run(ctx, &mcp.StdioTransport{})
	case "hub":
		token, err := c.Token()
		if err != nil {
			return err
		}
		if (*cert == "") != (*key == "") {
			return errors.New("provide both --tls-cert and --tls-key")
		}
		server := &http.Server{Addr: *listen, Handler: monitor.HubHandler(c, token), ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout: 30 * time.Second, WriteTimeout: 60 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 16 << 10}
		go func() {
			<-ctx.Done()
			stop, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = server.Shutdown(stop)
		}()
		log.Printf("Token Monitor hub listening on %s", *listen)
		if *cert != "" {
			err = server.ListenAndServeTLS(*cert, *key)
		} else {
			err = server.ListenAndServe()
		}
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
	return nil
}

func registerMCP(ctx context.Context, configPath string) error {
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	configPath, err = filepath.Abs(configPath)
	if err != nil {
		return err
	}
	cmd, err := codexRegistrationCommand(ctx, executable, configPath)
	if err != nil {
		return fmt.Errorf("configuration saved, but Codex registration failed: %w", err)
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("configuration saved, but Codex registration failed: %w", err)
	}
	return nil
}

func watch(ctx context.Context, c monitor.Config) error {
	timer := time.NewTicker(time.Duration(c.IntervalSeconds) * time.Second)
	defer timer.Stop()
	for {
		if ctx.Err() != nil {
			return nil
		}
		if _, err := monitor.Refresh(ctx, c); err != nil && ctx.Err() == nil {
			log.Printf("Collection/sync failed (will retry): %v", err)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-timer.C:
		}
	}
}

func writeNew(path string, b []byte) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, err = f.Write(b)
	closeErr := f.Close()
	if err != nil {
		return err
	}
	return closeErr
}

func printJSON(v any) error {
	e := json.NewEncoder(os.Stdout)
	e.SetIndent("", "  ")
	return e.Encode(v)
}
