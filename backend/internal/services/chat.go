package services

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/enterprise-kb/backend/internal/database"
	"github.com/enterprise-kb/backend/internal/llm"
	"github.com/enterprise-kb/backend/internal/models"
	"github.com/enterprise-kb/backend/internal/rerank"
	"github.com/enterprise-kb/backend/internal/search"
)

var ErrNoAccess = errors.New("no access to this knowledge base")

type ChatService struct {
	search              *search.Service
	embedder            llm.EmbeddingClient
	answerer            llm.AnswerClient
	reranker            *rerank.Client
	web                 *WebSearchClient
	maxChunks           int
	missThreshold       float64
	rerankMissThreshold float64
}

func NewChatService(searchSvc *search.Service, embedder llm.EmbeddingClient, answerer llm.AnswerClient, reranker *rerank.Client, missThreshold, rerankMissThreshold float64) *ChatService {
	if missThreshold <= 0 {
		missThreshold = 0.25
	}
	if rerankMissThreshold <= 0 {
		rerankMissThreshold = 0.1
	}
	return &ChatService{
		search:              searchSvc,
		embedder:            embedder,
		answerer:            answerer,
		reranker:            reranker,
		maxChunks:           6,
		missThreshold:       missThreshold,
		rerankMissThreshold: rerankMissThreshold,
	}
}

func (s *ChatService) SetWebSearch(client *WebSearchClient) {
	s.web = client
}

type AskInput struct {
	TenantID  uuid.UUID
	UserID    uuid.UUID
	UserName  string
	SessionID uuid.UUID
	KBID      string // optional: scope to a KB
	Question  string
	History   []llm.HistoryTurn
	IsAdmin   bool
	Dept      string
	// WebSearch lets the user opt into web search for this question,
	// used when KB retrieval is empty or miss.
	WebSearch bool
}

type AskResult struct {
	SessionID    uuid.UUID
	UserMsg      models.ChatMessage
	AssistantMsg models.ChatMessage
	Answer       string
	Citations    []models.Citation
	IsMissed     bool
}

// AskStream is the preparsed state of an Ask before answer generation. It
// lets the SSE handler stream tokens while still sharing the exact same
// retrieval / persistence logic as the synchronous Ask.
type AskStream struct {
	// In carries the original request.
	In      AskInput
	Session *models.ChatSession
	UserMsg *models.ChatMessage
	// Citations / ChunkCtx are computed from retrieval (+ optional web search),
	// before the LLM judges relevance.
	Citations []models.Citation
	ChunkCtx  []llm.ContextChunk
	PreMissed bool
}

// AskStreamInit returns an AskStream (retrieval + session + user message
// persisted) ready to be streamed via StreamAnswer. Used by the SSE endpoint
// so the client can run askPrep errors as normal JSON errors.
func (s *ChatService) AskStreamInit(in AskInput) (*AskStream, error) {
	return s.askPrep(in)
}

// Ask runs the full synchronous pipeline (used by the IM bots and as the
// non-streaming fallback for web clients).
func (s *ChatService) Ask(in AskInput) (*AskResult, error) {
	st, err := s.askPrep(in)
	if err != nil {
		return nil, err
	}
	return s.StreamAnswer(context.Background(), st, nil)
}

