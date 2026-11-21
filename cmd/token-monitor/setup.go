package main

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/AlirezaDadrass/codex-token-monitor/internal/monitor"
	"golang.org/x/term"
)

type setupOptions struct {
	ConfigPath, Name, Account, CodexHome, DataDir, HubURL, PublicURL, TokenFile string
	Install, CreateToken, Local, NonInteractive, TokenStdin, SkipCheck          bool
}

type prompter struct {
	reader *bufio.Reader
	input  io.Reader
	output io.Writer
}

func newPrompter(input io.Reader, output io.Writer) *prompter {
	return &prompter{reader: bufio.NewReader(input), input: input, output: output}
}

func (p *prompter) read(label string, secret bool) (string, error) {
	fmt.Fprint(p.output, label)
	if f, ok := p.input.(*os.File); secret && ok && term.IsTerminal(int(f.Fd())) {
		b, err := term.ReadPassword(int(f.Fd()))
		fmt.Fprintln(p.output)
		return strings.TrimSpace(string(b)), err
	}
	line, err := p.reader.ReadString('\n')
	if errors.Is(err, io.EOF) && len(line) > 0 {
		err = nil
	}
	if err != nil {
		return "", errors.New("required installation input was not provided")
	}
	return strings.TrimSpace(line), nil
}

