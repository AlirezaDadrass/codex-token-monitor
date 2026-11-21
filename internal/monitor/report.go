package monitor

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
	_ "time/tzdata"
)

type MachineReport struct {
	ID                   string            `json:"machine_id"`
	Name                 string            `json:"name"`
	LastSeen             time.Time         `json:"last_seen"`
	Stale                bool              `json:"stale"`
	Tokens               Tokens            `json:"tokens"`
	NonCachedTokens      int64             `json:"non_cached_tokens"`
	SharePercent         float64           `json:"share_of_observed_tokens_percent"`
	EstimatedQuotaPoints *float64          `json:"estimated_weekly_limit_percentage_points"`
	Models               map[string]Tokens `json:"by_model"`
	Sessions             int               `json:"sessions"`
	Diagnostics          Diagnostics       `json:"diagnostics"`
}

type QuotaReport struct {
	Quota
	RemainingPercent float64 `json:"remaining_percent"`
	Stale            bool    `json:"stale"`
	Active           bool    `json:"active"`
}

type Report struct {
	Account           string          `json:"account"`
	GeneratedAt       time.Time       `json:"generated_at"`
	Scope             string          `json:"scope"`
	WindowSource      string          `json:"window_source"`
	WindowStart       time.Time       `json:"window_start"`
	WindowEnd         time.Time       `json:"window_end"`
	RankingMetric     string          `json:"ranking_metric"`
	WeeklyQuota       *QuotaReport    `json:"account_weekly_quota"`
	ExpectedMachines  int             `json:"expected_machines"`
	ReportingMachines int             `json:"reporting_machines"`
	FreshMachines     int             `json:"fresh_machines"`
	CoverageComplete  bool            `json:"coverage_complete"`
	Totals            Tokens          `json:"observed_tokens"`
	Unattributed      Tokens          `json:"unattributed_tokens"`
	ConflictingEvents int             `json:"conflicting_events"`
	Machines          []MachineReport `json:"machines"`
	Daily             []DayUsage      `json:"daily"`
	Warnings          []string        `json:"warnings"`
}

