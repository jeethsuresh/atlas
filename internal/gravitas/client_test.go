package gravitas

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fixture(t *testing.T) (*Client, *atomic.Int32) {
	t.Helper()
	calls := &atomic.Int32{}
	var issuer string
	mux := http.NewServeMux()
	expiry := time.Now().Add(24 * time.Hour).Unix()
	mux.HandleFunc("GET /.well-known/iam-configuration", func(w http.ResponseWriter, r *http.Request) {
		u, _ := url.Parse(issuer)
		_ = json.NewEncoder(w).Encode(metadata{Issuer: issuer, Domain: u.Hostname(), Browser: issuer + "/oauth/start", Token: issuer + "/oauth/token", Profile: issuer + "/app/v1/me", SessionCheck: issuer + "/oauth/session"})
	})
	mux.HandleFunc("POST /oauth/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		verifier := r.Form.Get("code_verifier")
		if r.Form.Get("code") != "one-use-code" || len(verifier) != 43 || r.Form.Get("redirect_uri") != "https://app.example/auth/gravitas/callback" || r.Form.Get("grant_type") != "authorization_code" || calls.Add(1) != 1 {
			w.WriteHeader(400)
			return
		}
		_ = json.NewEncoder(w).Encode(issuedTokens{AccessToken: "private-opaque-token", TokenType: "Bearer", SessionID: "family-1", CheckToken: strings.Repeat("p", 43), CheckExpiresIn: 86400})
	})
	mux.HandleFunc("GET /app/v1/me", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer private-opaque-token" {
			w.WriteHeader(401)
			return
		}
		u, _ := url.Parse(issuer)
		_ = json.NewEncoder(w).Encode(Identity{Subject: "stable-subject", Username: "alice", Domain: u.Hostname(), UserID: "@alice:" + u.Hostname()})
	})
	mux.HandleFunc("GET /oauth/session", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+strings.Repeat("p", 43) {
			w.WriteHeader(401)
			return
		}
		_ = json.NewEncoder(w).Encode(familyStatus{Active: true, Issuer: issuer, Subject: "stable-subject", Origin: "https://app.example", SessionID: "family-1", Expires: expiry})
	})
	iam := httptest.NewTLSServer(mux)
	t.Cleanup(iam.Close)
	issuer = iam.URL
	c, err := New(issuer, "https://app.example", base64.RawURLEncoding.EncodeToString(make([]byte, 32)))
	if err != nil {
		t.Fatal(err)
	}
	c.HTTP.Transport = iam.Client().Transport
	return c, calls
}
func start(t *testing.T, c *Client) (flow, *http.Cookie, *httptest.ResponseRecorder) {
	t.Helper()
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", c.AppURL+"/auth/gravitas/start?next=%2Fdocuments%2F123", nil)
	if err := c.Start(w, r, false); err != nil {
		t.Fatal(err)
	}
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatal("missing flow cookie")
	}
	cookie := cookies[0]
	if !cookie.Secure || !cookie.HttpOnly || cookie.Domain != "" || cookie.Path != "/" || cookie.SameSite != http.SameSiteLaxMode {
		t.Fatal("unsafe cookie")
	}
	var f flow
	if err := c.open("flow", cookie.Value, &f); err != nil {
		t.Fatal(err)
	}
	location, err := url.Parse(w.Header().Get("Location"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(f.Verifier))
	q := location.Query()
	if q.Get("codeChallenge") != base64.RawURLEncoding.EncodeToString(sum[:]) || q.Get("codeChallengeMethod") != "S256" || q.Get("scope") != "profile.name" || q.Get("state") != f.State || q.Get("redirectURL") != c.AppURL+"/auth/gravitas/callback" {
		t.Fatal("invalid PKCE request")
	}
	if strings.Contains(location.String(), f.Verifier) {
		t.Fatal("verifier exposed in redirect")
	}
	return f, cookie, w
}
func TestPKCECallbackCreatesOnlyAppSession(t *testing.T) {
	c, _ := fixture(t)
	f, cookie, _ := start(t, c)
	r := httptest.NewRequest("GET", c.AppURL+"/auth/gravitas/callback?code=one-use-code&state="+f.State, nil)
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	identity, next, err := c.Callback(w, r)
	if err != nil || identity.Subject != "stable-subject" || identity.Issuer != c.Issuer || next != "/documents/123" {
		t.Fatalf("callback: %v %s", err, next)
	}
	if err = c.SetSession(w, identity); err != nil {
		t.Fatal(err)
	}
	var saved *http.Cookie
	for _, v := range w.Result().Cookies() {
		if v.Name == FlowCookie && v.MaxAge != -1 {
			t.Fatal("flow cookie not consumed")
		}
		if v.Name == SessionCookie {
			saved = v
		}
	}
	if saved == nil || strings.Contains(saved.Value, "private-opaque-token") {
		t.Fatal("unsafe app session")
	}
	req := httptest.NewRequest("GET", c.AppURL+"/", nil)
	req.AddCookie(saved)
	got, err := c.Session(req)
	if err != nil || got.Subject != identity.Subject {
		t.Fatal("app session unavailable")
	}
	_, _, err = c.Callback(httptest.NewRecorder(), r)
	if err == nil {
		t.Fatal("replayed code accepted")
	}
	other, _ := New(c.Issuer, "https://other.example", base64.RawURLEncoding.EncodeToString(make([]byte, 32)))
	if _, err = other.Session(req); err == nil {
		t.Fatal("session accepted by another app")
	}
}
func TestCallbackRejectsStateCookieAndExpiryBeforeExchange(t *testing.T) {
	for _, kind := range []string{"missing", "tampered", "state", "expired", "duplicate-state"} {
		t.Run(kind, func(t *testing.T) {
			c, calls := fixture(t)
			f, cookie, _ := start(t, c)
			state := f.State
			switch kind {
			case "tampered":
				cookie.Value = "tampered"
			case "state":
				state = "wrong"
			case "expired":
				f.Expires = time.Now().Add(-time.Minute).Unix()
				cookie.Value, _ = c.seal("flow", f)
			}
			raw := c.AppURL + "/auth/gravitas/callback?code=one-use-code&state=" + state
			if kind == "duplicate-state" {
				raw += "&state=" + state
			}
			r := httptest.NewRequest("GET", raw, nil)
			if kind != "missing" {
				r.AddCookie(cookie)
			}
			if _, _, err := c.Callback(httptest.NewRecorder(), r); err == nil {
				t.Fatal("invalid callback accepted")
			}
			if calls.Load() != 0 {
				t.Fatal("invalid callback contacted token endpoint")
			}
		})
	}
}
func TestSilentFailuresAndRegistrationKeepValidatedReturn(t *testing.T) {
	c, _ := fixture(t)
	f, cookie, _ := start(t, c)
	r := httptest.NewRequest("GET", c.AppURL+"/auth/gravitas/callback?error=login_required&state="+f.State, nil)
	r.AddCookie(cookie)
	if _, next, err := c.Callback(httptest.NewRecorder(), r); err != ErrLoginRequired || next != "/documents/123" {
		t.Fatal("silent login error lost context")
	}
	w := httptest.NewRecorder()
	if err := c.Start(w, httptest.NewRequest("GET", c.AppURL+"/auth/gravitas/register?next=%2Fdocuments%2F123", nil), true); err != nil {
		t.Fatal(err)
	}
	u, _ := url.Parse(w.Header().Get("Location"))
	if u.Query().Get("screen") != "register" || u.Host != strings.TrimPrefix(c.Issuer, "https://") {
		t.Fatal("registration did not use IAM")
	}
}
func TestPinnedIssuerAndSafeReturns(t *testing.T) {
	for _, raw := range []string{"https://evil.example", "//evil.example", "/%2f%2fevil.example", "/%5cevil.example", "/auth/gravitas/start", "/login"} {
		if SafeNext(raw) != "/" {
			t.Fatalf("unsafe return %s", raw)
		}
	}
	if SafeNext("/documents/123?view=grid") != "/documents/123?view=grid" {
		t.Fatal("legitimate return lost")
	}
	if _, err := New("http://iam.example", "https://app.example", base64.RawURLEncoding.EncodeToString(make([]byte, 32))); err == nil {
		t.Fatal("insecure issuer accepted")
	}
	c, _ := fixture(t)
	c.cached = metadata{Issuer: c.Issuer}
	c.cachedUntil = time.Time{}
	fake := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u, _ := url.Parse(c.Issuer)
		_ = json.NewEncoder(w).Encode(metadata{Issuer: c.Issuer, Domain: u.Hostname(), Browser: "https://evil.example/start", Token: c.Issuer + "/oauth/token", Profile: c.Issuer + "/app/v1/me"})
	}))
	defer fake.Close()
	c.HTTP.Transport = fake.Client().Transport
	// A transport that redirects the pinned host to a second TLS server still cannot change metadata authority.
	c.Issuer = fake.URL
	w := httptest.NewRecorder()
	if err := c.Start(w, httptest.NewRequest("GET", c.AppURL+"/auth/gravitas/start", nil), false); err == nil {
		t.Fatal("foreign discovery endpoint accepted")
	}
}

func TestExpiredSessionCannotAuthenticate(t *testing.T) {
	c, _ := fixture(t)
	value, err := c.seal("session", session{Identity: Identity{Issuer: c.Issuer, Subject: "subject"}, Expires: time.Now().Add(-time.Second).Unix()})
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("GET", c.AppURL+"/", nil)
	r.AddCookie(&http.Cookie{Name: SessionCookie, Value: value})
	if _, err := c.Session(r); err == nil {
		t.Fatal("expired session accepted")
	}
}
