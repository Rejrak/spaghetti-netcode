package policy

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/big"
	"net/http"
	"net/url"
	"time"
)

type CosmosBalanceClient struct {
	LCDBaseURL string
	Denom      string
	MinAmount  *big.Int
	Timeout    time.Duration
	Client     *http.Client
}

func NewCosmosBalanceClient(lcd, denom string, min *big.Int, timeout time.Duration) *CosmosBalanceClient {
	return &CosmosBalanceClient{
		LCDBaseURL: lcd,
		Denom:      denom,
		MinAmount:  new(big.Int).Set(min),
		Timeout:    timeout,
		Client:     &http.Client{Timeout: timeout},
	}
}

type lcdBalanceByDenom struct {
	Balance struct {
		Denom  string `json:"denom"`
		Amount string `json:"amount"`
	} `json:"balance"`
}

func (c *CosmosBalanceClient) Evaluate(ctx context.Context, in *Context) (Decision, error) {
	if c.Client == nil {
		c.Client = &http.Client{Timeout: c.Timeout}
	}
	if in == nil || in.Address == "" {
		return Decision{}, fmt.Errorf("invalid request: empty address")
	}

	u, err := url.Parse(c.LCDBaseURL)
	if err != nil {
		return Decision{}, err
	}
	u.Path = fmt.Sprintf("/cosmos/bank/v1beta1/balances/%s/by_denom", in.Address)
	q := url.Values{}
	q.Set("denom", c.Denom)
	u.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Decision{}, err
	}
	resp, err := c.Client.Do(req)
	if err != nil {
		return Decision{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Decision{}, fmt.Errorf("LCD status %d", resp.StatusCode)
	}

	var out lcdBalanceByDenom
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return Decision{}, err
	}
	slog.Info("CosmosBalanceClient",
		slog.String("address", in.Address),
		slog.String("denom", out.Balance.Denom),
		slog.String("amount", out.Balance.Amount),
	)

	amt := new(big.Int)
	if _, ok := amt.SetString(out.Balance.Amount, 10); !ok {
		amt.SetInt64(0)
	}
	allow := amt.Cmp(c.MinAmount) >= 0

	msg := ""
	if allow {
		msg = fmt.Sprintf("balance %s %s ≥ threshold %s", out.Balance.Amount, c.Denom, c.MinAmount.String())
	} else {
		msg = fmt.Sprintf("balance %s %s < required %s", out.Balance.Amount, c.Denom, c.MinAmount.String())
	}

	return Decision{
		Allow:   allow,
		Message: msg,
		TTL:     5 * time.Second,
	}, nil
}
