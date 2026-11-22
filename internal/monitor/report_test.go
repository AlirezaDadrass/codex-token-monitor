package monitor

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func sampleSnapshot(id string, total int64, now time.Time) Snapshot {
	return Snapshot{Version: 1, Account: "personal", MachineID: id, MachineName: id, CollectedAt: now,
		HistorySince: now.Add(-14 * 24 * time.Hour), Diagnostics: Diagnostics{Files: 1},
		Events: []UsageEvent{{ID: hash(id + "event"), Session: hash(id + "session"), At: now.Add(-time.Hour), Model: "test", Tokens: tokens(total, 0, 0, 0)}},
		Quotas: []Quota{{LimitID: "codex", At: now, UsedPercent: 60, WindowMinutes: 10080, ResetsAt: now.Add(time.Hour)}}}
}

func TestThreeComputersWeekWindowRankingAndSharedQuota(t *testing.T) {
	c := testConfig(t)
	now := time.Now().UTC()
	a, b, d := sampleSnapshot("a", 100, now), sampleSnapshot("b", 300, now), sampleSnapshot("c", 600, now)
	// The newest global observation wins. Shared percentages are never summed.
	a.Quotas[0].UsedPercent = 59
	a.Quotas[0].At = now.Add(-time.Second)
	old := a.Events[0]
	old.ID = hash("before-week")
	old.At = now.Add(-8 * 24 * time.Hour)
	a.Events = append(a.Events, old)
	r, err := BuildReport(c, []Snapshot{a, b, d}, now, "total_tokens")
	if err != nil {
		t.Fatal(err)
	}
	if r.Totals.Total != 1000 || r.Machines[0].Name != "c" || r.Machines[0].SharePercent != 60 || r.WeeklyQuota.UsedPercent != 60 || !r.CoverageComplete {
		t.Fatalf("bad report: %+v", r)
	}
	if p := r.Machines[0].EstimatedQuotaPoints; p == nil || *p != 36 {
		t.Fatalf("wrong quota estimate: %+v", p)
	}
	if !r.WindowStart.Equal(now.Add(time.Hour - 7*24*time.Hour)) {
		t.Fatal("did not align with account reset")
	}
	// Changing metric accounts for cached input without double counting reasoning.
	a.Events[0].Tokens = tokens(1000, 990, 10, 8)
	r, err = BuildReport(c, []Snapshot{a, b, d}, now, "non_cached_tokens")
	if err != nil {
		t.Fatal(err)
	}
	if r.Machines[0].Name != "c" || r.Totals.Total != 1910 {
		t.Fatalf("cache metric wrong: %+v", r)
	}
}

func TestDuplicateLogsAreUnattributedAndNotDoubleCounted(t *testing.T) {
	c := testConfig(t)
	now := time.Now().UTC()
	a, b, d := sampleSnapshot("a", 100, now), sampleSnapshot("b", 200, now), sampleSnapshot("c", 300, now)
	b.Events = append(b.Events, a.Events[0])
	r, err := BuildReport(c, []Snapshot{a, b, d}, now, "")
	if err != nil {
		t.Fatal(err)
	}
	if r.Totals.Total != 600 || r.Unattributed.Total != 100 || r.ConflictingEvents != 1 || r.CoverageComplete {
		t.Fatalf("duplicate attribution wrong: %+v", r)
	}
	for _, m := range r.Machines {
		if m.EstimatedQuotaPoints != nil {
			t.Fatal("estimated quota despite ambiguous provenance")
		}
	}
}

func TestExpiredQuotaMissingAndStaleComputers(t *testing.T) {
	c := testConfig(t)
	now := time.Now().UTC()
	a := sampleSnapshot("a", 100, now)
	a.Quotas[0].ResetsAt = now.Add(-time.Hour)
	a.CollectedAt = now.Add(-time.Hour)
	r, err := BuildReport(c, []Snapshot{a}, now, "")
	if err != nil {
		t.Fatal(err)
	}
	if r.WeeklyQuota.Active || r.CoverageComplete || r.WindowSource != "rolling_7_days" || r.FreshMachines != 0 || r.Machines[0].EstimatedQuotaPoints != nil {
		t.Fatalf("expired limit used as current: %+v", r)
	}
	a.Quotas = nil
	r, err = BuildReport(c, []Snapshot{a}, now, "")
	if err != nil {
		t.Fatal(err)
	}
	if r.WeeklyQuota != nil {
		t.Fatal("invented quota")
	}
}

