package services

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// WordWebResult is a single web search result.
type WebResult struct {
	Title   string
	URL     string
	Content string
}

// WebSearchClient performs optional web searches for the chat assistant.
type WebSearchClient struct {
	enabled   bool
	apiKey    string
	baseURL   string
	maxResult int
	client    *http.Client
}

func NewWebSearchClient(enabled bool, apiKey, baseURL string, maxResult int) *WebSearchClient {
	if baseURL == "" {
		baseURL = "https://api.tavily.com/search"
	}
	if maxResult <= 0 {
		maxResult = 5
	}
	return &WebSearchClient{
		enabled:   enabled,
		apiKey:    apiKey,
		baseURL:   baseURL,
		maxResult: maxResult,
		client:    &http.Client{Timeout: 15 * time.Second},
	}
}

func (w *WebSearchClient) Enabled() bool { return w != nil && w.enabled }

var ErrWebSearchDisabled = errors.New("联网搜索未启用,请联系管理员配置")

// Search performs a web search and returns normalized results.
// The configured endpoint follows the Tavily-compatible JSON contract:
//
//	POST {query, max_results} -> {"results": [{"title","url","content"}]}
func (w *WebSearchClient) Search(ctx context.Context, query string) ([]WebResult, error) {
	if !w.Enabled() {
		return nil, ErrWebSearchDisabled
	}
	body, _ := json.Marshal(map[string]interface{}{
		"query":      query,
		"max_results": w.maxResult,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, w.baseURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if w.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+w.apiKey)
	}

	resp, err := w.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("web search api error %d", resp.StatusCode)
	}
	var out struct {
		Results []struct {
			Title   string `json:"title"`
			URL     string `json:"url"`
			Content string `json:"content"`
		} `json:"results"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	results := make([]WebResult, 0, len(out.Results))
	for _, r := range out.Results {
		r.Title = strings.TrimSpace(r.Title)
		r.URL = strings.TrimSpace(r.URL)
		r.Content = strings.TrimSpace(r.Content)
		if r.Title == "" && r.URL == "" {
			continue
		}
		results = append(results, WebResult{Title: r.Title, URL: r.URL, Content: r.Content})
	}
	return results, nil
}