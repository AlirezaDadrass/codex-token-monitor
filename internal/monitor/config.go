package monitor

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Config struct {
	MachineID          string `json:"machine_id"`
	MachineName        string `json:"machine_name"`
	Account            string `json:"account"`
	CodexHome          string `json:"codex_home"`
	DataDir            string `json:"data_dir"`
	HubURL             string `json:"hub_url,omitempty"`
	PublicURL          string `json:"public_url,omitempty"`
	TokenFile          string `json:"token_file,omitempty"`
	ExpectedMachines   int    `json:"expected_machines"`
	IntervalSeconds    int    `json:"interval_seconds"`
	RetentionDays      int    `json:"retention_days"`
	LimitID            string `json:"limit_id"`
	AccountFingerprint string `json:"account_fingerprint,omitempty"`
}

func DefaultConfigPath() string {
	dir, err := os.UserConfigDir()
	if err != nil {
		dir = "."
	}
	return filepath.Join(dir, "codex-token-monitor", "config.json")
}

func NewConfig(name string) (Config, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return Config{}, err
	}
	if name == "" {
		name, _ = os.Hostname()
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return Config{}, err
	}
	codexHome := os.Getenv("CODEX_HOME")
	if codexHome == "" {
		codexHome = filepath.Join(home, ".codex")
	}
	dataRoot, err := os.UserConfigDir()
	if err != nil {
		return Config{}, err
	}
	return Config{
		MachineID: hex.EncodeToString(id[:]), MachineName: name, Account: "personal",
		CodexHome: codexHome, DataDir: filepath.Join(dataRoot, "codex-token-monitor", "data"),
		ExpectedMachines: 3, IntervalSeconds: 60, RetentionDays: 14, LimitID: "codex",
	}, nil
}

func LoadConfig(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config (run init first): %w", err)
	}
	var c Config
	if err := json.Unmarshal(b, &c); err != nil {
		return c, err
	}
	for _, v := range []string{c.MachineID, c.MachineName, c.Account, c.LimitID} {
		if strings.TrimSpace(v) == "" || len(v) > 200 {
			return c, errors.New("identifiers must be 1..200 characters")
		}
	}
	if c.ExpectedMachines < 1 || c.ExpectedMachines > 100 || c.IntervalSeconds < 5 ||
		c.IntervalSeconds > 86400 || c.RetentionDays < 8 || c.RetentionDays > 365 {
		return c, errors.New("invalid expected_machines, interval_seconds, or retention_days")
	}
	for _, p := range []*string{&c.CodexHome, &c.DataDir, &c.TokenFile} {
		if *p == "" {
			continue
		}
		if strings.HasPrefix(*p, "~/") || strings.HasPrefix(*p, `~\`) {
			home, err := os.UserHomeDir()
			if err != nil {
				return c, err
			}
			*p = filepath.Join(home, (*p)[2:])
		}
		if !filepath.IsAbs(*p) {
			*p = filepath.Join(filepath.Dir(path), *p)
		}
		*p, err = filepath.Abs(*p)
		if err != nil {
			return c, err
		}
	}
	if c.CodexHome == "" || c.DataDir == "" {
		return c, errors.New("codex_home and data_dir are required")
	}
	c.HubURL = strings.TrimRight(c.HubURL, "/")
	for _, address := range []string{c.HubURL, c.PublicURL} {
		if address == "" {
			continue
		}
		u, err := url.Parse(address)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" ||
			u.User != nil || u.RawQuery != "" || u.Fragment != "" {
			return c, errors.New("hub_url/public_url must be an http(s) URL without credentials, query, or fragment")
		}
	}
	return c, nil
}

func (c Config) Token() (string, error) {
	token := os.Getenv("TOKEN_MONITOR_HUB_TOKEN")
	if token == "" && c.TokenFile != "" {
		b, err := os.ReadFile(c.TokenFile)
		if err != nil {
			return "", err
		}
		token = strings.TrimSpace(string(b))
	}
	if len(token) < 32 || len(strings.Fields(token)) != 1 {
		return "", errors.New("set TOKEN_MONITOR_HUB_TOKEN or token_file to a secret of 32+ characters")
	}
	return token, nil
}

func atomicJSON(path string, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return atomicWrite(path, b)
}

func atomicWrite(path string, b []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(f.Name(), path)
}

func staleAfter(c Config) time.Duration {
	return max(5*time.Minute, time.Duration(c.IntervalSeconds)*3*time.Second)
}
