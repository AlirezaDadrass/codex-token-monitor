package monitor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBrowserLoginDashboardAndLogout(t *testing.T) {
	c := testConfig(t)
	now := time.Now().UTC()
	for _, id := range []string{"laptop", "desktop"} {
		s := sampleSnapshot(id, 100, now)
		s.Events[0].AccountFingerprint = hash("retained-log-" + id)
		if _, err := (Store{Dir: filepath.Join(c.DataDir, "hub")}).Put(context.Background(), s); err != nil {
			t.Fatal(err)
		}
	}
	handler := HubHandler(c, testToken)
	request := func(method, path, body, origin string, cookie *http.Cookie) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "http://hub.test"+path, strings.NewReader(body))
		if method == "POST" {
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		}
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if cookie != nil {
			req.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if strings.Contains(w.Body.String(), testToken) {
			t.Fatal("response leaked the hub token")
		}
		return w
	}
	login := request("GET", "/", "", "", nil)
	if login.Code != 200 || !strings.Contains(login.Body.String(), `type="password"`) || strings.Contains(login.Body.String(), "report-data") || login.Header().Get("Cache-Control") != "no-store" || login.Header().Get("Referrer-Policy") != "same-origin" || !strings.Contains(login.Body.String(), `name="referrer" content="same-origin"`) {
		t.Fatal("dashboard leaked counters before login")
	}
	for _, path := range []string{"/health", "/v1/report", "/v1/reports", "/mcp"} {
		if w := request("GET", path, "", "", nil); w.Code != 401 {
			t.Fatalf("public API route: %s", path)
		}
	}
	if w := request("GET", "/login?token="+testToken, "", "", nil); w.Code != 405 {
		t.Fatal("accepted login token in URL")
	}
	if w := request("POST", "/login", "token=wrong", "http://hub.test", nil); w.Code != 401 || len(w.Result().Cookies()) != 0 {
		t.Fatal("wrong token authorized")
	}
	form := url.Values{"token": {testToken}}.Encode()
	if w := request("POST", "/login", form, "https://evil.test", nil); w.Code != 403 {
		t.Fatal("cross-origin login accepted")
	}
	loggedIn := request("POST", "/login", form, "http://hub.test", nil)
	if loggedIn.Code != 303 || loggedIn.Header().Get("Location") != "/" {
		t.Fatalf("login failed: %d", loggedIn.Code)
	}
	cookies := loggedIn.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("missing session cookie")
	}
	cookie := cookies[0]
	if !cookie.HttpOnly || cookie.SameSite != http.SameSiteStrictMode || cookie.MaxAge != int(browserSessionLifetime.Seconds()) || strings.Contains(cookie.Value, testToken) {
		t.Fatal("unsafe session cookie")
	}
	dashboard := request("GET", "/", "", "", cookie)
	for _, marker := range []string{"report-data", "laptop", "desktop", "مصرف هر سشن", "شناسه‌های حساب", `action="/logout"`} {
		if !strings.Contains(dashboard.Body.String(), marker) {
			t.Fatalf("dashboard missing %q", marker)
		}
	}
	if dashboard.Code != 200 {
		t.Fatal("dashboard failed on differing account metadata")
	}
	if w := request("GET", "/v1/report", "", "", cookie); w.Code != 401 {
		t.Fatal("browser cookie authorized the API")
	}
	if w := request("POST", "/logout", "", "https://evil.test", cookie); w.Code != 403 {
		t.Fatal("cross-origin logout accepted")
	}
	logout := request("POST", "/logout", "", "http://hub.test", cookie)
	if logout.Code != 303 || logout.Result().Cookies()[0].MaxAge != -1 {
		t.Fatal("logout did not remove cookie")
	}
	tampered := *cookie
	tampered.Value += "x"
	if w := request("GET", "/", "", "", &tampered); strings.Contains(w.Body.String(), "report-data") {
		t.Fatal("tampered session authorized")
	}
}

func TestBrowserSessionExpiryRotationAndSecureProxy(t *testing.T) {
	now := time.Now().UTC()
	value, err := newBrowserSession(testToken, now)
	if err != nil {
		t.Fatal(err)
	}
	if !validBrowserSession(testToken, value, now) || validBrowserSession("rotated-token", value, now) || validBrowserSession(testToken, value, now.Add(browserSessionLifetime+time.Second)) {
		t.Fatal("browser expiry or token rotation failed")
	}
	c := testConfig(t)
	c.PublicURL = "https://monitor.example.com"
	r := httptest.NewRequest("POST", "http://internal/login", strings.NewReader(url.Values{"token": {testToken}}.Encode()))
	r.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	r.Header.Set("Origin", c.PublicURL)
	w := httptest.NewRecorder()
	HubHandler(c, testToken).ServeHTTP(w, r)
	if w.Code != 303 || !w.Result().Cookies()[0].Secure {
		t.Fatal("HTTPS proxy did not use a secure cookie")
	}
}

func TestReportGeneratedOnlyOnHubWithExplicitServingToken(t *testing.T) {
	hubConfig, clientConfig := testConfig(t), testConfig(t)
	s := sampleSnapshot("named-device", 200, time.Now().UTC())
	if _, err := (Store{Dir: filepath.Join(hubConfig.DataDir, "hub")}).Put(context.Background(), s); err != nil {
		t.Fatal(err)
	}
	hub := httptest.NewServer(HubHandler(hubConfig, testToken))
	defer hub.Close()
	clientConfig.HubURL = hub.URL
	// The hub uses its explicit serving token, not an ambient environment value.
	t.Setenv("TOKEN_MONITOR_HUB_TOKEN", "wrong-environment-token-long-enough-for-tests")
	clientConfig.TokenFile = filepath.Join(t.TempDir(), "connection-token")
	if err := atomicWrite(clientConfig.TokenFile, []byte(testToken)); err != nil {
		t.Fatal(err)
	}
	// Clear the override only for this client call. Server signing must still use
	// the actual token passed into HubHandler, never re-read its config/env.
	t.Setenv("TOKEN_MONITOR_HUB_TOKEN", "")
	a, err := GenerateReport(context.Background(), clientConfig, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if a.Path != "" || a.URL == "" || a.ReportingMachines != 1 || a.LLMCalls != 0 || strings.Contains(a.URL, testToken) {
		t.Fatalf("invalid hub artifact: %+v", a)
	}
	for _, tc := range []struct {
		dir   string
		count int
	}{{clientConfig.DataDir, 0}, {hubConfig.DataDir, 1}} {
		files, _ := filepath.Glob(filepath.Join(tc.dir, "reports", "*.html"))
		if len(files) != tc.count {
			t.Fatalf("wrong report location: %s", tc.dir)
		}
	}
	response, err := http.Get(a.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != 200 || !strings.Contains(string(body), "مصرف هر سشن") || !strings.Contains(string(body), "named-device") {
		t.Fatal("signed hub report failed")
	}
}
