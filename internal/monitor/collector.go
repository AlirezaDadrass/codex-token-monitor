package monitor

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/gofrs/flock"
)

func hash(s string) string { v := sha256.Sum256([]byte(s)); return hex.EncodeToString(v[:]) }

type fileState struct {
	Offset      int64   `json:"offset"`
	Prefix      string  `json:"prefix"`
	PrefixLen   int     `json:"prefix_len"`
	Session     string  `json:"session"`
	Fingerprint string  `json:"fingerprint"`
	Model       string  `json:"model"`
	Provider    string  `json:"provider"`
	Previous    *Tokens `json:"previous,omitempty"`
}

type collectorCache struct {
	Files       map[string]*fileState `json:"files"`
	Events      map[string]UsageEvent `json:"events"`
	Quotas      map[string]Quota      `json:"quotas"`
	Diagnostics Diagnostics           `json:"diagnostics"`
}

func Collect(ctx context.Context, c Config, now time.Time) (Snapshot, error) {
	if err := os.MkdirAll(c.DataDir, 0700); err != nil {
		return Snapshot{}, err
	}
	lock := flock.New(filepath.Join(c.DataDir, "collector.lock"))
	ok, err := lock.TryLockContext(ctx, 50*time.Millisecond)
	if err != nil {
		return Snapshot{}, err
	}
	if !ok {
		return Snapshot{}, errors.New("collector lock unavailable")
	}
	defer lock.Close()
	cachePath := filepath.Join(c.DataDir, "collector-"+hash(c.CodexHome)[:16]+".json")
	cache := collectorCache{}
	b, err := os.ReadFile(cachePath)
	if err == nil {
		if err := json.Unmarshal(b, &cache); err != nil {
			return Snapshot{}, fmt.Errorf("read collector cache: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return Snapshot{}, err
	}
	if cache.Files == nil {
		cache.Files = map[string]*fileState{}
	}
	if cache.Events == nil {
		cache.Events = map[string]UsageEvent{}
	}
	if cache.Quotas == nil {
		cache.Quotas = map[string]Quota{}
	}
	cache.Diagnostics.Errors = nil
	cutoff := now.Add(-time.Duration(c.RetentionDays) * 24 * time.Hour)
	paths := []string{}
	for _, dir := range []string{"sessions", "archived_sessions"} {
		err := filepath.WalkDir(filepath.Join(c.CodexHome, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				if os.IsNotExist(err) {
					return nil
				}
				return err
			}
			if d.Type().IsRegular() && strings.HasSuffix(d.Name(), ".jsonl") {
				info, err := d.Info()
				if err != nil {
					return err
				}
				// Existing cursors are also checked, so appends with unusual mtimes are not lost.
				if info.ModTime().After(cutoff) || cache.Files[path] != nil {
					paths = append(paths, path)
				}
			}
			return nil
		})
		if err != nil {
			return Snapshot{}, fmt.Errorf("discover Codex sessions: %w", err)
		}
	}
	sort.Strings(paths)
	for _, path := range paths {
		if err := ctx.Err(); err != nil {
			return Snapshot{}, err
		}
		if err := scanFile(ctx, path, &cache); err != nil {
			// Do not disclose paths in shared reports.
			cache.Diagnostics.Errors = append(cache.Diagnostics.Errors, "cannot read session "+hash(path)[:12]+": "+errorKind(err))
		}
	}
	cache.Diagnostics.Files = len(paths)
	for id, event := range cache.Events {
		if event.At.Before(cutoff) {
			delete(cache.Events, id)
		}
	}
	for id, q := range cache.Quotas {
		if q.At.Before(cutoff) {
			delete(cache.Quotas, id)
		}
	}
	if err := atomicJSON(cachePath, &cache); err != nil {
		return Snapshot{}, err
	}
	s := Snapshot{Version: 1, Account: c.Account, MachineID: c.MachineID, MachineName: c.MachineName,
		CollectedAt: now, HistorySince: cutoff, Events: []UsageEvent{}, Quotas: []Quota{}, Diagnostics: cache.Diagnostics}
	for _, event := range cache.Events {
		if c.AccountFingerprint == "" || event.AccountFingerprint == c.AccountFingerprint {
			s.Events = append(s.Events, event)
		}
	}
	for _, q := range cache.Quotas {
		if c.AccountFingerprint == "" || q.AccountFingerprint == c.AccountFingerprint {
			s.Quotas = append(s.Quotas, q)
		}
	}
	sort.Slice(s.Events, func(i, j int) bool { return s.Events[i].ID < s.Events[j].ID })
	sort.Slice(s.Quotas, func(i, j int) bool { return s.Quotas[i].At.Before(s.Quotas[j].At) })
	return s, nil
}

func errorKind(err error) string {
	if errors.Is(err, os.ErrPermission) {
		return "permission denied"
	}
	if errors.Is(err, os.ErrNotExist) {
		return "file disappeared"
	}
	return "I/O error"
}

func scanFile(ctx context.Context, path string, cache *collectorCache) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	state := cache.Files[path]
	if state != nil {
		prefix := make([]byte, state.PrefixLen)
		_, err := io.ReadFull(f, prefix)
		if err != nil || info.Size() < state.Offset || hash(string(prefix)) != state.Prefix {
			state = nil
		}
	}
	if state == nil {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return err
		}
		prefix := make([]byte, min(info.Size(), 4096))
		if _, err := io.ReadFull(f, prefix); err != nil {
			return err
		}
		state = &fileState{Prefix: hash(string(prefix)), PrefixLen: len(prefix), Session: hash(filepath.Base(path)), Model: "unknown"}
		cache.Files[path] = state
	}
	if _, err := f.Seek(state.Offset, io.SeekStart); err != nil {
		return err
	}
	reader := bufio.NewReaderSize(f, 64*1024)
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		line, size, complete, err := boundedLine(reader)
		if err != nil {
			return err
		}
		if !complete {
			break
		} // Writer may still be appending this line. Retry it next scan.
		state.Offset += size
		if line == nil {
			cache.Diagnostics.OversizedLines++
			continue
		}
		if !bytes.Contains(line, []byte(`"token_count"`)) && !bytes.Contains(line, []byte(`"session_meta"`)) &&
			!bytes.Contains(line, []byte(`"turn_context"`)) {
			continue
		}
		consume(line, state, cache)
	}
	return nil
}