func setupConfig(ctx context.Context, o setupOptions, p *prompter) (monitor.Config, error) {
	if _, err := os.Stat(o.ConfigPath); err == nil {
		if !o.Install {
			return monitor.Config{}, errors.New("config already exists; edit it or choose a new --config path")
		}
		c, err := monitor.LoadConfig(o.ConfigPath)
		if err != nil {
			return c, err
		}
		if (o.Name != "" && o.Name != c.MachineName) || (o.HubURL != "" && strings.TrimRight(o.HubURL, "/") != c.HubURL) || o.TokenStdin || o.CreateToken {
			return c, errors.New("existing configuration is preserved; edit it or choose another --config path")
		}
		if c.HubURL == "" && !o.Local {
			return c, errors.New("existing profile is local; use --local or configure hub_url and token_file")
		}
		if c.HubURL != "" && !o.SkipCheck {
			if err := monitor.CheckHub(ctx, c); err != nil {
				return c, fmt.Errorf("hub connection check failed: %w", err)
			}
		}
		fmt.Fprintf(p.output, "Using existing configuration for %s\n", c.MachineName)
		return c, nil
	} else if !os.IsNotExist(err) {
		return monitor.Config{}, err
	}
	if o.Local && (o.HubURL != "" || o.TokenFile != "" || o.TokenStdin) {
		return monitor.Config{}, errors.New("--local cannot be combined with hub credentials")
	}
	o.Name = strings.TrimSpace(o.Name)
	if o.Name == "" && !o.NonInteractive {
		var err error
		o.Name, err = p.read("نام این کامپیوتر در گزارش: ", false)
		if err != nil {
			return monitor.Config{}, err
		}
	}
	if o.Name == "" || len(o.Name) > 200 {
		return monitor.Config{}, errors.New("a computer name of 1..200 characters is required; use --name")
	}
	if o.Install && !o.Local && o.HubURL == "" {
		if o.NonInteractive {
			return monitor.Config{}, errors.New("--hub-url is required (or use --local for an explicit standalone install)")
		}
		var err error
		o.HubURL, err = p.read("آدرس سرور (مثال https://monitor.example.com): ", false)
		if err != nil {
			return monitor.Config{}, err
		}
		if o.HubURL == "" {
			return monitor.Config{}, errors.New("hub address is required; use --local for a standalone install")
		}
	}
	for _, address := range []string{o.HubURL, o.PublicURL} {
		if address == "" {
			continue
		}
		u, err := url.Parse(address)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return monitor.Config{}, errors.New("server address must be an http(s) URL without credentials, query, or fragment")
		}
	}
	if len(o.Account) == 0 || len(o.Account) > 200 {
		return monitor.Config{}, errors.New("account must be 1..200 characters")
	}
	c, err := monitor.NewConfig(o.Name)
	if err != nil {
		return c, err
	}
	c.Account = o.Account
	c.HubURL = strings.TrimRight(o.HubURL, "/")
	c.PublicURL = strings.TrimRight(o.PublicURL, "/")
	for dst, src := range map[*string]string{&c.CodexHome: o.CodexHome, &c.DataDir: o.DataDir, &c.TokenFile: o.TokenFile} {
		if src != "" {
			*dst, err = absoluteUserPath(src)
			if err != nil {
				return c, err
			}
		}
	}
	if o.CreateToken && (o.TokenFile != "" || o.TokenStdin) {
		return c, errors.New("choose only one of --create-token, --token-file, --token-stdin")
	}
	if o.TokenStdin && o.TokenFile != "" {
		return c, errors.New("choose either --token-file or --token-stdin")
	}
	secret := ""
	if o.CreateToken {
		var b [32]byte
		if _, err := rand.Read(b[:]); err != nil {
			return c, err
		}
		secret = hex.EncodeToString(b[:])
	} else if o.TokenStdin {
		secret, err = p.read("", true)
		if err != nil {
			return c, err
		}
		if secret == "" {
			return c, errors.New("hub token from stdin must not be empty")
		}
	} else if c.TokenFile == "" && c.HubURL != "" {
		secret = strings.TrimSpace(os.Getenv("TOKEN_MONITOR_HUB_TOKEN"))
		if secret == "" {
			if o.NonInteractive {
				return c, errors.New("a hub token is required; provide --token-file, --token-stdin, or TOKEN_MONITOR_HUB_TOKEN")
			}
			secret, err = p.read("توکن اتصال (مخفی): ", true)
			if err != nil {
				return c, err
			}
		}
	}
	if secret != "" && (len(secret) < 32 || len(strings.Fields(secret)) != 1) {
		return c, errors.New("hub token must be 32+ characters without whitespace")
	}
	if c.HubURL != "" && c.TokenFile == "" && secret == "" {
		return c, errors.New("hub token is required")
	}
	if err := os.MkdirAll(filepath.Dir(o.ConfigPath), 0700); err != nil {
		return c, err
	}
	createdToken := ""
	configSaved := false
	defer func() {
		if !configSaved && createdToken != "" {
			_ = os.Remove(createdToken)
		}
	}()
	if secret != "" {
		c.TokenFile, err = filepath.Abs(filepath.Join(filepath.Dir(o.ConfigPath), "hub-token"))
		if err != nil {
			return c, err
		}
		if err := writeNew(c.TokenFile, []byte(secret+"\n")); err != nil {
			return c, err
		}
		createdToken = c.TokenFile
	}
	if c.HubURL != "" {
		if _, err := c.Token(); err != nil {
			return c, err
		}
	}
	if o.Install && c.HubURL != "" && !o.SkipCheck {
		if err := monitor.CheckHub(ctx, c); err != nil {
			return c, fmt.Errorf("hub connection check failed; configuration not installed: %w", err)
		}
	}
	b, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return c, err
	}
	if err := writeNew(o.ConfigPath, append(b, '\n')); err != nil {
		return c, err
	}
	configSaved = true
	fmt.Fprintf(p.output, "Created configuration for %s at %s\n", c.MachineName, o.ConfigPath)
	if createdToken != "" {
		fmt.Fprintf(p.output, "Saved hub token privately at %s (token value is not displayed)\n", c.TokenFile)
	}
	return c, nil
}

func absoluteUserPath(path string) (string, error) {
	if strings.HasPrefix(path, "~/") || strings.HasPrefix(path, `~\`) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(home, path[2:])
	}
	return filepath.Abs(path)
}
