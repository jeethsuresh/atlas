package gravitas

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

var ErrUnavailable = errors.New("Gravitas is temporarily unavailable")

const FamilyLifetime = 30 * 24 * time.Hour

type issuedTokens struct {
	AccessToken    string `json:"access_token"`
	RefreshToken   string `json:"refresh_token"`
	TokenType      string `json:"token_type"`
	Scope          string `json:"scope"`
	ExpiresIn      int64  `json:"expires_in"`
	SessionID      string `json:"session_id"`
	CheckToken     string `json:"session_check_token"`
	CheckExpiresIn int64  `json:"session_check_expires_in"`
}

// Only an explicit native PKCE broker exchange returns this bundle. Never persist
// it server-side or include it in browser responses. Android encrypts it at rest.
type BrokerBundle struct {
	Issuer            string `json:"issuer"`
	AppOrigin         string `json:"appOrigin"`
	Subject           string `json:"subject"`
	Account           string `json:"account"`
	AccessToken       string `json:"accessToken"`
	RefreshToken      string `json:"refreshToken"`
	Scope             string `json:"scope"`
	AccessExpiresAt   int64  `json:"accessExpiresAt"`
	SessionID         string `json:"sessionId"`
	SessionCheckToken string `json:"sessionCheckToken"`
	SessionExpiresAt  int64  `json:"sessionExpiresAt"`
}

func (BrokerBundle) String() string { return "BrokerBundle(redacted)" }

type familyStatus struct {
	Active    bool   `json:"active"`
	Issuer    string `json:"iss"`
	Subject   string `json:"sub"`
	Origin    string `json:"app_origin"`
	SessionID string `json:"session_id"`
	Expires   int64  `json:"exp"`
}

func (c *Client) proofStatus(ctx context.Context, identity Identity) (int64, error) {
	selected, selectErr := c.ForIssuer(identity.Issuer)
	if selectErr != nil {
		return 0, ErrLoginRequired
	}
	c = selected
	if !c.acceptsIssuer(identity.Issuer) || identity.Subject == "" || identity.SessionID == "" || len(identity.SessionID) > 128 || !proofPattern.MatchString(identity.CheckToken) {
		return 0, ErrLoginRequired
	}
	m, err := c.discovery()
	if err != nil {
		return 0, ErrUnavailable
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.SessionCheck, nil)
	if err != nil {
		return 0, ErrUnavailable
	}
	req.Header.Set("Authorization", "Bearer "+identity.CheckToken)
	req.Header.Set("Accept", "application/json")
	response, err := c.HTTP.Do(req)
	if err != nil {
		return 0, ErrUnavailable
	}
	defer response.Body.Close()
	if response.StatusCode == 401 {
		return 0, ErrLoginRequired
	}
	if response.StatusCode != 200 {
		return 0, ErrUnavailable
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	if err != nil || len(raw) > 65536 {
		return 0, ErrUnavailable
	}
	var status familyStatus
	if json.Unmarshal(raw, &status) != nil || !status.Active || status.Issuer != c.Issuer || status.Subject != identity.Subject || status.Origin != c.AppURL || status.SessionID != identity.SessionID || status.Expires <= time.Now().Unix() || status.Expires > time.Now().Add(FamilyLifetime+time.Minute).Unix() || (identity.SessionExpires != 0 && status.Expires != identity.SessionExpires) {
		return 0, ErrUnavailable
	}
	return status.Expires, nil
}

// No positive status decisions are cached: every protected request reaches IAM.
func (c *Client) CheckIdentity(ctx context.Context, identity Identity) error {
	if identity.SessionExpires <= time.Now().Unix() {
		return ErrLoginRequired
	}
	_, err := c.proofStatus(ctx, identity)
	return err
}
func (c *Client) SealDevice(identity Identity) (string, error) {
	if !c.acceptsIssuer(identity.Issuer) || identity.SessionExpires <= time.Now().Unix() || identity.SessionID == "" || !proofPattern.MatchString(identity.CheckToken) {
		return "", ErrLoginRequired
	}
	return c.seal("device-session", identity)
}
func (c *Client) OpenDevice(value string) (Identity, error) {
	var identity Identity
	if c.open("device-session", value, &identity) != nil || !c.acceptsIssuer(identity.Issuer) || identity.SessionExpires <= time.Now().Unix() {
		return Identity{}, ErrLoginRequired
	}
	return identity, nil
}
func (c *Client) NativeBrokerExchange(code, verifier string) (Identity, *BrokerBundle, error) {
	if code == "" || len(code) > 2048 || !proofPattern.MatchString(verifier) {
		return Identity{}, nil, ErrLoginRequired
	}
	identity, tokens, err := c.exchangeGrant(code, verifier, c.NativeRedirect())
	if err != nil {
		return Identity{}, nil, err
	}
	fields := strings.Fields(tokens.Scope)
	scopes := map[string]bool{}
	for _, scope := range fields {
		scopes[scope] = true
	}
	if len(fields) != 2 || len(scopes) != 2 || !scopes["profile.name"] || !scopes["native.broker"] || !proofPattern.MatchString(tokens.AccessToken) || !proofPattern.MatchString(tokens.RefreshToken) || tokens.ExpiresIn < 1 || tokens.ExpiresIn > 3600 {
		return Identity{}, nil, ErrLoginRequired
	}
	expiry := min(time.Now().Unix()+tokens.ExpiresIn, identity.SessionExpires)
	return identity, &BrokerBundle{Issuer: c.Issuer, AppOrigin: c.AppURL, Subject: identity.Subject, Account: identity.UserID, AccessToken: tokens.AccessToken, RefreshToken: tokens.RefreshToken, Scope: tokens.Scope, AccessExpiresAt: expiry, SessionID: identity.SessionID, SessionCheckToken: identity.CheckToken, SessionExpiresAt: identity.SessionExpires}, nil
}