// askPrep performs everything before answer generation: KB access check,
// retrieval, web search fusion, session + user message persistence.
func (s *ChatService) askPrep(in AskInput) (*AskStream, error) {
	question := strings.TrimSpace(in.Question)
	if question == "" {
		return nil, errors.New("question required")
	}

	// resolve KB + access check
	var kb models.KnowledgeBase
	var kbID uuid.UUID
	if in.KBID != "" {
		id, err := uuid.Parse(in.KBID)
		if err != nil {
			return nil, errors.New("invalid kb id")
		}
		kbID = id
		if err := database.DB.First(&kb, "id = ? AND tenant_id = ?", kbID, in.TenantID).Error; err != nil {
			return nil, errors.New("knowledge base not found")
		}
		if !in.IsAdmin && !canSeeKB(kb, in.Dept) {
			return nil, ErrNoAccess
		}
	}

	// compute visibility filter
	var visibleTags []string
	if in.IsAdmin {
		visibleTags = nil // no filter for admins
	} else {
		visibleTags = visibleTagsForUser(in.Dept, false)
	}

	// 1) hybrid retrieval
	hits, err := s.retrieve(context.Background(), in.TenantID, in.KBID, question, visibleTags, in.IsAdmin)
	if err != nil {
		return nil, fmt.Errorf("retrieval failed: %w", err)
	}

	// ensure session exists
	session, err := s.ensureSession(in.SessionID, in.TenantID, in.UserID)
	if err != nil {
		return nil, err
	}

	// save user message
	userMsg := models.ChatMessage{
		SessionID: session.ID,
		TenantID:  in.TenantID,
		UserID:    in.UserID,
		Role:      "user",
		Content:   question,
	}
	if err := database.DB.Create(&userMsg).Error; err != nil {
		return nil, err
	}

	// 2) relevance gate. The cross-encoder rerank score is the most
	// discriminative relevance signal, so we gate on it first — but only when the
	// rerank actually returned scores (bestRerank > 0). If the reranker is rate
	// limited / unavailable, bestRerank stays 0 and we fall back to the vector
	// cosine gate, and finally to the strict "no hits" rule. Gating on the cosine
	// (or BM25) alone let weakly related chunks leak off-topic answers, so the
	// rerank score — trained to separate relevant from irrelevant — is preferred
	// whenever available.
	bestVec := 0.0
	bestRerank := 0.0
	if len(hits) > 0 {
		bestRerank = hits[0].RerankScore
	}
	for _, h := range hits {
		if h.VectorScore > bestVec {
			bestVec = h.VectorScore
		}
	}
	reranked := bestRerank > 0
	isMissed := len(hits) == 0
	// Primary miss signal is the vector cosine similarity (rate-limit free with
	// Cohere embeddings and well separated on this corpus: off-topic queries
	// score <= ~0.52, in-scope >= ~0.55). The rerank score is a secondary,
	// stricter signal used only when a reranker returns discriminative scores
	// (e.g. Cohere reranker-v3.5). A query is a miss if EITHER signal is below
	// its threshold — this keeps clearly off-topic questions out while never
	// rejecting in-scope ones, and degrades gracefully if the reranker is rate
	// limited (bestRerank stays 0 and only the vector gate applies).
	if s.embedder.Enabled() && len(hits) > 0 && bestVec < s.missThreshold {
		isMissed = true
	}
	if reranked && bestRerank < s.rerankMissThreshold {
		isMissed = true
	}
	if isMissed {
		hits = nil
	}

	var citations []models.Citation
	chunkCtx := make([]llm.ContextChunk, 0, len(hits))

	if in.WebSearch && s.web != nil && s.web.Enabled() {
		results, err := s.web.Search(context.Background(), question)
		if err != nil {
			logWarn("web search failed: %v", err)
		} else if len(results) > 0 {
			if isMissed {
				isMissed = false
			}
			for _, r := range results {
				title := strings.TrimSpace(r.Title)
				if title == "" {
					title = r.URL
				}
				citations = append(citations, models.Citation{
					DocTitle:  title,
					Snippet:   truncate(r.Content, 160),
					ChunkText: r.Content,
					URL:       r.URL,
				})
				chunkCtx = append(chunkCtx, llm.ContextChunk{
					DocTitle: title,
					DocURL:   r.URL,
					Snippet:  r.Content,
					Text:     r.Content,
				})
			}
		}
	}

	// batch-fetch document titles once instead of one query per hit (N+1)
	var docIDs []string
	for _, h := range hits {
		docIDs = append(docIDs, h.DocID)
	}
	var docs []models.Document
	if len(docIDs) > 0 {
		database.DB.Where("id IN ?", docIDs).Find(&docs)
	}
	titleByID := make(map[string]string, len(docs))
	for _, d := range docs {
		titleByID[d.ID.String()] = d.Title
	}
	for _, h := range hits {
		c := models.Citation{
			DocID:      h.DocID,
			DocTitle:   titleByID[h.DocID],
			ChunkID:    h.ID,
			Snippet:    truncate(h.Text, 160),
			Score:      h.Score,
			ChunkText:  h.Text,
			ChunkIndex: h.ChunkIndex,
			Page:       h.Page,
		}
		citations = append(citations, c)
		chunkCtx = append(chunkCtx, llm.ContextChunk{
			DocTitle: titleByID[h.DocID],
			Snippet:  h.Text,
			Text:     h.Text,
			Score:    h.Score,
		})
	}

	return &AskStream{
		In:        in,
		Session:   session,
		UserMsg:   &userMsg,
		Citations: citations,
		ChunkCtx:  chunkCtx,
		PreMissed: isMissed,
	}, nil
}