func boundedLine(r *bufio.Reader) ([]byte, int64, bool, error) {
	const maxLine = 8 * 1024 * 1024
	var line []byte
	var size int64
	oversized := false
	for {
		part, err := r.ReadSlice('\n')
		size += int64(len(part))
		if size > maxLine {
			oversized = true
			line = nil
		}
		if !oversized {
			line = append(line, part...)
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if errors.Is(err, io.EOF) {
			return nil, size, false, nil
		}
		return line, size, err == nil, err
	}
}

func consume(line []byte, state *fileState, cache *collectorCache) {
	var record struct {
		Timestamp time.Time       `json:"timestamp"`
		Type      string          `json:"type"`
		Payload   json.RawMessage `json:"payload"`
	}
	if json.Unmarshal(line, &record) != nil {
		cache.Diagnostics.MalformedLines++
		return
	}
	switch record.Type {
	case "session_meta":
		var p struct {
			ID        string `json:"id"`
			AccountID string `json:"creator_account_id"`
			Provider  string `json:"model_provider"`
		}
		if json.Unmarshal(record.Payload, &p) != nil {
			cache.Diagnostics.MalformedLines++
			return
		}
		if p.ID != "" {
			state.Session = hash(p.ID)
		}
		if p.AccountID != "" {
			state.Fingerprint = hash("account:" + p.AccountID)
		}
		state.Provider = p.Provider
	case "turn_context":
		var p struct {
			Model string `json:"model"`
		}
		if json.Unmarshal(record.Payload, &p) == nil && p.Model != "" {
			state.Model = p.Model
		}
	case "event_msg":
		if state.Provider != "" && state.Provider != "openai" {
			return
		}
		var p struct {
			Type string `json:"type"`
			Info *struct {
				Total Tokens `json:"total_token_usage"`
				Last  Tokens `json:"last_token_usage"`
			} `json:"info"`
			Limits *struct {
				ID      string `json:"limit_id"`
				Primary *struct {
					Used    float64 `json:"used_percent"`
					Minutes int64   `json:"window_minutes"`
					Reset   int64   `json:"resets_at"`
				} `json:"primary"`
				Secondary *struct {
					Used    float64 `json:"used_percent"`
					Minutes int64   `json:"window_minutes"`
					Reset   int64   `json:"resets_at"`
				} `json:"secondary"`
			} `json:"rate_limits"`
		}
		if json.Unmarshal(record.Payload, &p) != nil {
			cache.Diagnostics.MalformedLines++
			return
		}
		if p.Type != "token_count" || record.Timestamp.IsZero() {
			return
		}
		if p.Limits != nil {
			id := p.Limits.ID
			if id == "" {
				id = "codex"
			}
			for _, w := range []*struct {
				Used    float64 `json:"used_percent"`
				Minutes int64   `json:"window_minutes"`
				Reset   int64   `json:"resets_at"`
			}{p.Limits.Primary, p.Limits.Secondary} {
				if w == nil || w.Minutes <= 0 || w.Reset <= 0 || w.Used < 0 {
					continue
				}
				key := fmt.Sprintf("%s/%s/%d", id, state.Fingerprint, w.Minutes)
				q := Quota{id, record.Timestamp, w.Used, w.Minutes, time.Unix(w.Reset, 0).UTC(), state.Fingerprint}
				if old, ok := cache.Quotas[key]; !ok || !q.At.Before(old.At) {
					cache.Quotas[key] = q
				}
			}
		}
		if p.Info == nil {
			return
		}
		total, last := p.Info.Total, p.Info.Last
		if !total.Valid() {
			cache.Diagnostics.MalformedLines++
			return
		}
		delta := last
		if state.Previous != nil {
			if total == *state.Previous {
				return
			} // Rate-limit-only repeats contain the same totals.
			delta = total.Sub(*state.Previous)
			if !delta.Valid() {
				if !last.Valid() {
					cache.Diagnostics.MalformedLines++
					return
				}
				delta = last
				cache.Diagnostics.CounterResets++
			}
		} else if !last.Valid() {
			// Context-size-only notifications may carry total_tokens without any
			// input/output counters. Establish a baseline without inventing usage.
			state.Previous = &total
			cache.Diagnostics.BootstrapEvents++
			return
		} else if total != last {
			// A resumed/truncated log can inherit a lifetime total from another device.
			// Only the last observed request is attributable at this boundary.
			cache.Diagnostics.BootstrapEvents++
		}
		state.Previous = &total
		if delta.Total == 0 {
			return
		}
		key := fmt.Sprintf("%s/%s/%d/%d/%d/%d/%d/%d", state.Session, record.Timestamp.Format(time.RFC3339Nano),
			total.Input, total.Cached, total.CacheWrite, total.Output, total.Reasoning, total.Total)
		id := hash(key)
		cache.Events[id] = UsageEvent{id, state.Session, record.Timestamp, state.Model, state.Fingerprint, delta}
	}
}