func TestOtherAccountsAndLimitBuckets(t *testing.T) {
	c := testConfig(t)
	now := time.Now().UTC()
	a := sampleSnapshot("a", 100, now)
	b := sampleSnapshot("b", 900, now)
	b.Account = "work"
	a.Quotas[0].LimitID = "another-model"
	r, err := BuildReport(c, []Snapshot{a, b}, now, "")
	if err != nil {
		t.Fatal(err)
	}
	if r.Totals.Total != 100 || r.WeeklyQuota != nil || r.ReportingMachines != 1 {
		t.Fatalf("mixed account or bucket: %+v", r)
	}
	b.Account = c.Account
	a.Events[0].AccountFingerprint = hash("a")
	b.Events[0].AccountFingerprint = hash("b")
	r, err = BuildReport(c, []Snapshot{a, b}, now, "")
	if err != nil || r.Totals.Total != 1000 || r.ReportingMachines != 2 || !r.AccountIdentityAmbiguous || r.WeeklyQuota != nil || r.WindowSource != "rolling_7_days" {
		t.Fatalf("identity metadata blocked device counters or combined quotas: %+v %v", r, err)
	}
	for _, m := range r.Machines {
		if m.EstimatedQuotaPoints != nil {
			t.Fatal("estimated a shared quota from differing identities")
		}
	}
}

func TestSessionBreakdownContinuationDuplicatesAndMetrics(t *testing.T) {
	c := testConfig(t)
	now := time.Now().UTC()
	a, b := sampleSnapshot("laptop", 100, now), sampleSnapshot("desktop", 200, now)
	// One copied event stays unattributed; the rest of a continued session is
	// attributed to the computer that observed it, never to its creator.
	sharedSession := a.Events[0].Session
	b.Events[0].Session = sharedSession
	b.Events = append(b.Events, a.Events[0])
	more := a.Events[0]
	more.ID, more.At, more.Tokens = hash("later"), now.Add(-30*time.Minute), tokens(30, 20, 5, 0)
	a.Events = append(a.Events, more)
	another := more
	another.ID, another.Session, another.Tokens = hash("another"), hash("another-session"), tokens(50, 0, 0, 0)
	a.Events = append(a.Events, another)
	old := more
	old.ID, old.At = hash("old-session-event"), now.Add(-8*24*time.Hour)
	a.Events = append(a.Events, old)
	for _, metric := range []string{"total_tokens", "non_cached_tokens"} {
		r, err := BuildReport(c, []Snapshot{a, b}, now, metric)
		if err != nil {
			t.Fatal(err)
		}
		if r.Totals.Total != 385 || r.Unattributed.Total != 100 || len(r.Sessions) != 4 {
			t.Fatalf("wrong session split: %+v", r)
		}
		var total Tokens
		var share float64
		for _, s := range r.Sessions {
			total = total.Add(s.Tokens)
			share += s.SharePercent
			if s.EstimatedQuotaPoints != nil {
				t.Fatal("estimated quota despite duplicated history")
			}
			if s.ID == sharedSession && s.MachineID == "laptop" && (s.Tokens.Total != 35 || !s.FirstSeen.Equal(more.At) || !s.LastSeen.Equal(more.At)) {
				t.Fatalf("continued session totals/timestamps wrong: %+v", s)
			}
		}
		if total != r.Totals || share < 99.999999 || share > 100.000001 {
			t.Fatal("session rows do not reconcile to observed total")
		}
		if r.Machines[0].Sessions != 1 || r.Machines[1].Sessions != 2 {
			t.Fatal("device session count changed")
		}
		if metric == "non_cached_tokens" && r.Sessions[3].NonCachedTokens != 15 {
			t.Fatalf("session metric double-counted cache/reasoning: %+v", r.Sessions[3])
		}
	}
}