// StreamAnswer generates the final answer (streamed via onDelta when set),
// then persists the assistant message / audit / session metadata.
func (s *ChatService) StreamAnswer(ctx context.Context, st *AskStream, onDelta func(string)) (*AskResult, error) {
	question := st.In.Question
	emit := func(p string) {
		if onDelta != nil && p != "" {
			onDelta(p)
		}
	}

	var result llm.AnswerResult
	var err error
	if s.answerer.Enabled() {
		result, err = s.answerer.GenerateStream(ctx, question, st.In.History, st.ChunkCtx, emit)
	} else {
		result, err = s.answerer.Generate(ctx, question, st.In.History, st.ChunkCtx)
		if err == nil {
			emit(result.Answer)
		}
	}
	if err != nil {
		logWarn("llm generate failed: %v", err)
		off, _ := llm.OfflineGenerate(question, st.In.History, st.ChunkCtx)
		result = llm.AnswerResult{Answer: off, Hit: len(st.Citations) > 0}
		if onDelta != nil && result.Answer != "" {
			onDelta(result.Answer)
		}
	}

	answer := strings.TrimSpace(result.Answer)
	isMissed := st.PreMissed
	citations := st.Citations
	chunkCtx := st.ChunkCtx
	if answer == "" {
		answer = "抱歉，我暂时没有找到合适的答案。建议联系知识管理员补充相关知识。"
		isMissed = true
	} else if !result.Hit {
		// The LLM judged the retrieved chunks irrelevant to the question;
		// treat as a knowledge gap so it lands in admin gaps / is_missed stats,
		// but keep the model's "no answer" explanation.
		isMissed = true
		citations = nil
		chunkCtx = chunkCtx[:0]
	}

	assistantMsg := models.ChatMessage{
		SessionID: st.Session.ID,
		TenantID:  st.In.TenantID,
		UserID:    st.In.UserID,
		Role:      "assistant",
		Content:   answer,
		Citations: citations,
		IsMissed:  isMissed,
	}
	if err := database.DB.Create(&assistantMsg).Error; err != nil {
		return nil, err
	}

	// audit
	database.DB.Create(&models.AuditLog{
		TenantID: st.In.TenantID,
		UserID:   st.In.UserID,
		UserName: st.In.UserName,
		Action:   "ask",
		Detail:   fmt.Sprintf("问题：%s；命中：%d 条；未命中：%v", truncate(question, 100), len(citations), isMissed),
	})

	// update session title on first message
	if st.Session.Title == "" {
		database.DB.Model(st.Session).Update("title", truncate(question, 20))
	}

	// update last activity
	database.DB.Model(st.Session).Update("updated_at", nowFunc())

	return &AskResult{
		SessionID:    st.Session.ID,
		UserMsg:      *st.UserMsg,
		AssistantMsg: assistantMsg,
		Answer:       answer,
		Citations:    citations,
		IsMissed:     isMissed,
	}, nil
}

// retrieve runs BM25 + optional vector search and merges via RRF.
func (s *ChatService) retrieve(ctx context.Context, tenantID uuid.UUID, kbID string, query string, visibleTags []string, isAdmin bool) ([]search.SearchResult, error) {
	var kbFilter string
	if kbID != "" {
		kbFilter = kbID
	}

	bm25Hits, err := s.search.Search(tenantID.String(), kbFilter, query, visibleTags, int64(s.maxChunks))
	if err != nil {
		return nil, err
	}

	// optional vector results
	var vecHits []search.SearchResult
	if s.embedder.Enabled() {
		vecHits, err = s.vectorSearch(tenantID, kbFilter, query, visibleTags, isAdmin)
		if err != nil {
			logWarn("vector search failed: %v", err)
		}
	}

	if len(vecHits) == 0 {
		return s.rerankIfEnabled(ctx, query, bm25Hits), nil
	}
	merged := rrfMerge(bm25Hits, vecHits, int64(s.maxChunks))
	return s.rerankIfEnabled(ctx, query, merged), nil
}

// rerankIfEnabled re-orders retrieval candidates with a cross-encoder reranker
// when one is configured. Falls back to the original order on any error so the
// chat pipeline never breaks because of a rerank outage.
func (s *ChatService) rerankIfEnabled(ctx context.Context, query string, hits []search.SearchResult) []search.SearchResult {
	if s.reranker == nil || !s.reranker.Enabled() || len(hits) <= 1 {
		return hits
	}
	texts := make([]string, len(hits))
	for i, h := range hits {
		texts[i] = h.Text
	}
	order, scores, err := s.reranker.Rank(ctx, query, texts)
	if err != nil {
		logWarn("rerank failed, using RRF order: %v", err)
		return hits
	}
	out := make([]search.SearchResult, 0, len(order))
	for _, idx := range order {
		if idx >= 0 && idx < len(hits) {
			h := hits[idx]
			if idx < len(scores) {
				h.Score = scores[idx]
				h.RerankScore = scores[idx]
			}
			out = append(out, h)
		}
	}
	if len(out) == 0 {
		return hits
	}
	return out
}

