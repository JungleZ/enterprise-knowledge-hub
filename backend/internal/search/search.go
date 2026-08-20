package search

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/meilisearch/meilisearch-go"

	"github.com/enterprise-kb/backend/internal/config"
	"github.com/enterprise-kb/backend/internal/models"
)

const chunksIndex = "kb_chunks"

// ChunkDoc is the document shape stored in Meilisearch.
type ChunkDoc struct {
	ID         string   `json:"id"`
	TenantID   string   `json:"tenant_id"`
	KBID       string   `json:"kb_id"`
	DocID      string   `json:"doc_id"`
	ChunkIndex int      `json:"chunk_index"`
	Title      string   `json:"title"`
	Text       string   `json:"text"`
	Visibility []string `json:"visibility"`
}

type Service struct {
	client meilisearch.ServiceManager
	index  meilisearch.IndexManager
}

func NewService(cfg config.MeiliConfig) *Service {
	opts := []meilisearch.Option{}
	if cfg.APIKey != "" {
		opts = append(opts, meilisearch.WithAPIKey(cfg.APIKey))
	}
	client := meilisearch.New(cfg.Host, opts...)
	return &Service{client: client, index: client.Index(chunksIndex)}
}

func (s *Service) InitIndexes() error {
	// ensure index exists with primary key "id"
	_, err := s.client.CreateIndex(&meilisearch.IndexConfig{Uid: chunksIndex, PrimaryKey: "id"})
	if err != nil && !isIndexExistsError(err) {
		return fmt.Errorf("create index: %w", err)
	}
	settings := &meilisearch.Settings{
		FilterableAttributes: []string{
			"tenant_id", "kb_id", "doc_id", "visibility",
		},
		SearchableAttributes: []string{"text", "title"},
		RankingRules: []string{
			"words",
			"typo",
			"proximity",
			"attribute",
			"sort",
			"exactness",
		},
	}
	task, err := s.index.UpdateSettings(settings)
	if err != nil {
		return fmt.Errorf("update settings: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return s.waitTask(ctx, task)
}

func isIndexExistsError(err error) bool {
	return err != nil && (err.Error() == "index already exists" || contains(err.Error(), "index_already_exists"))
}

func contains(s, sub string) bool { return len(s) >= len(sub) && indexOf(s, sub) >= 0 }

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func (s *Service) waitTask(ctx context.Context, task *meilisearch.TaskInfo) error {
	if task == nil {
		return nil
	}
	for {
		info, err := s.client.GetTask(task.TaskUID)
		if err != nil {
			return err
		}
		switch info.Status {
		case "succeeded":
			return nil
		case "failed":
			return fmt.Errorf("meilisearch task failed: %+v", info.Error)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(300 * time.Millisecond):
		}
	}
}

func (s *Service) IndexChunks(chunks []models.Chunk) error {
	if len(chunks) == 0 {
		return nil
	}
	docs := make([]ChunkDoc, 0, len(chunks))
	for _, ch := range chunks {
		docs = append(docs, ChunkDoc{
			ID:         ch.ID.String(),
			TenantID:   ch.TenantID.String(),
			KBID:       ch.KBID.String(),
			DocID:      ch.DocID.String(),
			ChunkIndex: ch.ChunkIndex,
			Title:      ch.Title,
			Text:       ch.Text,
			Visibility: ch.Visibility,
		})
	}
	task, err := s.index.AddDocuments(docs, nil)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	return s.waitTask(ctx, task)
}

func (s *Service) DeleteByDocID(docID uuid.UUID) error {
	filter := fmt.Sprintf("doc_id = %q", docID.String())
	task, err := s.index.DeleteDocumentsByFilter(filter, nil)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return s.waitTask(ctx, task)
}

func (s *Service) DeleteByKBID(kbID uuid.UUID) error {
	filter := fmt.Sprintf("kb_id = %q", kbID.String())
	task, err := s.index.DeleteDocumentsByFilter(filter, nil)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return s.waitTask(ctx, task)
}

func (s *Service) DeleteByTenantID(tenantID uuid.UUID) error {
	filter := fmt.Sprintf("tenant_id = %q", tenantID.String())
	task, err := s.index.DeleteDocumentsByFilter(filter, nil)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return s.waitTask(ctx, task)
}

// Hit is a retrieved chunk.
type Hit struct {
	ID         string   `json:"id"`
	KBID       string   `json:"kb_id"`
	DocID      string   `json:"doc_id"`
	ChunkIndex int      `json:"chunk_index"`
	Title      string   `json:"title"`
	Text       string   `json:"text"`
	Visibility []string `json:"visibility"`
}

type SearchResult struct {
	Hit
	Score float64
}

// Search performs BM25 full-text retrieval with tenant/visibility filtering.
// visibleTags nil means no visibility restriction (admin).
// To compensate for Meilisearch's weak CJK tokenization, when the primary
// query returns no hits it retries with derived candidate queries and merges.
func (s *Service) Search(tenantID, kbID string, query string, visibleTags []string, limit int64) ([]SearchResult, error) {
	if limit <= 0 {
		limit = 6
	}
	filter := fmt.Sprintf("tenant_id = %q", tenantID)
	if kbID != "" {
		filter += fmt.Sprintf(" AND kb_id = %q", kbID)
	}
	if visibleTags != nil {
		filter += fmt.Sprintf(" AND visibility IN [%s]", joinQuoted(visibleTags))
	}

	primary, err := s.searchOnce(filter, query, limit)
	if err != nil {
		return nil, err
	}
	if len(primary) > 0 {
		return primary, nil
	}

	// CJK query expansion: Meilisearch tokenizes Chinese by character, so a
	// natural-language query rarely overlaps with indexed terms. Retry with
	// derived keyword queries and merge results.
	merged := map[string]SearchResult{}
	var order []string
	add := func(rs []SearchResult) {
		for _, r := range rs {
			if _, ok := merged[r.ID]; !ok {
				order = append(order, r.ID)
			}
			if merged[r.ID].Score < r.Score {
				merged[r.ID] = r
			}
		}
	}
	any := false
	for _, cand := range expandQueries(query) {
		if cand == "" {
			continue
		}
		rs, err := s.searchOnce(filter, cand, limit)
		if err != nil {
			continue
		}
		if len(rs) > 0 {
			any = true
		}
		add(rs)
	}
	if !any {
		return primary, nil
	}
	out := make([]SearchResult, 0, len(order))
	for _, id := range order {
		out = append(out, merged[id])
	}
	if int64(len(out)) > limit {
		out = out[:limit]
	}
	return out, nil
}

// expandQueries derives candidate search queries for CJK fallback.
func expandQueries(query string) []string {
	var cands []string
	seen := map[string]bool{}
	add := func(q string) {
		q = strings.TrimSpace(q)
		if q != "" && q != query && !seen[q] {
			seen[q] = true
			cands = append(cands, q)
		}
	}

	// 1) query with common question particles removed
	cleaned := stripCJKNoise(query)
	add(cleaned)

	// 2) whitespace/punct-separated words (useful for mixed zh-en queries)
	for _, w := range splitWords(query) {
		add(w)
	}

	// 3) CJK character n-grams (bigrams are the most discriminative)
	ng := cjkBigrams(query)
	for _, g := range ng {
		add(g)
	}

	// 4) join extracted n-grams with spaces (AND is too strict, so keep 1-2)
	if len(ng) >= 2 {
		add(ng[0] + " " + ng[1])
	}
	return cands
}

// splitWords splits a query on whitespace/punctuation.
func splitWords(q string) []string {
	var out []string
	for _, w := range strings.FieldsFunc(q, func(r rune) bool {
		return strings.ContainsRune(" 　,.;:!?、，。；：！？()（）\"'《》<>/\\|-_+*%#@~`", r)
	}) {
		if len([]rune(w)) >= 2 {
			out = append(out, w)
		}
	}
	return out
}

// stripCJKNoise removes common Chinese question/function words from a query.
func stripCJKNoise(q string) string {
	noise := []string{"这个", "那个", "项目", "产品", "公司", "请问", "一个", "如何", "怎么", "怎样", "什么", "哪个", "哪些", "吗", "呢", "了", "的", "和", "与", "是", "用", "使用", "有", "我", "我们", "你们", "请问一下", "帮", "帮我"}
	for _, n := range noise {
		q = strings.ReplaceAll(q, n, "")
	}
	return strings.TrimSpace(q)
}

// cjkBigrams returns consecutive 2-rune bigrams of Chinese text.
func cjkBigrams(q string) []string {
	var rs []rune
	for _, r := range q {
		if r > 127 {
			rs = append(rs, r)
		}
	}
	var out []string
	for i := 0; i+1 < len(rs); i++ {
		out = append(out, string(rs[i])+string(rs[i+1]))
	}
	return out
}

func (s *Service) searchOnce(filter, query string, limit int64) ([]SearchResult, error) {
	req := &meilisearch.SearchRequest{
		Filter: filter,
		Limit:  limit,
		AttributesToRetrieve: []string{
			"id", "kb_id", "doc_id", "chunk_index", "title", "text", "visibility",
		},
	}
	resp, err := s.index.Search(query, req)
	if err != nil {
		return nil, err
	}
	out := make([]SearchResult, 0, len(resp.Hits))
	for _, h := range resp.Hits {
		m := map[string]interface{}{}
		if err := h.DecodeInto(&m); err != nil {
			continue
		}
		hit := SearchResult{Score: 0.5}
		if id, ok := m["id"].(string); ok {
			hit.ID = id
		}
		if v, ok := m["kb_id"].(string); ok {
			hit.KBID = v
		}
		if v, ok := m["doc_id"].(string); ok {
			hit.DocID = v
		}
		if v, ok := m["chunk_index"].(float64); ok {
			hit.ChunkIndex = int(v)
		}
		if v, ok := m["title"].(string); ok {
			hit.Title = v
		}
		if v, ok := m["text"].(string); ok {
			hit.Text = v
		}
		if v, ok := m["visibility"].([]interface{}); ok {
			for _, x := range v {
				if s, ok := x.(string); ok {
					hit.Visibility = append(hit.Visibility, s)
				}
			}
		}
		out = append(out, hit)
	}
	return out, nil
}

func joinQuoted(tags []string) string {
	out := ""
	for i, t := range tags {
		if i > 0 {
			out += ","
		}
		out += fmt.Sprintf("%q", t)
	}
	return out
}