func TestSharedQuotaSessionEstimatesAndIdentityMismatch(t *testing.T) {
	c := testConfig(t)
	now := time.Now().UTC()
	a, b := sampleSnapshot("a", 100, now), sampleSnapshot("b", 300, now)
	for _, s := range []*Snapshot{&a, &b} {
		s.Events[0].AccountFingerprint = hash("shared-account")
		s.Quotas[0].AccountFingerprint = hash("shared-account")
	}
	r, err := BuildReport(c, []Snapshot{a, b}, now, "")
	if err != nil || len(r.Sessions) != 2 || r.AccountIdentityAmbiguous || r.WeeklyQuota == nil || r.Sessions[0].EstimatedQuotaPoints == nil || *r.Sessions[0].EstimatedQuotaPoints != 45 {
		t.Fatalf("single-account quota/session estimate failed: %+v %v", r, err)
	}
	b.Events[0].AccountFingerprint = hash("different-log-metadata")
	r, err = BuildReport(c, []Snapshot{a, b}, now, "")
	if err != nil || r.Totals.Total != 400 || r.WeeklyQuota != nil || !r.AccountIdentityAmbiguous || len(r.Sessions) != 2 {
		t.Fatalf("log identity mismatch stopped reporting: %+v %v", r, err)
	}
	for _, s := range r.Sessions {
		if s.EstimatedQuotaPoints != nil {
			t.Fatal("invented shared allowance")
		}
	}
}

func TestStoreRetriesOlderUploadsAndConcurrentDevices(t *testing.T) {
	c := testConfig(t)
	now := time.Now().UTC()
	s := Store{Dir: filepath.Join(c.DataDir, "hub")}
	a := sampleSnapshot("a", 100, now)
	for i := 0; i < 2; i++ {
		if _, err := s.Put(context.Background(), a); err != nil {
			t.Fatal(err)
		}
	}
	old := a
	old.CollectedAt = now.Add(-time.Hour)
	old.Events = nil
	accepted, err := s.Put(context.Background(), old)
	if err != nil || accepted {
		t.Fatalf("older snapshot accepted: %v %v", accepted, err)
	}
	rows, err := s.List(c.Account)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || len(rows[0].Events) != 1 {
		t.Fatal("retry duplicated or erased usage")
	}
}

func TestReachedAllowanceDoesNotEstimatePaidUsage(t *testing.T) {
	c := testConfig(t)
	now := time.Now().UTC()
	a, b, d := sampleSnapshot("a", 100, now), sampleSnapshot("b", 200, now), sampleSnapshot("c", 300, now)
	for _, s := range []*Snapshot{&a, &b, &d} {
		s.Quotas[0].UsedPercent = 100
	}
	r, err := BuildReport(c, []Snapshot{a, b, d}, now, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range r.Machines {
		if m.EstimatedQuotaPoints != nil {
			t.Fatal("estimated allowance from possible credit-funded usage")
		}
	}
}

func TestRegisteredDeviceCountDoesNotLimitCoverageOrEstimates(t *testing.T) {
	for _, count := range []int{1, 2, 4, 101} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			c := testConfig(t)
			now := time.Now().UTC()
			devices := make([]Snapshot, 0, count)
			for i := 0; i < count; i++ {
				devices = append(devices, sampleSnapshot(fmt.Sprintf("device-%d", i), 100, now))
			}
			r, err := BuildReport(c, devices, now, "")
			if err != nil {
				t.Fatal(err)
			}
			if r.ReportingMachines != count || len(r.Machines) != count || !r.CoverageComplete || r.Totals.Total != int64(count)*100 {
				t.Fatalf("device count changed coverage/totals: %+v", r)
			}
			var estimated float64
			for _, m := range r.Machines {
				if m.EstimatedQuotaPoints == nil {
					t.Fatal("arbitrary device count prevented quota estimate")
				}
				estimated += *m.EstimatedQuotaPoints
			}
			if estimated < 59.999999 || estimated > 60.000001 {
				t.Fatalf("estimates do not preserve shared quota: %v", estimated)
			}
			body, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(body), "expected_machines") {
				t.Fatal("report still exposes an assumed fleet size")
			}
		})
	}
}

func TestNoRegisteredDevicesDoesNotImplyCompleteCoverage(t *testing.T) {
	r, err := BuildReport(testConfig(t), nil, time.Now().UTC(), "")
	if err != nil {
		t.Fatal(err)
	}
	if r.ReportingMachines != 0 || r.CoverageComplete {
		t.Fatal("empty report claims full observed coverage")
	}
}
