package client

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/themid6t/floo/internal/broker"
)

type Session = broker.Session
type Message = broker.Message

type Client struct {
	base  string
	token string
	http  *http.Client
}

func New(baseURL string) *Client {
	if baseURL == "" {
		baseURL = os.Getenv("FLOO_URL")
	}
	if baseURL == "" {
		baseURL = "http://127.0.0.1:7777"
	}
	return &Client{base: baseURL, token: Token(), http: &http.Client{Timeout: 40 * time.Second}}
}

// Token returns FLOO_TOKEN, else the value in ~/.config/floo/token
// ("FLOO_TOKEN=<hex>", the same file systemd loads as the broker's
// EnvironmentFile), so hook-spawned adapters need no env plumbing.
func Token() string {
	if t := os.Getenv("FLOO_TOKEN"); t != "" {
		return t
	}
	home, _ := os.UserHomeDir()
	b, err := os.ReadFile(filepath.Join(home, ".config", "floo", "token"))
	if err != nil {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(b)), "FLOO_TOKEN="))
}

func (c *Client) request(ctx context.Context, method, path string, body any, out any) error {
	var raw []byte
	if body != nil {
		var err error
		raw, err = json.Marshal(body)
		if err != nil {
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("X-Floo-Client", "1")
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		var e struct {
			Error string `json:"error"`
		}
		json.NewDecoder(resp.Body).Decode(&e)
		if e.Error == "" {
			e.Error = resp.Status
		}
		return fmt.Errorf("floo: %s", e.Error)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func (c *Client) Register(ctx context.Context, s Session) error {
	return c.request(ctx, http.MethodPost, "/v1/sessions", s, nil)
}

func (c *Client) Send(ctx context.Context, from, to, text string) (int64, error) {
	var result struct {
		Seq int64 `json:"seq"`
	}
	err := c.request(ctx, http.MethodPost, "/v1/messages", map[string]string{"from": from, "to": to, "text": text}, &result)
	return result.Seq, err
}

func (c *Client) Peers(ctx context.Context) ([]Session, error) {
	var out []Session
	err := c.request(ctx, http.MethodGet, "/v1/peers", nil, &out)
	return out, err
}

func (c *Client) Poll(ctx context.Context, sessionID string, after int64, wait time.Duration) ([]Message, error) {
	ms := int(wait / time.Millisecond)
	if ms < 0 {
		ms = 0
	}
	if ms > 30000 {
		ms = 30000
	}
	path := "/v1/messages?session=" + url.QueryEscape(sessionID) + "&after=" + strconv.FormatInt(after, 10) + "&wait_ms=" + strconv.Itoa(ms)
	var out []Message
	err := c.request(ctx, http.MethodGet, path, nil, &out)
	return out, err
}

func (c *Client) Ack(ctx context.Context, sessionID string, seq int64) error {
	return c.request(ctx, http.MethodPost, "/v1/acks", map[string]any{"session": sessionID, "seq": seq}, nil)
}

func (c *Client) History(ctx context.Context, sessionID string) ([]Message, error) {
	var out []Message
	err := c.request(ctx, http.MethodGet, "/v1/history?session="+url.QueryEscape(sessionID), nil, &out)
	return out, err
}
