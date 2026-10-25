// Package gravitas implements the IAM server's authorization-code + PKCE protocol.
package gravitas

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
)

const FlowCookie = "__Host-gravitas_flow"
const SessionCookie = "__Host-gravitas_session"
const SessionLifetime = 8 * time.Hour

var ErrLoginRequired = errors.New("login required")
var ErrConsentRequired = errors.New("consent required")

type Identity struct {
	Issuer         string `json:"issuer"`
	Subject        string `json:"sub"`
	Username       string `json:"username"`
	Domain         string `json:"domain"`
	UserID         string `json:"user_id"`
	SessionID      string `json:"session_id,omitempty"`
	CheckToken     string `json:"session_check_token,omitempty"`
	SessionExpires int64  `json:"session_expires_at,omitempty"`
}
type metadata struct {
	Issuer       string `json:"issuer"`
	Domain       string `json:"identity_domain"`
	Browser      string `json:"browser_initiation_endpoint"`
	Token        string `json:"token_endpoint"`
	Profile      string `json:"userinfo_endpoint"`
	SessionCheck string `json:"session_check_endpoint"`
}
type flow struct {
	Issuer   string
	Username string
	State    string
	Verifier string
	Next     string
	Expires  int64
}
type session struct {
	Identity Identity
	Expires  int64
}

type Client struct {
	federationMu   sync.Mutex
	federated      map[string]*Client
	federatedOrder []string
	Issuer         string
	AppURL         string
	HTTP           *http.Client
	aead           cipher.AEAD
	mu             sync.Mutex
	cached         metadata
	cachedUntil    time.Time
}