func (s *ChatService) vectorSearch(tenantID uuid.UUID, kbID, query string, visibleTags []string, isAdmin bool) ([]search.SearchResult, error) {
	emb, err := s.embedder.EmbedQuery(context.Background(), query)
	if err != nil || len(emb) == 0 {
		return nil, err
	}
	// models.Vector serializes to the "[...]" text pgvector expects (plain
	// []float32 would be sent as a record and rejected by the <=> operator).
	vec := models.Vector(emb)

	// Args must match the SQL placeholder order: the SELECT's `embedding <=> ?`
	// comes first (vec), then the WHERE tenant_id (tenantID), then optional
	// filters, and finally the ORDER BY's `embedding <=> ?` (vec appended below).
	args := []interface{}{vec, tenantID}
	where := "tenant_id = ? AND embedding IS NOT NULL"
	if kbID != "" {
		where += " AND kb_id = ?"
		args = append(args, kbID)
	}
	if !isAdmin {
		tags := visibleTags
		if len(tags) == 0 {
			tags = []string{"public"}
		}
		// jsonb_exists_any avoids the `?|` operator, which collides with the
		// driver's `?` placeholder and breaks the query.
		where += " AND jsonb_exists_any(visibility, array[" + placeholders(len(tags)) + "])"
		for _, t := range tags {
			args = append(args, t)
		}
	}

	rows, err := database.DB.Raw(
		"SELECT id::text, kb_id::text, doc_id::text, chunk_index, page, title, text, 1 - (embedding <=> ?::vector) AS score FROM chunks WHERE "+
			where+" ORDER BY embedding <=> ?::vector LIMIT 6",
		append(args, vec)...,
	).Rows()
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []search.SearchResult
	for rows.Next() {
		var r search.SearchResult
		if err := rows.Scan(&r.ID, &r.KBID, &r.DocID, &r.ChunkIndex, &r.Page, &r.Title, &r.Text, &r.Score); err != nil {
			continue
		}
		r.RawScore = r.Score
		r.VectorScore = r.Score
		out = append(out, r)
	}
	return out, rows.Err()
}

func placeholders(n int) string {
	parts := make([]string, n)
	for i := range parts {
		parts[i] = "?"
	}
	return strings.Join(parts, ",")
}

func rrfMerge(a, b []search.SearchResult, limit int64) []search.SearchResult {
	type entry struct {
		hit   search.SearchResult
		score float64
		raw   float64
		vec   float64
	}
	m := map[string]*entry{}
	var order []string
	for i, h := range a {
		if _, ok := m[h.ID]; !ok {
			m[h.ID] = &entry{hit: h, raw: h.RawScore, vec: h.VectorScore}
			order = append(order, h.ID)
		}
		m[h.ID].score += 1.0 / (60 + float64(i))
		if h.RawScore > m[h.ID].raw {
			m[h.ID].raw = h.RawScore
		}
		if h.VectorScore > m[h.ID].vec {
			m[h.ID].vec = h.VectorScore
		}
	}
	for i, h := range b {
		if _, ok := m[h.ID]; !ok {
			m[h.ID] = &entry{hit: h, raw: h.RawScore, vec: h.VectorScore}
			order = append(order, h.ID)
		}
		m[h.ID].score += 1.0 / (60 + float64(i))
		if h.RawScore > m[h.ID].raw {
			m[h.ID].raw = h.RawScore
		}
		if h.VectorScore > m[h.ID].vec {
			m[h.ID].vec = h.VectorScore
		}
	}
	out := make([]search.SearchResult, 0, len(order))
	for _, id := range order {
		e := m[id]
		e.hit.Score = e.score
		e.hit.RawScore = e.raw
		e.hit.VectorScore = e.vec
		out = append(out, e.hit)
	}
	if int64(len(out)) > limit {
		out = out[:limit]
	}
	return out
}

func (s *ChatService) ensureSession(sessionID uuid.UUID, tenantID, userID uuid.UUID) (*models.ChatSession, error) {
	if sessionID != uuid.Nil {
		var s models.ChatSession
		if err := database.DB.First(&s, "id = ? AND tenant_id = ? AND user_id = ?", sessionID, tenantID, userID).Error; err == nil {
			return &s, nil
		}
	}
	sess := models.ChatSession{TenantID: tenantID, UserID: userID}
	if err := database.DB.Create(&sess).Error; err != nil {
		return nil, err
	}
	return &sess, nil
}

func canSeeKB(kb models.KnowledgeBase, dept string) bool {
	allowed := splitTags(kb.AllowedDepartments)
	if len(allowed) == 0 {
		return true
	}
	for _, a := range allowed {
		if a == dept || a == "全员" || a == "全部" {
			return true
		}
	}
	return false
}

func truncate(s string, n int) string {
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	return string(rs[:n]) + "..."
}

// nowFunc returns current time (kept as var for testability).
var nowFunc = func() interface{} { return timeNow() }

// provided by timeutil below
