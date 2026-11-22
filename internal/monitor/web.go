package monitor

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const browserCookieName = "token_monitor_session"
const browserSessionLifetime = 8 * time.Hour
const reportCSP = "default-src 'none'; script-src 'unsafe-inline'; style-src 'unsafe-inline'; font-src data:; img-src data:; connect-src 'none'; base-uri 'none'; frame-ancestors 'none'; form-action 'self'"

// Browser sessions never contain the hub token. API and MCP requests still
// require their Bearer token; browser cookies authorize only the dashboard.
func newHubWeb(c Config, token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		w.Header().Set("Content-Security-Policy", reportCSP)
		secure := strings.HasPrefix(hubOrigin(c, r), "https://")
		setCookie := func(value string, maxAge int) {
			http.SetCookie(w, &http.Cookie{Name: browserCookieName, Value: value, Path: "/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode, MaxAge: maxAge})
		}
		if r.URL.Path == "/login" || r.URL.Path == "/logout" {
			if r.Method != http.MethodPost {
				w.Header().Set("Allow", http.MethodPost)
				http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
				return
			}
			if !sameBrowserOrigin(c, r) {
				http.Error(w, "Cross-site request rejected", http.StatusForbidden)
				return
			}
			if r.URL.Path == "/logout" {
				setCookie("", -1)
				http.Redirect(w, r, "/", http.StatusSeeOther)
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, 4096)
			if r.ParseForm() != nil {
				http.Error(w, "Invalid login request", http.StatusBadRequest)
				return
			}
			provided := sha256.Sum256([]byte(r.PostForm.Get("token")))
			wanted := sha256.Sum256([]byte(token))
			if subtle.ConstantTimeCompare(provided[:], wanted[:]) != 1 {
				w.WriteHeader(http.StatusUnauthorized)
				renderLogin(w, "توکن واردشده درست نیست.")
				return
			}
			session, err := newBrowserSession(token, time.Now().UTC())
			if err != nil {
				http.Error(w, "Cannot create session", http.StatusInternalServerError)
				return
			}
			setCookie(session, int(browserSessionLifetime.Seconds()))
			http.Redirect(w, r, "/", http.StatusSeeOther)
			return
		}
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			w.Header().Set("Allow", "GET, HEAD")
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}
		cookie, err := r.Cookie(browserCookieName)
		if err != nil || !validBrowserSession(token, cookie.Value, time.Now().UTC()) {
			renderLogin(w, "")
			return
		}
		report, err := GetReport(r.Context(), c, true, r.URL.Query().Get("metric"), false)
		if err != nil {
			http.Error(w, "خواندن گزارش هاب موفق نبود؛ دوباره تلاش کنید.", http.StatusServiceUnavailable)
			return
		}
		var body bytes.Buffer
		if err := renderHTML(&body, report, true); err != nil {
			http.Error(w, "Cannot render report", http.StatusInternalServerError)
			return
		}
		if r.Method != http.MethodHead {
			_, _ = w.Write(body.Bytes())
		}
	})
}

func sameBrowserOrigin(c Config, r *http.Request) bool {
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		return false
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
		return false
	}
	want, err := url.Parse(hubOrigin(c, r))
	return err == nil && strings.EqualFold(u.Scheme, want.Scheme) && strings.EqualFold(u.Host, want.Host)
}

func browserSessionSignature(token, payload string) []byte {
	h := hmac.New(sha256.New, []byte(token))
	_, _ = h.Write([]byte("browser-session/" + payload))
	return h.Sum(nil)
}

func newBrowserSession(token string, now time.Time) (string, error) {
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return "", err
	}
	payload := hex.EncodeToString(nonce[:]) + "." + strconv.FormatInt(now.Add(browserSessionLifetime).Unix(), 10)
	return payload + "." + hex.EncodeToString(browserSessionSignature(token, payload)), nil
}

func validBrowserSession(token, value string, now time.Time) bool {
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return false
	}
	nonce, err := hex.DecodeString(parts[0])
	if err != nil || len(nonce) != 16 {
		return false
	}
	expires, err := strconv.ParseInt(parts[1], 10, 64)
	if err != nil || expires <= now.Unix() || expires > now.Add(browserSessionLifetime+time.Minute).Unix() {
		return false
	}
	sig, err := hex.DecodeString(parts[2])
	return err == nil && hmac.Equal(sig, browserSessionSignature(token, parts[0]+"."+parts[1]))
}

var loginTemplate = template.Must(template.New("login").Parse(`<!doctype html>
<html lang="fa" dir="rtl"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="referrer" content="same-origin"><title>ورود به گزارش مصرف کدکس</title>
<style>@font-face{font-family:ReportVazirmatn;src:url("{{.Font}}") format("truetype");font-weight:100 900}*{box-sizing:border-box}body{margin:0;background:#f3f4ef;color:#182d2a;font:14px/1.9 ReportVazirmatn,Tahoma,Arial,sans-serif;min-height:100vh;display:grid;place-items:center;padding:24px}main{background:white;border:1px solid #e5e9e2;border-radius:17px;padding:32px;width:100%;max-width:420px}h1{font-size:24px;margin:0 0 8px}p{color:#687670;margin:0 0 24px}label{display:block;margin-bottom:8px}input,button{font:inherit;width:100%;border-radius:9px;padding:11px 13px}input{border:1px solid #c8d8cf;background:#fff;direction:ltr;text-align:left}input:focus{outline:2px solid #156d62;outline-offset:2px}button{margin-top:18px;background:#156d62;color:white;border:0;cursor:pointer}.error{color:#9c3a27;margin:12px 0 0}small{display:block;color:#687670;margin-top:20px}</style></head>
<body><main><h1>گزارش مصرف کدکس</h1><p>مصرف همهٔ دستگاه‌ها و سشن‌های متصل به این هاب</p><form method="post" action="/login"><label for="token">توکن هاب</label><input id="token" name="token" type="password" autocomplete="current-password" required autofocus spellcheck="false">{{if .Error}}<p class="error" role="alert">{{.Error}}</p>{{end}}<button type="submit">ورود به گزارش</button></form><small>توکن همان کلیدی است که هنگام اتصال دستگاه‌ها وارد کرده‌اید.</small></main></body></html>`))

func renderLogin(w http.ResponseWriter, message string) {
	_ = loginTemplate.Execute(w, struct {
		Font  template.URL
		Error string
	}{
		Font: template.URL("data:font/ttf;base64," + base64.StdEncoding.EncodeToString(reportFont)), Error: message,
	})
}
