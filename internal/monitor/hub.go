package monitor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const maxSnapshotBytes = 32 << 20

func HubHandler(c Config, token string) http.Handler {
	store := Store{Dir: filepath.Join(c.DataDir, "hub")}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/snapshots", func(w http.ResponseWriter, r *http.Request) {
		r.Body = http.MaxBytesReader(w, r.Body, maxSnapshotBytes)
		var s Snapshot
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&s); err != nil {
			http.Error(w, "invalid or oversized snapshot", 400)
			return
		}
		var extra any
		if decoder.Decode(&extra) != io.EOF {
			http.Error(w, "expected one JSON object", 400)
			return
		}
		if err := validateSnapshot(s, c.Account, time.Now().UTC()); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		accepted, err := store.Put(r.Context(), s)
		if err != nil {
			http.Error(w, "cannot persist snapshot", 500)
			return
		}
		writeJSON(w, map[string]bool{"accepted": accepted})
	})
	mux.HandleFunc("GET /v1/report", func(w http.ResponseWriter, r *http.Request) {
		snapshots, err := store.List(c.Account)
		if err != nil {
			http.Error(w, "cannot read snapshots", 500)
			return
		}
		report, err := BuildReport(c, snapshots, time.Now().UTC(), r.URL.Query().Get("metric"))
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		report.Scope = "hub"
		writeJSON(w, report)
	})
	mux.Handle("/mcp", mcp.NewStreamableHTTPHandler(func(r *http.Request) *mcp.Server {
		scheme := "http"
		if r.TLS != nil {
			scheme = "https"
		}
		origin := scheme + "://" + r.Host
		if c.PublicURL != "" {
			origin = c.PublicURL
		}
		return newMCPServer(c, true, origin)
	}, nil))
	mux.HandleFunc("GET /reports/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if !reportFilePattern.MatchString(id) {
			http.NotFound(w, r)
			return
		}
		f, err := os.Open(filepath.Join(c.DataDir, "reports", id))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		info, err := f.Stat()
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; font-src data:; img-src data:; connect-src 'none'; base-uri 'none'; frame-ancestors 'none'")
		http.ServeContent(w, r, id, info.ModTime(), f)
	})
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, map[string]string{"status": "ok"}) })
	want := sha256.Sum256([]byte("Bearer " + token))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		provided := sha256.Sum256([]byte(r.Header.Get("Authorization")))
		signed := strings.HasPrefix(r.URL.Path, "/reports/") && validReportSignature(token, strings.TrimPrefix(r.URL.Path, "/reports/"), r.URL.Query().Get("expires"), r.URL.Query().Get("signature"), time.Now().UTC())
		if subtle.ConstantTimeCompare(want[:], provided[:]) != 1 && !signed {
			w.Header().Set("WWW-Authenticate", `Bearer realm="token-monitor"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		mux.ServeHTTP(w, r)
	})
}

func validateSnapshot(s Snapshot, account string, now time.Time) error {
	validText := func(v string) bool { return len(v) > 0 && len(v) <= 200 }
	if s.Version != 1 || s.Account != account || !validText(s.MachineID) || !validText(s.MachineName) ||
		s.CollectedAt.IsZero() || s.CollectedAt.After(now.Add(5*time.Minute)) ||
		s.HistorySince.IsZero() || s.HistorySince.After(s.CollectedAt) || len(s.Events) > 100000 || len(s.Quotas) > 100 {
		return errors.New("invalid snapshot metadata or account mismatch")
	}
	isHash := func(v string) bool { b, err := hex.DecodeString(v); return err == nil && len(b) == 32 }
	seen := map[string]bool{}
	for _, e := range s.Events {
		if !isHash(e.ID) || !isHash(e.Session) || (e.AccountFingerprint != "" && !isHash(e.AccountFingerprint)) ||
			!validText(e.Model) || !e.Tokens.Valid() || e.At.Before(s.HistorySince) || e.At.After(s.CollectedAt.Add(time.Minute)) || seen[e.ID] {
			return errors.New("invalid or duplicate usage event")
		}
		seen[e.ID] = true
	}
	for _, q := range s.Quotas {
		if !validText(q.LimitID) || q.WindowMinutes <= 0 || q.WindowMinutes > 525600 || q.UsedPercent < 0 ||
			q.UsedPercent > 100000 || q.At.IsZero() || q.At.After(s.CollectedAt.Add(time.Minute)) || q.ResetsAt.IsZero() ||
			(q.AccountFingerprint != "" && !isHash(q.AccountFingerprint)) {
			return errors.New("invalid quota observation")
		}
	}
	return nil
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func hubRequest(ctx context.Context, c Config, method, path string, body any, out any) error {
	token, err := c.Token()
	if err != nil {
		return err
	}
	var data []byte
	if body != nil {
		data, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	if len(data) > maxSnapshotBytes {
		return errors.New("snapshot exceeds 32 MiB; reduce retention_days")
	}
	req, err := http.NewRequestWithContext(ctx, method, c.HubURL+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 30 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return errors.New("hub redirects are not permitted")
	}}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("hub is unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("hub returned HTTP %d: %s", resp.StatusCode, strings.ReplaceAll(string(b), token, "[redacted]"))
	}
	return json.NewDecoder(io.LimitReader(resp.Body, maxSnapshotBytes)).Decode(out)
}

func CheckHub(ctx context.Context, c Config) error {
	var result struct {
		Status string `json:"status"`
	}
	if err := hubRequest(ctx, c, http.MethodGet, "/health", nil, &result); err != nil {
		return err
	}
	if result.Status != "ok" {
		return errors.New("hub health check did not return ok")
	}
	return nil
}

func Refresh(ctx context.Context, c Config) (Snapshot, error) {
	snapshot, err := Collect(ctx, c, time.Now().UTC())
	if err != nil {
		return Snapshot{}, err
	}
	_, err = (Store{Dir: filepath.Join(c.DataDir, "snapshots")}).Put(ctx, snapshot)
	if err != nil {
		return snapshot, err
	}
	if c.HubURL != "" {
		var result struct {
			Accepted bool `json:"accepted"`
		}
		if err := hubRequest(ctx, c, http.MethodPost, "/v1/snapshots", snapshot, &result); err != nil {
			return snapshot, err
		}
		if !result.Accepted {
			return snapshot, errors.New("hub kept a newer snapshot; check this computer's clock and machine_id")
		}
	}
	return snapshot, nil
}

func GetReport(ctx context.Context, c Config, hubMode bool, metric string, refresh bool) (Report, error) {
	var syncErr error
	if refresh && !hubMode {
		_, syncErr = Refresh(ctx, c)
	}
	if c.HubURL != "" && !hubMode && syncErr == nil {
		var report Report
		err := hubRequest(ctx, c, http.MethodGet, "/v1/report?metric="+url.QueryEscape(metric), nil, &report)
		if err == nil {
			return report, nil
		}
		syncErr = err
	}
	dir := "snapshots"
	if hubMode {
		dir = "hub"
	}
	snapshots, err := (Store{Dir: filepath.Join(c.DataDir, dir)}).List(c.Account)
	if err != nil {
		return Report{}, err
	}
	r, err := BuildReport(c, snapshots, time.Now().UTC(), metric)
	if hubMode {
		r.Scope = "hub"
	}
	if syncErr != nil {
		r.Scope = "local_fallback"
		r.CoverageComplete = false
		for i := range r.Machines {
			r.Machines[i].EstimatedQuotaPoints = nil
		}
		r.Warnings = append([]string{"Hub sync or local collection failed: " + syncErr.Error() + ". This is a cached local report, not the cross-computer total."}, r.Warnings...)
	}
	return r, err
}
