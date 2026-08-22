package rerank

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/enterprise-kb/backend/internal/config"
)

// Client re-ranks retrieval candidates with a cross-encoder reranker.
type Client struct {
	cfg    config.RerankConfig
	client *http.Client
}

// New builds a rerank client. Callers should check Enabled() before use.
func New(cfg config.RerankConfig) *Client {
	return &Client{
		cfg:    cfg,
		client: &http.Client{Timeout: 15 * time.Second},
	}
}

// Enabled reports whether reranking is configured.
func (c *Client) Enabled() bool {
	return c.cfg.Enabled && c.cfg.APIKey != "" && c.cfg.Provider != ""
}

// Rank returns the input indices ordered by descending relevance to query.
// docIndices is the list of candidate document texts; the returned slice has
// the same elements, sorted best-first, truncated to TopN when set.
func (c *Client) Rank(ctx context.Context, query string, docs []string) ([]int, error) {
	if len(docs) == 0 {
		return nil, nil
	}
	scores, err := c.scores(ctx, query, docs)
	if err != nil {
		return nil, err
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
	return order, nil
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
	url := c.cfg.BaseURL
	if url == "" {
		url = "https://api.siliconflow.cn/v1"
	}
	url = url + "/rerank"
	return c.doRank(ctx, url, body, func(raw json.RawMessage) ([]float64, error) {
		var resp struct {
			Results []struct {
				Index           int     `json:"index"`
				RelevanceScore  float64 `json:"relevance_score"`
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
	url := c.cfg.BaseURL
	if url == "" {
		url = "https://api.cohere.ai/v1/rerank"
	}
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
	defer resp.Body.Close()
	var raw json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("rerank api error %d: %s", resp.StatusCode, string(raw))
	}
	return parse(raw)
}
