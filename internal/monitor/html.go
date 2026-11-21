package monitor

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

//go:embed templates/report.html
var reportTemplate string

//go:embed templates/Vazirmatn.ttf
var reportFont []byte

//go:embed templates/OFL.txt
var reportFontLicense string

var reportFilePattern = regexp.MustCompile(`^usage-[0-9]{8}-[a-f0-9]{16}\.html$`)

type HTMLArtifact struct {
	Path              string    `json:"file_path"`
	URL               string    `json:"url,omitempty"`
	DisplayMarkdown   string    `json:"display_markdown"`
	GeneratedAt       time.Time `json:"generated_at"`
	ReportingMachines int       `json:"reporting_machines"`
	LLMCalls          int       `json:"llm_calls"`
}

func WriteHTMLReport(c Config, report Report, output string) (HTMLArtifact, error) {
	if output == "" {
		var id [8]byte
		if _, err := rand.Read(id[:]); err != nil {
			return HTMLArtifact{}, err
		}
		output = filepath.Join(c.DataDir, "reports", "usage-"+report.GeneratedAt.Format("20060102")+"-"+hex.EncodeToString(id[:])+".html")
	}
	path, err := filepath.Abs(output)
	if err != nil {
		return HTMLArtifact{}, err
	}
	var buffer bytes.Buffer
	if err := RenderHTML(&buffer, report); err != nil {
		return HTMLArtifact{}, err
	}
	if err := atomicWrite(path, buffer.Bytes()); err != nil {
		return HTMLArtifact{}, err
	}
	return HTMLArtifact{Path: path, DisplayMarkdown: "[مشاهدهٔ گزارش مصرف کدکس](<" + path + ">)", GeneratedAt: report.GeneratedAt,
		ReportingMachines: report.ReportingMachines, LLMCalls: 0}, nil
}

func RenderHTML(w io.Writer, report Report) error {
	data, err := json.Marshal(report)
	if err != nil {
		return err
	}
	view := struct {
		Report      Report
		JSON        template.JS
		Notes       []string
		Font        template.URL
		FontLicense string
	}{Report: report, JSON: template.JS(data), Notes: reportNotes(report),
		Font: template.URL("data:font/ttf;base64," + base64.StdEncoding.EncodeToString(reportFont)), FontLicense: reportFontLicense}
	t, err := template.New("usage").Funcs(template.FuncMap{
		"number":     exactNumber,
		"short":      shortNumber,
		"percent":    func(v float64) string { return persianNumber(strconv.FormatFloat(v, 'f', 1, 64)) + "٪" },
		"indexColor": func(i int) string { return []string{"#156d62", "#4d69d8", "#ce8235", "#9a62bd", "#d45f78"}[i%5] },
		"counter":    func(i int) string { return persianNumber(strconv.Itoa(i + 1)) },
	}).Parse(reportTemplate)
	if err != nil {
		return err
	}
	return t.Execute(w, view)
}

func exactNumber(n int64) string {
	s := strconv.FormatInt(n, 10)
	var out strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			out.WriteRune('٬')
		}
		out.WriteRune(c)
	}
	return persianNumber(out.String())
}

func shortNumber(n int64) string {
	for _, unit := range []struct {
		n     int64
		label string
	}{{1e9, "میلیارد"}, {1e6, "میلیون"}, {1e3, "هزار"}} {
		if n >= unit.n {
			return persianNumber(strconv.FormatFloat(float64(n)/float64(unit.n), 'f', 2, 64)) + " " + unit.label
		}
	}
	return persianNumber(strconv.FormatInt(n, 10))
}

func persianNumber(s string) string {
	out := []rune(s)
	for i, c := range out {
		if c >= '0' && c <= '9' {
			out[i] = '۰' + c - '0'
		} else if c == '.' {
			out[i] = '٫'
		}
	}
	return string(out)
}

