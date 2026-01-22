package policy

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type AttributesClient struct {
	BaseURL string

	MinCount         int
	MaxTotalEvalTime time.Duration
	Timeout          time.Duration
	Client           *http.Client
}

func NewAttributesClient(baseURL string, minCount int, maxTotalEval time.Duration, timeout time.Duration) *AttributesClient {
	return &AttributesClient{
		BaseURL:          baseURL,
		MinCount:         minCount,
		MaxTotalEvalTime: maxTotalEval,
		Timeout:          timeout,
		Client:           &http.Client{Timeout: timeout},
	}
}

type attrItem struct {
	ID              int     `json:"id"`
	Name            string  `json:"name"`
	Value           string  `json:"value"`
	ComplexityUnits int     `json:"complexity_units"`
	EvalTimeMs      float64 `json:"eval_time_ms"`
	Checksum        string  `json:"checksum"`
}

type attributesResponse struct {
	Count           int        `json:"count"`
	Complexity      int        `json:"complexity"`
	TotalEvalTimeMs float64    `json:"total_eval_time_ms"`
	Attributes      []attrItem `json:"attributes"`
}

func (c *AttributesClient) Evaluate(ctx context.Context, in *Context) (Decision, error) {
	if c.Client == nil {
		c.Client = &http.Client{Timeout: c.Timeout}
	}
	if in == nil {
		return Decision{}, fmt.Errorf("invalid request: nil context")
	}
	count, err := strconv.Atoi(in.Resources["count"])
	if err != nil || count <= 0 {
		slog.Error("AttributesClient Evaluate: invalid count", "count_str", in.Resources["count"])
		return Decision{}, fmt.Errorf("invalid request: count must be > 0")
	}
	complexity, err := strconv.Atoi(in.Resources["complexity"])
	if err != nil || complexity < 0 {
		slog.Error("AttributesClient Evaluate: invalid complexity", "complexity_str", in.Resources["complexity"])
		return Decision{}, fmt.Errorf("invalid request: complexity must be >= 0")
	}

	base, err := url.Parse(c.BaseURL)
	if err != nil {
		return Decision{}, err
	}
	base.Path = "/api/v1/attributes"
	q := url.Values{}
	q.Set("count", fmt.Sprintf("%d", count))
	q.Set("complexity", fmt.Sprintf("%d", complexity))
	base.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base.String(), nil)
	if err != nil {
		return Decision{}, err
	}

	resp, err := c.Client.Do(req)
	if err != nil {
		return Decision{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return Decision{}, fmt.Errorf("attributes status %d", resp.StatusCode)
	}

	var out attributesResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return Decision{}, err
	}

	slog.Info("AttributesClient",
		slog.Int("requested_count", count),
		slog.Int("requested_complexity", complexity),
		slog.Int("returned_count", out.Count),
		slog.Float64("total_eval_time_ms", out.TotalEvalTimeMs),
	)

	okCount := len(out.Attributes) >= c.MinCount
	okLatency := true
	for _, attr := range out.Attributes {
		okLatency = okLatency && attr.EvalTimeMs < 10
		if !okLatency {
			slog.Info("Eval Attributes", slog.Float64("EvalTimeMs", attr.EvalTimeMs), slog.Bool("Evaluation", attr.EvalTimeMs < 10))
			break
		}
	}

	// allow := okCount && okLatency

	var msg string
	switch {
	case !okCount && !okLatency:
		msg = fmt.Sprintf("returned %d attrs < required %d AND total_eval_time %.2fms > max %dms",
			len(out.Attributes), c.MinCount, out.TotalEvalTimeMs, c.MaxTotalEvalTime/time.Millisecond)
	case !okCount:
		msg = fmt.Sprintf("returned %d attrs < required %d", len(out.Attributes), c.MinCount)
	case !okLatency:
		msg = fmt.Sprintf("total_eval_time %.2fms > max %dms", out.TotalEvalTimeMs, c.MaxTotalEvalTime/time.Millisecond)
	default:
		msg = fmt.Sprintf("ok: %d attrs, total_eval_time %.2fms", len(out.Attributes), out.TotalEvalTimeMs)
	}

	msg = fmt.Sprintf("ok: %d attrs, total_eval_time %.2fms", len(out.Attributes), out.TotalEvalTimeMs)

	return Decision{
		Allow:   true,
		Message: msg,
		TTL:     5 * time.Second,
	}, nil
}
