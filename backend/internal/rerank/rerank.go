package rerank

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/enterprise-kb/backend/internal/config"
)

// minInterval enforces a minimum spacing between rerank API calls. Cohere's
// trial keys are capped at 10 requests/minute, so without throttling a burst of
// questions would all 429 and the miss-gate would wrongly reject everything.
// Other (OpenAI-compatible) providers such as Jina have far higher quotas, so
// we only throttle hard for Cohere and use a small interval elsewhere.
const cohereInterval = 7 * time.Second
const defaultInterval = 100 * time.Millisecond

// Client re-ranks retrieval candidates with a cross-encoder reranker.
type Client struct {
	cfg    config.RerankConfig
	client *http.Client
	mu     sync.Mutex
	last   time.Time
}

// New builds a rerank client. Callers should check Enabled() before use.
func New(cfg config.RerankConfig) *Client {
	return &Client{
		cfg:    cfg,
		client: &http.Client{Timeout: 20 * time.Second},
	}
}

// Enabled reports whether reranking is configured.
func (c *Client) Enabled() bool {
	return c.cfg.Enabled && c.cfg.APIKey != "" && c.cfg.Provider != ""
}

// Rank returns the input indices ordered by descending relevance to query,
// together with the per-document relevance scores (index-aligned to docs).
// docIndices is the list of candidate document texts; the returned order slice
// has the same elements, sorted best-first, truncated to TopN when set.
func (c *Client) Rank(ctx context.Context, query string, docs []string) ([]int, []float64, error) {
	if len(docs) == 0 {
		return nil, nil, nil
	}
	scores, err := c.scores(ctx, query, docs)
	if err != nil {
		return nil, nil, err
	}
	order := make([]int, len(scores))
	for i := range order {
		order[i] = i
	}
	// simple insertion sort by score desc (n is small, <= ~20)
	for i := 1; i < len(order); i++ {
		j := i
		for j > 0 && scores[order[j]] > scores[order[j-1]] {
			order[j], order[j-1] = order[j-1], order[j]
			j--
		}
	}
	if c.cfg.TopN > 0 && len(order) > c.cfg.TopN {
		order = order[:c.cfg.TopN]
	}
	return order, scores, nil
}

// scores queries the configured rerank provider and returns a relevance score
// per input document (index-aligned).
func (c *Client) scores(ctx context.Context, query string, docs []string) ([]float64, error) {
	switch c.cfg.Provider {
	case "cohere":
		return c.scoreCohere(ctx, query, docs)
	default:
		return c.scoreOpenAI(ctx, query, docs)
	}
}

func (c *Client) scoreOpenAI(ctx context.Context, query string, docs []string) ([]float64, error) {
	body := map[string]interface{}{
		"model":     c.cfg.Model,
		"query":     query,
		"documents": docs,
		"top_n":     len(docs),
	}
	url := strings.TrimRight(c.cfg.BaseURL, "/")
	if url == "" {
		url = "https://api.siliconflow.cn/v1"
	}
	url = url + "/rerank"
	return c.doRank(ctx, url, body, func(raw json.RawMessage) ([]float64, error) {
		var resp struct {
			Results []struct {
				Index          int     `json:"index"`
				RelevanceScore float64 `json:"relevance_score"`
			} `json:"results"`
		}
		if err := json.Unmarshal(raw, &resp); err != nil {
			return nil, err
		}
		out := make([]float64, len(docs))
		for _, r := range resp.Results {
			if r.Index >= 0 && r.Index < len(out) {
				out[r.Index] = r.RelevanceScore
			}
		}
		return out, nil
	})
}

func (c *Client) scoreCohere(ctx context.Context, query string, docs []string) ([]float64, error) {
	body := map[string]interface{}{
		"model":     c.cfg.Model,
		"query":     query,
		"documents": docs,
		"top_n":     len(docs),
	}
	url := strings.TrimRight(c.cfg.BaseURL, "/")
	if url == "" {
		url = "https://api.cohere.ai/v1"
	}
	url = url + "/rerank"
	return c.doRank(ctx, url, body, func(raw json.RawMessage) ([]float64, error) {
		var resp struct {
			Results []struct {
				Index          int     `json:"index"`
				RelevanceScore float64 `json:"relevance_score"`
			} `json:"results"`
		}
		if err := json.Unmarshal(raw, &resp); err != nil {
			return nil, err
		}
		out := make([]float64, len(docs))
		for _, r := range resp.Results {
			if r.Index >= 0 && r.Index < len(out) {
				out[r.Index] = r.RelevanceScore
			}
		}
		return out, nil
	})
}

func (c *Client) doRank(ctx context.Context, url string, body map[string]interface{}, parse func(json.RawMessage) ([]float64, error)) ([]float64, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	// Throttle: ensure at least interval between successive API calls so we
	// stay within the provider's rate limit (Cohere trial: 10/min). Retry a few
	// times on 429 before giving up; the chat pipeline falls back to the vector
	// gate so an exhausted quota never blocks answers outright.
	interval := defaultInterval
	if c.cfg.Provider == "cohere" {
		interval = cohereInterval
	}
	c.mu.Lock()
	if elapsed := time.Since(c.last); elapsed < interval {
		time.Sleep(interval - elapsed)
	}
	c.last = time.Now()
	c.mu.Unlock()

	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
		resp, err := c.client.Do(req)
		if err != nil {
			return nil, err
		}
		raw, decErr := io.ReadAll(resp.Body)
		resp.Body.Close()
		if decErr != nil {
			return nil, decErr
		}
		if resp.StatusCode == http.StatusTooManyRequests {
			lastErr = fmt.Errorf("rerank rate limited (429)")
			log.Printf("[rerank] 429, backing off (%d/4)", attempt+1)
			time.Sleep(interval)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("rerank api error %d", resp.StatusCode)
		}
		return parse(raw)
	}
	return nil, lastErr
}