func New(issuer, appURL, encodedKey string) (*Client, error) {
	issuer = strings.TrimSuffix(issuer, "/")
	appURL = strings.TrimSuffix(appURL, "/")
	for _, raw := range []string{issuer, appURL} {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" {
			return nil, errors.New("IAM and application URLs must be HTTPS origins")
		}
	}
	key, err := base64.RawURLEncoding.DecodeString(encodedKey)
	if err != nil || len(key) != 32 {
		return nil, errors.New("GRAVITAS_SESSION_KEY must be a base64url-encoded 32-byte key")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Client{Issuer: issuer, AppURL: appURL, aead: aead, HTTP: &http.Client{Timeout: 12 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (c *Client) discovery() (metadata, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Now().Before(c.cachedUntil) {
		return c.cached, nil
	}
	var m metadata
	if err := c.getJSON(c.Issuer+"/.well-known/iam-configuration", "", &m); err != nil {
		return m, err
	}
	origin, _ := url.Parse(c.Issuer)
	if m.Issuer != c.Issuer || m.Domain != origin.Hostname() {
		return m, errors.New("IAM discovery issuer/domain mismatch")
	}
	for _, raw := range []string{m.Browser, m.Token, m.Profile, m.SessionCheck} {
		u, err := url.Parse(raw)
		if err != nil || u.Scheme != origin.Scheme || u.Host != origin.Host || u.User != nil || u.Fragment != "" || u.RawQuery != "" {
			return m, errors.New("IAM discovery endpoint must belong to the configured issuer")
		}
	}
	c.cached = m
	c.cachedUntil = time.Now().Add(5 * time.Minute)
	return m, nil
}
func readJSON(resp *http.Response, out any) error {
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode == 400 || resp.StatusCode == 401 {
			return ErrLoginRequired
		}
		return ErrUnavailable
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return ErrUnavailable
	}
	if json.Unmarshal(raw, out) != nil {
		return ErrUnavailable
	}
	return nil
}
func (c *Client) getJSON(endpoint, token string, out any) error {
	req, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return ErrUnavailable
	}
	return readJSON(resp, out)
}
func random() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}
func SafeNext(raw string) string {
	if len(raw) > 2048 {
		return "/"
	}
	u, err := url.Parse(raw)
	if err != nil || u.IsAbs() || u.Host != "" || !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") || strings.ContainsAny(raw, "\\\r\n") || strings.HasPrefix(u.Path, "//") || strings.ContainsAny(u.Path, "\\\r\n") {
		return "/"
	}
	if strings.HasPrefix(u.Path, "/auth/") || u.Path == "/login" {
		return "/"
	}
	return u.RequestURI()
}
func (c *Client) seal(purpose string, value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, c.aead.NonceSize())
	if _, err = rand.Read(nonce); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(c.aead.Seal(nonce, nonce, raw, []byte(c.AppURL+"|"+purpose))), nil
}
func (c *Client) open(purpose, encoded string, value any) error {
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(raw) < c.aead.NonceSize() {
		return errors.New("invalid authentication cookie")
	}
	raw, err = c.aead.Open(nil, raw[:c.aead.NonceSize()], raw[c.aead.NonceSize():], []byte(c.AppURL+"|"+purpose))
	if err != nil {
		return errors.New("invalid authentication cookie")
	}
	return json.Unmarshal(raw, value)
}
func cookie(w http.ResponseWriter, name, value string, age int) {
	http.SetCookie(w, &http.Cookie{Name: name, Value: value, Path: "/", MaxAge: age, Secure: true, HttpOnly: true, SameSite: http.SameSiteLaxMode})
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
}
func (c *Client) Start(w http.ResponseWriter, r *http.Request, register bool) error {
	raw := r.URL.Query().Get("username")
	if raw == "" {
		raw = r.URL.Query().Get("login_hint")
	}
	selected, username, err := c.ResolveLogin(raw, register)
	if err != nil {
		return err
	}
	c = selected
	m, err := c.discovery()
	if err != nil {
		return err
	}
	f := flow{Issuer: c.Issuer, Username: username, State: random(), Verifier: random(), Next: SafeNext(r.URL.Query().Get("next")), Expires: time.Now().Add(10 * time.Minute).Unix()}
	encoded, err := c.seal("flow", f)
	if err != nil {
		return err
	}
	h := sha256.Sum256([]byte(f.Verifier))
	q := url.Values{"callerURL": {c.AppURL}, "redirectURL": {c.AppURL + "/auth/gravitas/callback"}, "codeChallenge": {base64.RawURLEncoding.EncodeToString(h[:])}, "codeChallengeMethod": {"S256"}, "scope": {"profile.name"}, "state": {f.State}}
	if register {
		q.Set("screen", "register")
	}
	if r.URL.Query().Get("prompt") == "none" {
		q.Set("prompt", "none")
	}
	if username != "" {
		q.Set("login_hint", username)
	}
	cookie(w, FlowCookie, encoded, 600)
	http.Redirect(w, r, m.Browser+"?"+q.Encode(), http.StatusSeeOther)
	return nil
}
func (c *Client) Callback(w http.ResponseWriter, r *http.Request) (Identity, string, error) {
	var identity Identity
	got, err := r.Cookie(FlowCookie)
	cookie(w, FlowCookie, "", -1)
	if err != nil {
		return identity, "", errors.New("login expired; start again")
	}
	var f flow
	if err = c.open("flow", got.Value, &f); err != nil {
		return identity, "", err
	}
	q := r.URL.Query()
	if f.Expires <= time.Now().Unix() || f.Expires > time.Now().Add(11*time.Minute).Unix() || len(q["state"]) != 1 || f.State == "" || subtle.ConstantTimeCompare([]byte(f.State), []byte(q.Get("state"))) != 1 {
		return identity, "", errors.New("invalid or expired login state")
	}
	if failure := q.Get("error"); failure != "" {
		if failure == "login_required" {
			return identity, f.Next, ErrLoginRequired
		}
		if failure == "consent_required" {
			return identity, f.Next, ErrConsentRequired
		}
		return identity, f.Next, errors.New("IAM sign-in was not completed")
	}
	if len(q["code"]) != 1 || q.Get("code") == "" {
		return identity, "", errors.New("missing authorization code")
	}
	selected, err := c.ForIssuer(f.Issuer)
	if err != nil {
		return identity, "", err
	}
	c = selected
	identity, err = c.exchange(q.Get("code"), f.Verifier, c.AppURL+"/auth/gravitas/callback")
	if err == nil && f.Username != "" && identity.UserID != f.Username {
		return Identity{}, "", ErrLoginRequired
	}
	return identity, SafeNext(f.Next), err
}

var proofPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{43}$`)

func (c *Client) NativeRedirect() string { return c.AppURL + "/auth/android/callback" }
func (c *Client) NativeStart(state, challenge string) (string, error) {
	return c.NativeStartBroker(state, challenge, false)
}
func (c *Client) NativeStartBroker(state, challenge string, broker bool) (string, error) {
	if !proofPattern.MatchString(state) || !proofPattern.MatchString(challenge) {
		return "", errors.New("invalid native login proof")
	}
	m, err := c.discovery()
	if err != nil {
		return "", err
	}
	q := url.Values{"callerURL": {c.AppURL}, "redirectURL": {c.NativeRedirect()}, "codeChallenge": {challenge}, "codeChallengeMethod": {"S256"}, "scope": {"profile.name"}, "state": {state}}
	if broker {
		q.Set("scope", "profile.name native.broker")
	}
	return m.Browser + "?" + q.Encode(), nil
}
func (c *Client) NativeExchange(code, verifier string) (Identity, error) {
	if code == "" || len(code) > 2048 || !proofPattern.MatchString(verifier) {
		return Identity{}, errors.New("invalid native authorization code or verifier")
	}
	return c.exchange(code, verifier, c.NativeRedirect())
}
func (c *Client) exchange(code, verifier, redirect string) (Identity, error) {
	identity, _, err := c.exchangeGrant(code, verifier, redirect)
	return identity, err
}
func (c *Client) exchangeGrant(code, verifier, redirect string) (Identity, issuedTokens, error) {
	var tokens issuedTokens
	var identity Identity
	m, err := c.discovery()
	if err != nil {
		return identity, tokens, err
	}
	form := url.Values{"grant_type": {"authorization_code"}, "code": {code}, "code_verifier": {verifier}, "redirect_uri": {redirect}}
	req, err := http.NewRequest(http.MethodPost, m.Token, bytes.NewBufferString(form.Encode()))
	if err != nil {
		return identity, tokens, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return identity, tokens, ErrUnavailable
	}

	if err = readJSON(resp, &tokens); err != nil {
		return identity, tokens, err
	}
	if tokens.AccessToken == "" || !strings.EqualFold(tokens.TokenType, "Bearer") {
		return identity, tokens, ErrUnavailable
	}
	if err = c.getJSON(m.Profile, tokens.AccessToken, &identity); err != nil {
		return identity, tokens, err
	}
	if identity.Subject == "" || len(identity.Subject) > 256 || identity.Username == "" || identity.Domain != m.Domain || identity.UserID != "@"+identity.Username+":"+m.Domain {
		return Identity{}, tokens, ErrLoginRequired
	}
	identity.Issuer = c.Issuer
	identity.SessionID = tokens.SessionID
	identity.CheckToken = tokens.CheckToken
	identity.SessionExpires = 0
	if tokens.CheckExpiresIn < 1 || tokens.CheckExpiresIn > int64(FamilyLifetime.Seconds()) {
		return Identity{}, tokens, ErrLoginRequired
	}
	identity.SessionExpires, err = c.proofStatus(context.Background(), identity)
	if err != nil {
		return Identity{}, tokens, err
	}
	return identity, tokens, nil
}
func (c *Client) SetSession(w http.ResponseWriter, identity Identity) error {
	if identity.SessionExpires <= time.Now().Unix() || identity.CheckToken == "" {
		return ErrLoginRequired
	}
	expires := min(time.Now().Add(SessionLifetime).Unix(), identity.SessionExpires)
	value, err := c.seal("session", session{Identity: identity, Expires: expires})
	if err != nil {
		return err
	}
	cookie(w, SessionCookie, value, int(expires-time.Now().Unix()))
	return nil
}
func (c *Client) Session(r *http.Request) (Identity, error) {
	identity, _, err := c.SessionInfo(r)
	return identity, err
}
func (c *Client) SessionInfo(r *http.Request) (Identity, time.Time, error) {
	got, err := r.Cookie(SessionCookie)
	if err != nil {
		return Identity{}, time.Time{}, ErrLoginRequired
	}
	var s session
	if err = c.open("session", got.Value, &s); err != nil {
		return Identity{}, time.Time{}, ErrLoginRequired
	}
	if s.Expires <= time.Now().Unix() || s.Expires > time.Now().Add(SessionLifetime+time.Minute).Unix() || !c.acceptsIssuer(s.Identity.Issuer) || s.Identity.Subject == "" {
		return Identity{}, time.Time{}, ErrLoginRequired
	}
	if err := c.CheckIdentity(r.Context(), s.Identity); err != nil {
		return Identity{}, time.Time{}, err
	}
	return s.Identity, time.Unix(s.Expires, 0), nil
}
func (c *Client) ClearSession(w http.ResponseWriter) { cookie(w, SessionCookie, "", -1) }
func (c *Client) ValidMutation(r *http.Request) bool {
	return r.Header.Get("X-Gravitas-CSRF") == "1" && (r.Header.Get("Origin") == "" || r.Header.Get("Origin") == c.AppURL)
}
