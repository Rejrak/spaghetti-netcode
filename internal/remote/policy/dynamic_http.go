package policy

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

type HTTPDynamicClient struct {
	BaseURL   string
	Timeout   time.Duration
	AuthToken string
	HMACKey   []byte
	Client    *http.Client
	Retries   int
}

func NewHTTPDynamicClient(base string, timeout time.Duration) *HTTPDynamicClient {
	return &HTTPDynamicClient{
		BaseURL: base,
		Timeout: timeout,
		Client:  &http.Client{Timeout: timeout},
		Retries: 1,
	}
}

type httpReq struct {
	Session   string            `json:"session,omitempty"`
	Address   string            `json:"address"`
	Operation string            `json:"operation"`
	Resources map[string]string `json:"resources,omitempty"`
}

type httpResp struct {
	Allow     bool   `json:"allow"`
	Message   string `json:"message"`
	TTLSecond int    `json:"ttl_seconds,omitempty"`
}

func (c *HTTPDynamicClient) Evaluate(ctx context.Context, in *Context) (Decision, error) {
	if c.Client == nil {
		c.Client = &http.Client{Timeout: c.Timeout}
	}
	payload := httpReq{
		Session:   in.Session,
		Address:   in.Address,
		Operation: in.Operation,
		Resources: in.Resources,
	}
	body, _ := json.Marshal(payload)

	var lastErr error
	tries := c.Retries + 1
	for i := 0; i < tries; i++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/policy/evaluate", bytes.NewReader(body))
		if err != nil {
			return Decision{}, err
		}
		req.Header.Set("Content-Type", "application/json")
		if c.AuthToken != "" {
			req.Header.Set("Authorization", "Bearer "+c.AuthToken)
		}
		if len(c.HMACKey) > 0 {
			req.Header.Set("X-Signature", signHMAC(body, c.HMACKey))
		}
		resp, err := c.Client.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			lastErr = fmt.Errorf("dynamic policy status %d", resp.StatusCode)
			continue
		}
		var out httpResp
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			lastErr = err
			continue
		}
		return Decision{
			Allow:   out.Allow,
			Message: out.Message,
			TTL:     time.Duration(out.TTLSecond) * time.Second,
		}, nil
	}
	if lastErr == nil {
		lastErr = errors.New("dynamic policy: unknown error")
	}
	return Decision{}, lastErr
}

func signHMAC(b, key []byte) string {
	m := hmac.New(sha256.New, key)
	m.Write(b)
	return hex.EncodeToString(m.Sum(nil))
}
