package gravitas

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

type federationTransport func(*http.Request) (*http.Response, error)

func (f federationTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestUsernameSelectsIssuerAndRegistrationDefault(t *testing.T) {
	c, _ := New("https://accounts.example", "https://app.example", base64.RawURLEncoding.EncodeToString(make([]byte, 32)))
	for _, test := range []struct{ input, issuer, identity string }{
		{"alice", c.Issuer, "@alice:accounts.example"},
		{" @Alice ", c.Issuer, "@alice:accounts.example"},
		{"alice@accounts.example", c.Issuer, "@alice:accounts.example"},
		{"@alice:accounts.example", c.Issuer, "@alice:accounts.example"},
		{"alice@remote.example", "https://remote.example", "@alice:remote.example"},
		{"@ALICE:REMOTE.EXAMPLE", "https://remote.example", "@alice:remote.example"},
		{"alice:remote.example", "https://remote.example", "@alice:remote.example"},
	} {
		selected, identity, err := c.ResolveLogin(test.input, false)
		if err != nil || selected.Issuer != test.issuer || identity != test.identity {
			t.Fatalf("%q: %v %q", test.input, err, identity)
		}
	}
	for _, input := range []string{" ", "alice@", "alice@localhost", "alice@127.0.0.1", "alice@10.0.0.1", "alice@remote.example:443", "alice@remote.example/path", "alice@remote.example?x=1", "alice@remote.example#x", "alice@remote.example@evil.example", "alice@-bad.example", "alice@a..example", "alice@a.local", "alice@a.internal"} {
		if _, _, err := c.ResolveLogin(input, false); err == nil {
			t.Fatalf("accepted invalid input %q", input)
		}
	}
	selected, identity, err := c.ResolveLogin("alice@remote.example", true)
	if err != nil || selected != c || identity != "" || c.DefaultDomain() != "accounts.example" {
		t.Fatal("registration/default server drifted")
	}
	var group sync.WaitGroup
	for i := 0; i < 30; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			selected, _, err := c.ResolveLogin("alice@remote.example", false)
			if err != nil || selected.Issuer != "https://remote.example" {
				t.Error("concurrent issuer selection failed")
			}
		}()
	}
	group.Wait()
	if c.Issuer != "https://accounts.example" {
		t.Fatal("remote login mutated the default issuer")
	}
}

func TestRemoteLoginKeepsIssuerThroughCallbackAndRevocation(t *testing.T) {
	for _, wrongAccount := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "wrong-account"}[wrongAccount], func(t *testing.T) {
			c, _ := New("https://accounts.example", "https://app.example", base64.RawURLEncoding.EncodeToString(make([]byte, 32)))
			issuer := "https://remote.example"
			expires := time.Now().Add(time.Hour).Unix()
			proof := base64.RawURLEncoding.EncodeToString(make([]byte, 32))
			revoked, exchanges := false, 0
			c.HTTP.Transport = federationTransport(func(r *http.Request) (*http.Response, error) {
				if r.URL.Host != "remote.example" {
					t.Fatalf("sent remote credentials to %s", r.URL.Host)
				}
				var body any
				status := 200
				switch r.URL.Path {
				case "/.well-known/iam-configuration":
					if r.Header.Get("Authorization") != "" {
						t.Fatal("discovery sent credentials")
					}
					body = metadata{Issuer: issuer, Domain: "remote.example", Browser: issuer + "/oauth/start", Token: issuer + "/oauth/token", Profile: issuer + "/app/v1/me", SessionCheck: issuer + "/oauth/session"}
				case "/oauth/token":
					exchanges++
					_ = r.ParseForm()
					if len(r.Form.Get("code_verifier")) != 43 || r.Form.Get("redirect_uri") != c.AppURL+"/auth/gravitas/callback" {
						t.Fatal("lost PKCE binding")
					}
					body = map[string]any{"access_token": "remote-access", "token_type": "Bearer", "session_check_token": proof, "session_check_expires_in": expires - time.Now().Unix(), "session_id": "11111111-1111-4111-8111-111111111111"}
				case "/app/v1/me":
					if r.Header.Get("Authorization") != "Bearer remote-access" {
						t.Fatal("missing remote access token")
					}
					name := "alice"
					if wrongAccount {
						name = "bob"
					}
					body = Identity{Subject: "same-subject-on-two-servers", Username: name, Domain: "remote.example", UserID: "@" + name + ":remote.example"}
				case "/oauth/session":
					if r.Header.Get("Authorization") != "Bearer "+proof {
						t.Fatal("wrong session proof")
					}
					if revoked {
						status = 401
					}
					body = map[string]any{"active": !revoked, "iss": issuer, "sub": "same-subject-on-two-servers", "app_origin": c.AppURL, "session_id": "11111111-1111-4111-8111-111111111111", "exp": expires}
				default:
					t.Fatalf("unexpected request %s", r.URL)
				}
				raw, _ := json.Marshal(body)
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
			})
			w := httptest.NewRecorder()
			if err := c.Start(w, httptest.NewRequest("GET", c.AppURL+"/auth/gravitas/start?username=alice%40remote.example&next=%2Fdocs%3Fview%3Dgrid", nil), false); err != nil {
				t.Fatal(err)
			}
			target, _ := url.Parse(w.Header().Get("Location"))
			if target.Host != "remote.example" || target.Query().Get("login_hint") != "@alice:remote.example" {
				t.Fatal("username did not select remote login")
			}
			r := httptest.NewRequest("GET", c.AppURL+"/auth/gravitas/callback?code=code&state="+target.Query().Get("state")+"&issuer=https://evil.example", nil)
			r.AddCookie(w.Result().Cookies()[0])
			identity, next, err := c.Callback(httptest.NewRecorder(), r)
			if wrongAccount {
				if err == nil {
					t.Fatal("accepted a different account")
				}
				return
			}
			if err != nil || identity.Issuer != issuer || next != "/docs?view=grid" || exchanges != 1 {
				t.Fatalf("remote callback failed: %v", err)
			}
			saved := httptest.NewRecorder()
			if err := c.SetSession(saved, identity); err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest("GET", c.AppURL+"/", nil)
			request.AddCookie(saved.Result().Cookies()[0])
			if got, err := c.Session(request); err != nil || got.Issuer != issuer {
				t.Fatalf("remote session failed: %v", err)
			}
			revoked = true
			if _, err := c.Session(request); err != ErrLoginRequired {
				t.Fatalf("remote revocation ignored: %v", err)
			}
		})
	}
}

func TestFederationBlocksPrivateAddressesAtDialTime(t *testing.T) {
	client := federatedHTTP(&http.Client{})
	transport := client.Transport.(*http.Transport)
	for _, address := range []string{"127.0.0.1:443", "10.0.0.1:443", "169.254.169.254:443", "100.100.100.200:443", "[::1]:443", "[fd00::1]:443", "[::ffff:127.0.0.1]:443"} {
		if conn, err := transport.DialContext(context.Background(), "tcp", address); err == nil {
			conn.Close()
			t.Fatalf("dialed %s", address)
		}
	}
	if !publicIdentityIP(net.ParseIP("1.1.1.1")) {
		t.Fatal("public address rejected")
	}
}
