package monitor

import "time"

type DayUsage struct {
	Date      string            `json:"date"`
	ByMachine map[string]Tokens `json:"by_machine"`
	Total     Tokens            `json:"total"`
}

// Tokens keeps provider counters intact. Cached input and reasoning output are
// subsets of input and output; adding either to Total would double count them.
type Tokens struct {
	Input      int64 `json:"input_tokens"`
	Cached     int64 `json:"cached_input_tokens"`
	CacheWrite int64 `json:"cache_write_input_tokens"`
	Output     int64 `json:"output_tokens"`
	Reasoning  int64 `json:"reasoning_output_tokens"`
	Total      int64 `json:"total_tokens"`
}

func (t Tokens) Add(v Tokens) Tokens {
	return Tokens{t.Input + v.Input, t.Cached + v.Cached, t.CacheWrite + v.CacheWrite,
		t.Output + v.Output, t.Reasoning + v.Reasoning, t.Total + v.Total}
}

func (t Tokens) Sub(v Tokens) Tokens {
	return Tokens{t.Input - v.Input, t.Cached - v.Cached, t.CacheWrite - v.CacheWrite,
		t.Output - v.Output, t.Reasoning - v.Reasoning, t.Total - v.Total}
}

func (t Tokens) Valid() bool {
	return t.Input >= 0 && t.Cached >= 0 && t.Cached <= t.Input && t.CacheWrite >= 0 &&
		t.Output >= 0 && t.Reasoning >= 0 && t.Reasoning <= t.Output && t.Total >= 0 &&
		t.Input <= 1e15 && t.Total <= 1e15 && t.Total == t.Input+t.Output
}

func (t Tokens) NonCached() int64 { return t.Input - t.Cached + t.Output }

type UsageEvent struct {
	ID                 string    `json:"id"`
	Session            string    `json:"session"` // Hash, never the original chat ID.
	At                 time.Time `json:"at"`
	Model              string    `json:"model"`
	AccountFingerprint string    `json:"account_fingerprint,omitempty"`
	Tokens             Tokens    `json:"tokens"`
}

type Quota struct {
	LimitID            string    `json:"limit_id"`
	At                 time.Time `json:"observed_at"`
	UsedPercent        float64   `json:"used_percent"`
	WindowMinutes      int64     `json:"window_minutes"`
	ResetsAt           time.Time `json:"resets_at"`
	AccountFingerprint string    `json:"account_fingerprint,omitempty"`
}

type Diagnostics struct {
	Files           int      `json:"files"`
	MalformedLines  int      `json:"malformed_lines"`
	OversizedLines  int      `json:"oversized_lines"`
	BootstrapEvents int      `json:"bootstrap_events"`
	CounterResets   int      `json:"counter_resets"`
	Errors          []string `json:"errors,omitempty"`
}

type Snapshot struct {
	Version      int          `json:"version"`
	Account      string       `json:"account"`
	MachineID    string       `json:"machine_id"`
	MachineName  string       `json:"machine_name"`
	CollectedAt  time.Time    `json:"collected_at"`
	HistorySince time.Time    `json:"history_since"`
	Events       []UsageEvent `json:"events"`
	Quotas       []Quota      `json:"quotas"`
	Diagnostics  Diagnostics  `json:"diagnostics"`
}