func BuildReport(c Config, snapshots []Snapshot, now time.Time, metric string) (Report, error) {
	if metric == "" {
		metric = "total_tokens"
	}
	if metric != "total_tokens" && metric != "non_cached_tokens" {
		return Report{}, errors.New("metric must be total_tokens or non_cached_tokens")
	}
	r := Report{Account: c.Account, GeneratedAt: now, Scope: "local", WindowSource: "rolling_7_days",
		WindowStart: now.Add(-7 * 24 * time.Hour), WindowEnd: now, RankingMetric: metric,
		ExpectedMachines: c.ExpectedMachines, Machines: []MachineReport{}, Warnings: []string{}}
	byDevice := map[string]Snapshot{}
	for _, s := range snapshots {
		if s.Account != c.Account {
			continue
		}
		if old, ok := byDevice[s.MachineID]; !ok || s.CollectedAt.After(old.CollectedAt) {
			byDevice[s.MachineID] = s
		}
	}
	devices := make([]Snapshot, 0, len(byDevice))
	for _, s := range byDevice {
		devices = append(devices, s)
	}
	sort.Slice(devices, func(i, j int) bool { return devices[i].MachineID < devices[j].MachineID })
	fingerprints := map[string]bool{}
	var latest *Quota
	for _, s := range devices {
		for _, e := range s.Events {
			if !e.At.Before(r.WindowStart) && e.AccountFingerprint != "" {
				fingerprints[e.AccountFingerprint] = true
			}
		}
		for _, q := range s.Quotas {
			if q.LimitID != c.LimitID || q.WindowMinutes != 10080 {
				continue
			}
			if q.AccountFingerprint != "" {
				fingerprints[q.AccountFingerprint] = true
			}
			if latest == nil || q.At.After(latest.At) {
				qCopy := q
				latest = &qCopy
			}
		}
	}
	if len(fingerprints) > 1 {
		ids := []string{}
		for id := range fingerprints {
			ids = append(ids, id)
		}
		sort.Strings(ids)
		return r, fmt.Errorf("multiple OpenAI accounts detected; set account_fingerprint in each collector config to one of: %s", strings.Join(ids, ", "))
	}
	if latest != nil {
		start := latest.ResetsAt.Add(-7 * 24 * time.Hour)
		active := !now.Before(start) && now.Before(latest.ResetsAt)
		r.WeeklyQuota = &QuotaReport{Quota: *latest, RemainingPercent: max(0, 100-latest.UsedPercent),
			Stale: now.Sub(latest.At) > staleAfter(c), Active: active}
		if active {
			r.WindowStart = start
			r.WindowEnd = latest.ResetsAt
			r.WindowSource = "account_weekly_reset"
		}
		if !active {
			r.Warnings = append(r.Warnings, "The last weekly quota snapshot has expired; token totals use the last 7 days, and current quota is unknown.")
		} else if r.WeeklyQuota.Stale {
			r.Warnings = append(r.Warnings, "Account quota is a stale observation; it has not been refreshed from OpenAI.")
		}
		if active && latest.UsedPercent >= 100 {
			r.Warnings = append(r.Warnings, "The weekly allowance has been reached. Token counts may include continued use with purchased credits; per-device allowance estimates are unavailable.")
		}
	} else {
		r.Warnings = append(r.Warnings, "No weekly quota was found for the configured limit_id; using the last 7 days.")
	}
	r.ReportingMachines = len(devices)
	indexes := map[string]int{}
	for _, s := range devices {
		m := MachineReport{ID: s.MachineID, Name: s.MachineName, LastSeen: s.CollectedAt,
			Stale: now.Sub(s.CollectedAt) > staleAfter(c), Models: map[string]Tokens{}, Diagnostics: s.Diagnostics}
		if !m.Stale {
			r.FreshMachines++
		}
		indexes[m.ID] = len(r.Machines)
		r.Machines = append(r.Machines, m)
	}
	type claim struct {
		event    UsageEvent
		machines map[string]bool
		mismatch bool
	}
	claims := map[string]*claim{}
	for _, s := range devices {
		for _, e := range s.Events {
			if e.At.Before(r.WindowStart) || !e.At.Before(r.WindowEnd) || e.At.After(now) {
				continue
			}
			if old := claims[e.ID]; old != nil {
				old.machines[s.MachineID] = true
				old.mismatch = old.mismatch || old.event.Tokens != e.Tokens
			} else {
				claims[e.ID] = &claim{event: e, machines: map[string]bool{s.MachineID: true}}
			}
		}
	}
	sessions := map[string]map[string]bool{}
	location, _ := time.LoadLocation("Asia/Tehran")
	days := map[string]*DayUsage{}
	startDay := r.WindowStart.In(location)
	startDay = time.Date(startDay.Year(), startDay.Month(), startDay.Day(), 0, 0, 0, 0, location)
	for day := startDay; !day.After(now.In(location)); day = day.AddDate(0, 0, 1) {
		date := day.Format("2006-01-02")
		days[date] = &DayUsage{Date: date, ByMachine: map[string]Tokens{}}
	}
	for _, claim := range claims {
		e := claim.event
		r.Totals = r.Totals.Add(e.Tokens)
		date := e.At.In(location).Format("2006-01-02")
		if days[date] == nil {
			days[date] = &DayUsage{Date: date, ByMachine: map[string]Tokens{}}
		}
		days[date].Total = days[date].Total.Add(e.Tokens)
		if len(claim.machines) > 1 || claim.mismatch {
			r.Unattributed = r.Unattributed.Add(e.Tokens)
			r.ConflictingEvents++
			continue
		}
		for id := range claim.machines {
			days[date].ByMachine[id] = days[date].ByMachine[id].Add(e.Tokens)
			m := &r.Machines[indexes[id]]
			m.Tokens = m.Tokens.Add(e.Tokens)
			m.Models[e.Model] = m.Models[e.Model].Add(e.Tokens)
			if sessions[id] == nil {
				sessions[id] = map[string]bool{}
			}
			sessions[id][e.Session] = true
		}
	}
	for _, day := range days {
		r.Daily = append(r.Daily, *day)
	}
	sort.Slice(r.Daily, func(i, j int) bool { return r.Daily[i].Date < r.Daily[j].Date })
	r.CoverageComplete = r.ReportingMachines == c.ExpectedMachines && r.FreshMachines == c.ExpectedMachines && r.ConflictingEvents == 0
	for _, s := range devices {
		d := s.Diagnostics
		if s.HistorySince.After(r.WindowStart) || d.Files == 0 || d.MalformedLines > 0 || d.OversizedLines > 0 ||
			d.BootstrapEvents > 0 || len(d.Errors) > 0 {
			r.CoverageComplete = false
		}
	}
	if !r.CoverageComplete {
		r.Warnings = append(r.Warnings, "Coverage is incomplete: check device freshness, expected count, and collection diagnostics. Shares cover observed local logs only.")
	}
	if r.ConflictingEvents > 0 {
		r.Warnings = append(r.Warnings, "Some events appear on multiple computers (copied/synced logs). They are counted once in totals and left unattributed.")
	}
	if len(fingerprints) == 0 {
		r.Warnings = append(r.Warnings, "Source logs do not identify the OpenAI account; grouping relies on the configured account label.")
	}
	value := func(t Tokens) int64 {
		if metric == "non_cached_tokens" {
			return t.NonCached()
		}
		return t.Total
	}
	denominator := value(r.Totals)
	for i := range r.Machines {
		m := &r.Machines[i]
		m.NonCachedTokens = m.Tokens.NonCached()
		m.Sessions = len(sessions[m.ID])
		if denominator > 0 {
			m.SharePercent = 100 * float64(value(m.Tokens)) / float64(denominator)
		}
		if denominator > 0 && r.CoverageComplete && r.WeeklyQuota != nil && r.WeeklyQuota.Active && !r.WeeklyQuota.Stale && r.WeeklyQuota.UsedPercent < 100 {
			estimate := m.SharePercent / 100 * r.WeeklyQuota.UsedPercent
			m.EstimatedQuotaPoints = &estimate
		}
	}
	sort.Slice(r.Machines, func(i, j int) bool {
		if value(r.Machines[i].Tokens) == value(r.Machines[j].Tokens) {
			return r.Machines[i].ID < r.Machines[j].ID
		}
		return value(r.Machines[i].Tokens) > value(r.Machines[j].Tokens)
	})
	r.Warnings = append(r.Warnings, "Per-device quota percentage points are heuristic, not OpenAI billing attribution. Models, caching, speed, purchased credits, cloud tasks, and other shared features can change allowance consumption. Raw token shares do not establish exact quota shares.")
	return r, nil
}
