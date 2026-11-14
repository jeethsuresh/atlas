package storecatalog

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"os"
	"time"
)

const endpoint = "https://apps.gravitas.cool/v1/catalog/"

type Publisher struct {
	key, name string
	ids       []string
	client    *http.Client
	ready     chan struct{}
}

func New(name string, ids []string, gateway string) *Publisher {
	key := os.Getenv("STORE_CATALOG_KEY")
	if len(key) < 32 {
		return nil
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	if gateway != "" {
		dialer := net.Dialer{Timeout: 3 * time.Second}
		transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, gateway)
		}
	}
	return &Publisher{key: key, name: name, ids: ids, ready: make(chan struct{}), client: &http.Client{
		Transport: transport, Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

func (p *Publisher) put(ctx context.Context, path string, data []byte) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint+p.name+path, bytes.NewReader(data))
	if err != nil {
		return false
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.key)
	response, err := p.client.Do(req)
	if err != nil {
		return false
	}
	response.Body.Close()
	return response.StatusCode == http.StatusNoContent
}

func (p *Publisher) Run(ctx context.Context, manifest []byte) {
	if p == nil {
		return
	}
	backoff := 5 * time.Second
	announced := false
	for ctx.Err() == nil {
		delay := backoff
		if p.put(ctx, "", manifest) {
			delay, backoff = time.Hour, 5*time.Second
			if !announced {
				close(p.ready)
				announced = true
			}
		} else {
			if ctx.Err() == nil {
				log.Printf("Private Store catalog publication failed for %s", p.name)
			}
			backoff = min(2*backoff, 15*time.Minute)
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// Register follows successful sign-in or account provisioning. The Store derives
// the account from a status-only credential that is bound to this service.
func (p *Publisher) Register(ctx context.Context, issuer, proof string) {
	if p == nil || issuer == "" || proof == "" {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 4*time.Second)
	defer cancel()
	select {
	case <-p.ready:
	case <-ctx.Done():
		return
	}
	body, err := json.Marshal(struct {
		Issuer string   `json:"issuer"`
		Proof  string   `json:"sessionProof"`
		Apps   []string `json:"apps"`
	}{issuer, proof, p.ids})
	if err != nil {
		return
	}
	if !p.put(ctx, "/accounts", body) {
		log.Printf("Private Store catalog account registration failed for %s", p.name)
	}
}
