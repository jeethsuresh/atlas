package gravitas

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var ErrInvalidUsername = errors.New("use a username or user@domain")

var loginName = regexp.MustCompile(`^[a-z0-9]{1,20}$`)
var domainLabel = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

// DefaultDomain is also the server used by the registration endpoint.
func (c *Client) DefaultDomain() string {
	u, _ := url.Parse(c.Issuer)
	return u.Hostname()
}

// ResolveLogin accepts a local name, user@domain, or @user:domain.
// Registration always uses the configured issuer, regardless of the input.
func (c *Client) ResolveLogin(raw string, register bool) (*Client, string, error) {
	if register || raw == "" {
		return c, "", nil
	}
	raw = strings.ToLower(strings.TrimSpace(raw))
	if len(raw) > 256 {
		return nil, "", ErrInvalidUsername
	}
	raw = strings.TrimPrefix(raw, "@")
	name, domain := raw, c.DefaultDomain()
	if strings.Contains(raw, ":") {
		name, domain, _ = strings.Cut(raw, ":")
	} else if strings.Contains(raw, "@") {
		name, domain, _ = strings.Cut(raw, "@")
	}
	if !loginName.MatchString(name) || domain == "" {
		return nil, "", ErrInvalidUsername
	}
	issuer := c.Issuer
	if domain != c.DefaultDomain() {
		issuer = "https://" + domain
	}
	selected, err := c.ForIssuer(issuer)
	if err != nil {
		return nil, "", ErrInvalidUsername
	}
	return selected, "@" + name + ":" + domain, nil
}

// ForIssuer never mutates the default issuer or shares its discovery cache.
// Callers use an issuer bound into an authenticated flow or session envelope.
func (c *Client) ForIssuer(issuer string) (*Client, error) {
	if issuer == "" || issuer == c.Issuer {
		return c, nil
	}
	u, err := url.Parse(issuer)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.Host != u.Hostname() || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || strings.ToLower(u.Host) != u.Host || len(u.Host) > 253 || net.ParseIP(u.Host) != nil || !strings.Contains(u.Host, ".") {
		return nil, errors.New("invalid identity domain")
	}
	for _, label := range strings.Split(u.Host, ".") {
		if !domainLabel.MatchString(label) {
			return nil, errors.New("invalid identity domain")
		}
	}
	for _, suffix := range []string{".localhost", ".local", ".internal", ".home.arpa"} {
		if strings.HasSuffix(u.Host, suffix) {
			return nil, errors.New("identity domain must be public")
		}
	}
	c.federationMu.Lock()
	defer c.federationMu.Unlock()
	if selected := c.federated[issuer]; selected != nil {
		return selected, nil
	}
	if c.federated == nil {
		c.federated = make(map[string]*Client)
	}
	if len(c.federatedOrder) >= 64 {
		oldest := c.federatedOrder[0]
		c.federated[oldest].HTTP.CloseIdleConnections()
		delete(c.federated, oldest)
		c.federatedOrder = c.federatedOrder[1:]
	}
	selected := &Client{Issuer: issuer, AppURL: c.AppURL, aead: c.aead, HTTP: federatedHTTP(c.HTTP)}
	c.federated[issuer] = selected
	c.federatedOrder = append(c.federatedOrder, issuer)
	return selected, nil
}

func (c *Client) acceptsIssuer(issuer string) bool {
	if issuer == "" {
		return false
	}
	_, err := c.ForIssuer(issuer)
	return err == nil
}

// Resolve at dial time and connect to that exact public address, preventing
// private-network requests and DNS rebinding. TLS still verifies the hostname.
func federatedHTTP(base *http.Client) *http.Client {
	client := *base
	client.Jar = nil
	client.Timeout = 12 * time.Second
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	transport := base.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	if original, ok := transport.(*http.Transport); ok {
		t := original.Clone()
		t.Proxy = nil
		t.DialTLSContext = nil
		t.DialTLS = nil
		t.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil || port != "443" {
				return nil, errors.New("invalid identity address")
			}
			addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, err
			}
			for _, address := range addresses {
				ip := address.IP
				if !publicIdentityIP(ip) {
					return nil, errors.New("identity domain must resolve to public addresses")
				}
			}
			dialer := net.Dialer{Timeout: 5 * time.Second}
			for _, address := range addresses {
				conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(address.IP.String(), port))
				if dialErr == nil {
					return conn, nil
				}
				err = dialErr
			}
			if err == nil {
				err = errors.New("identity domain has no addresses")
			}
			return nil, err
		}
		client.Transport = t
	}
	return &client
}

func publicIdentityIP(ip net.IP) bool {
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
		return false
	}
	for _, cidr := range []string{"100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32", "64:ff9b::/96", "64:ff9b:1::/48", "2002::/16"} {
		_, block, _ := net.ParseCIDR(cidr)
		if block.Contains(ip) {
			return false
		}
	}
	return true
}
