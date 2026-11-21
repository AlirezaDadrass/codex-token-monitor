package monitor

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestHTMLIsStandaloneEscapesNamesAndContainsData(t *testing.T) {
	c := testConfig(t)
	now := time.Now().UTC()
	a, b, d := sampleSnapshot("a", 100, now), sampleSnapshot("b", 200, now), sampleSnapshot("c", 300, now)
	a.MachineName = `لپ‌تاپ </script><script>alert("xss")</script>`
	r, err := BuildReport(c, []Snapshot{a, b, d}, now, "")
	if err != nil {
		t.Fatal(err)
	}
	artifact, err := WriteHTMLReport(c, r, "")
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(artifact.Path)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{`</script><script>alert`, `src="http`, `href="http`, "ZgotmplZ"} {
		if bytes.Contains(body, []byte(forbidden)) {
			t.Fatalf("unsafe or non-standalone report: %s", forbidden)
		}
	}
	if !reportFilePattern.MatchString(filepath.Base(artifact.Path)) || artifact.LLMCalls != 0 {
		t.Fatalf("bad artifact: %+v", artifact)
	}
	const start = `<script id="report-data" type="application/json">`
	parts := strings.SplitN(string(body), start, 2)
	if len(parts) != 2 {
		t.Fatal("missing embedded counters")
	}
	data := strings.SplitN(parts[1], "</script>", 2)[0]
	var embedded Report
	if err := json.Unmarshal([]byte(data), &embedded); err != nil {
		t.Fatalf("invalid embedded JSON: %v", err)
	}
	if embedded.Totals.Total != 600 || len(embedded.Daily) == 0 || embedded.Machines[2].Name != a.MachineName {
		t.Fatal("report changed counters or names")
	}
	if len(body) > 1<<20 {
		t.Fatalf("report unexpectedly large: %d", len(body))
	}
}

func TestHTMLZeroDataAndExpiredQuota(t *testing.T) {
	c := testConfig(t)
	now := time.Now().UTC()
	r, err := BuildReport(c, nil, now, "")
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := RenderHTML(&buf, r); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "هنوز مصرفی") {
		t.Fatal("missing empty state")
	}
}

func TestReportDownloadTokenExpiryAndTampering(t *testing.T) {
	now := time.Now().UTC()
	id := "usage-20261121-1234567890abcdef.html"
	expiry := strconv.FormatInt(now.Add(time.Hour).Unix(), 10)
	signature := reportSignature(testToken, id, expiry)
	if !validReportSignature(testToken, id, expiry, signature, now) {
		t.Fatal("valid token rejected")
	}
	for _, v := range []struct {
		token, id, expiry, signature string
		at                           time.Time
	}{
		{"wrong-token", id, expiry, signature, now},
		{testToken, id, expiry, signature, now.Add(2 * time.Hour)},
		{testToken, "usage-20261121-aaaaaaaaaaaaaaaa.html", expiry, signature, now},
		{testToken, "../" + id, expiry, signature, now},
		{testToken, id, expiry, signature + "a", now},
	} {
		if validReportSignature(v.token, v.id, v.expiry, v.signature, v.at) {
			t.Fatal("accepted expired or tampered report token")
		}
	}
}