func reportNotes(r Report) []string {
	notes := []string{}
	if r.Scope == "local_fallback" {
		notes = append(notes, "ارتباط با مرکز یا خواندن داده‌ها موفق نبود؛ این فایل فقط آمار محلیِ ذخیره‌شده را نشان می‌دهد.")
	}
	if !r.CoverageComplete {
		notes = append(notes, "پوشش داده‌ها کامل نیست. تعداد دستگاه‌های گزارش‌دهنده، زمان همگام‌سازی و سابقهٔ موجود را در این گزارش بررسی کنید.")
	}
	if r.WeeklyQuota == nil || !r.WeeklyQuota.Active {
		notes = append(notes, "درصد معتبر سهمیهٔ هفتهٔ جاری در دسترس نیست؛ شمارش توکن‌ها مربوط به هفت روز اخیر است.")
	} else {
		if r.WeeklyQuota.Stale {
			notes = append(notes, "درصد سهمیه مربوط به آخرین مشاهدهٔ کدکس است و ممکن است از وضعیت فعلی عقب‌تر باشد.")
		}
		if r.WeeklyQuota.UsedPercent >= 100 {
			notes = append(notes, "لیمیت هفتگی به پایان رسیده است. مصرف پس از آن ممکن است با اعتبار خریداری‌شده انجام شده باشد؛ سهم دستگاه‌ها از لیمیت تخمین زده نمی‌شود.")
		}
	}
	if r.ConflictingEvents > 0 {
		notes = append(notes, "بخشی از سابقه روی چند دستگاه تکرار شده است. این مصرف فقط یک‌بار در مجموع محاسبه شده و به دستگاه خاصی نسبت داده نشده است.")
	}
	for _, m := range r.Machines {
		if m.Diagnostics.BootstrapEvents > 0 {
			notes = append(notes, fmt.Sprintf("در دستگاه «%s» سابقهٔ کامل بخشی از چت‌ها موجود نیست؛ تنها درخواست‌های قابل مشاهده محاسبه شده‌اند.", m.Name))
		}
		if m.Diagnostics.MalformedLines > 0 || m.Diagnostics.OversizedLines > 0 || len(m.Diagnostics.Errors) > 0 {
			notes = append(notes, fmt.Sprintf("خواندن بخشی از داده‌های دستگاه «%s» ممکن نبود؛ اعداد آن را با در نظر گرفتن این محدودیت بخوانید.", m.Name))
		}
	}
	notes = append(notes, "سهم هر دستگاه از توکن‌های ثبت‌شده محاسبه می‌شود. تخمین سهم از لیمیت، اندازه‌گیری مستقیم OpenAI نیست؛ مدل، کش، سرعت و مصرف ابری می‌توانند نتیجه را تغییر دهند.",
		"ورودی کش‌شده بخشی از ورودی و reasoning بخشی از خروجی است؛ هیچ‌کدام دوباره به جمع کل اضافه نشده‌اند. مصرف دستگاه‌های متصل‌نشده و کارهای ابری در آمار محلی دیده نمی‌شود.")
	return notes
}

func reportSignature(token, id, expiry string) string {
	h := hmac.New(sha256.New, []byte(token))
	_, _ = io.WriteString(h, id+"/"+expiry)
	return hex.EncodeToString(h.Sum(nil))
}

func signedReportURL(base, token, id string, now time.Time) string {
	expiry := strconv.FormatInt(now.Add(time.Hour).Unix(), 10)
	return base + "/reports/" + id + "?expires=" + expiry + "&signature=" + url.QueryEscape(reportSignature(token, id, expiry))
}

func validReportSignature(token, id, expiry, signature string, now time.Time) bool {
	if !reportFilePattern.MatchString(id) {
		return false
	}
	until, err := strconv.ParseInt(expiry, 10, 64)
	if err != nil || until < now.Unix() || until > now.Add(time.Hour+time.Minute).Unix() {
		return false
	}
	provided, err := hex.DecodeString(signature)
	if err != nil {
		return false
	}
	expected, _ := hex.DecodeString(reportSignature(token, id, expiry))
	return hmac.Equal(provided, expected)
}
