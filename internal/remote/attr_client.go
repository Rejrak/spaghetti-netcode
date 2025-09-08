package remote

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"spaghetti/internal/user"
)

type Client interface {
	FetchAttributes(ctx context.Context, address string) (*user.Attributes, error)
}

// Minimal JSON rest backend: GET /users/{address}
type HTTPClient struct {
	BaseURL string
	Timeout time.Duration
	Client  *http.Client
}

func NewHTTPClient(base string, timeout time.Duration) *HTTPClient {
	return &HTTPClient{
		BaseURL: base,
		Timeout: timeout,
		Client:  &http.Client{Timeout: timeout},
	}
}

func (h *HTTPClient) FetchAttributes(ctx context.Context, address string) (*user.Attributes, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, fmt.Sprintf("%s/users/%s", h.BaseURL, address), nil)
	if err != nil {
		return nil, err
	}
	resp, err := h.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		// address non conosciuto → niente attrs
		return &user.Attributes{CanCreate: false, CanRead: false, CanUpdate: false, CanDelete: false}, nil
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("remote status %d", resp.StatusCode)
	}
	var out user.Attributes
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return &out, nil
}
