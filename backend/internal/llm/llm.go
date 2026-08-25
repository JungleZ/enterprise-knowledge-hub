package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/enterprise-kb/backend/internal/config"
)

// EmbeddingClient produces vector embeddings (optional).
type EmbeddingClient interface {
	Enabled() bool
	Dim() int
	// Embed encodes a batch of document/section texts.
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	// EmbedQuery encodes a single search query. Providers that need a
	// different input type for queries (e.g. Cohere) override this.
	EmbedQuery(ctx context.Context, text string) ([]float32, error)
}

// AnswerResult bundles the final answer with a structured relevance verdict.
// Hit=false means the model judged the provided references insufficient to
// answer the question (knowledge gap), not a substring guess of the text.
type AnswerResult struct {
	Answer string
	Hit    bool
}

// AnswerClient generates final answers from retrieved chunks (optional).
type AnswerClient interface {
	Enabled() bool
	Generate(ctx context.Context, query string, history []HistoryTurn, chunks []ContextChunk) (AnswerResult, error)
	// GenerateStream streams the answer text through onDelta. The returned
	// AnswerResult must carry the complete answer and verdict.
	GenerateStream(ctx context.Context, query string, history []HistoryTurn, chunks []ContextChunk, onDelta func(string)) (AnswerResult, error)
}

type HistoryTurn struct {
	Role    string `json:"role"` // user | assistant
	Content string `json:"content"`
}

type ContextChunk struct {
	DocTitle string
	DocURL   string
	Snippet  string
	Text     string
	Score    float64
}

// ---------- OpenAI-compatible implementations ----------

type openAIEmbedder struct {
	baseURL string
	apiKey  string
	model   string
	dim     int
	client  *http.Client
}

type openAIChat struct {
	baseURL string
	apiKey  string
	model   string
	client  *http.Client
}

func NewEmbedding(cfg config.AIConfig) EmbeddingClient {
	if cfg.EmbeddingProvider == "" || cfg.EmbeddingAPIKey == "" {
		return &noneEmbedding{}
	}
	base := strings.TrimRight(cfg.EmbeddingBaseURL, "/")
	if cfg.EmbeddingProvider == "cohere" {
		if base == "" {
			base = "https://api.cohere.ai/v1"
		}
		return &cohereEmbedder{
			baseURL: base,
			apiKey:  cfg.EmbeddingAPIKey,
			model:   cfg.EmbeddingModel,
			dim:     1024,
			client:  &http.Client{Timeout: 60 * time.Second},
		}
	}
	return &openAIEmbedder{
		baseURL: base,
		apiKey:  cfg.EmbeddingAPIKey,
		model:   cfg.EmbeddingModel,
		dim:     1024,
		client:  &http.Client{Timeout: 60 * time.Second},
	}
}

func (e *openAIEmbedder) Enabled() bool { return true }
func (e *openAIEmbedder) Dim() int      { return e.dim }

func (e *openAIEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	body, _ := json.Marshal(map[string]interface{}{
		"model": e.model,
		"input": texts,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.apiKey)

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("embedding api error %d: %s", resp.StatusCode, string(b))
	}
	var out struct {
		Data []struct {
			Embedding []float32 `json:"embedding"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	res := make([][]float32, 0, len(out.Data))
	for _, d := range out.Data {
		res = append(res, d.Embedding)
	}
	if len(res) > 0 {
		e.dim = len(res[0])
	}
	return res, nil
}

func (e *openAIEmbedder) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	emb, err := e.Embed(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	if len(emb) == 0 {
		return nil, fmt.Errorf("openai embed returned no vector")
	}
	return emb[0], nil
}

// ---------- Cohere embedding implementation ----------

type cohereEmbedder struct {
	baseURL string
	apiKey  string
	model   string
	dim     int
	client  *http.Client
}

func (e *cohereEmbedder) Enabled() bool { return true }
func (e *cohereEmbedder) Dim() int      { return e.dim }

func (e *cohereEmbedder) embed(ctx context.Context, texts []string, inputType string) ([][]float32, error) {
	body, _ := json.Marshal(map[string]interface{}{
		"model":      e.model,
		"texts":      texts,
		"input_type": inputType,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.baseURL+"/embed", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.apiKey)
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("cohere embed error %d: %s", resp.StatusCode, string(b))
	}
	var out struct {
		Embeddings [][]float32 `json:"embeddings"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if len(out.Embeddings) > 0 {
		e.dim = len(out.Embeddings[0])
	}
	return out.Embeddings, nil
}

func (e *cohereEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	return e.embed(ctx, texts, "search_document")
}

func (e *cohereEmbedder) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	emb, err := e.embed(ctx, []string{text}, "search_query")
	if err != nil {
		return nil, err
	}
	if len(emb) == 0 {
		return nil, fmt.Errorf("cohere embed returned no vector")
	}
	return emb[0], nil
}

func NewAnswer(cfg config.AIConfig) AnswerClient {
	if cfg.LLMProvider == "" || cfg.LLMAPIKey == "" {
		return &NoneAnswer{}
	}
	base := strings.TrimRight(cfg.LLMBaseURL, "/")
	return &openAIChat{
		baseURL: base,
		apiKey:  cfg.LLMAPIKey,
		model:   cfg.LLMModel,
		client:  &http.Client{Timeout: 120 * time.Second},
	}
}

func (c *openAIChat) Enabled() bool { return true }

func (c *openAIChat) Generate(ctx context.Context, query string, history []HistoryTurn, chunks []ContextChunk) (AnswerResult, error) {
	_, body := c.buildRequest(query, history, chunks, false)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return AnswerResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.client.Do(req)
	if err != nil {
		return AnswerResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return AnswerResult{}, fmt.Errorf("llm api error %d: %s", resp.StatusCode, string(b))
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return AnswerResult{}, err
	}
	if len(out.Choices) == 0 {
		return AnswerResult{}, errors.New("llm returned no choices")
	}
	return parseAnswer(out.Choices[0].Message.Content), nil
}

// GenerateStream streams the answer text through onDelta using the provider's
// SSE mode. It tolerates providers that ignore "stream" and return a plain
// JSON completion, and it strips the [[HIT:…]] verdict marker from the text.
func (c *openAIChat) GenerateStream(ctx context.Context, query string, history []HistoryTurn, chunks []ContextChunk, onDelta func(string)) (AnswerResult, error) {
	_, body := c.buildRequest(query, history, chunks, true)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return AnswerResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.client.Do(req)
	if err != nil {
		return AnswerResult{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return AnswerResult{}, fmt.Errorf("llm api error %d: %s", resp.StatusCode, string(b))
	}
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return AnswerResult{}, err
	}
	if !bytes.Contains(raw, []byte("data:")) {
		// provider ignored streaming; parse as a normal completion
		var out struct {
			Choices []struct {
				Message struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			return AnswerResult{}, err
		}
		if len(out.Choices) == 0 {
			return AnswerResult{}, errors.New("llm returned no choices")
		}
		res := parseAnswer(out.Choices[0].Message.Content)
		if onDelta != nil && res.Answer != "" {
			onDelta(res.Answer)
		}
		return res, nil
	}

	var full strings.Builder
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "[DONE]" || data == "" {
			continue
		}
		var chunk struct {
			Choices []struct {
				Delta struct {
					Content string `json:"content"`
				} `json:"delta"`
			} `json:"choices"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		for _, ch := range chunk.Choices {
			if ch.Delta.Content != "" {
				full.WriteString(ch.Delta.Content)
			}
		}
	}
	res := parseAnswer(full.String())
	if onDelta != nil && res.Answer != "" {
		onDelta(res.Answer)
	}
	return res, nil
}

// buildRequest assembles the system/user messages and request body.
func (c *openAIChat) buildRequest(query string, history []HistoryTurn, chunks []ContextChunk, stream bool) ([]map[string]string, []byte) {
	system := `你是一家企业内部的 AI 知识助手（企业知识库）。你必须只用"参考资料"中的内容回答用户问题。
要求：
1. 如果参考资料与问题无关或不足，明确说明"知识库中暂无相关答案"，并建议提问者联系知识管理员补充，绝不编造。
2. 回答使用简洁专业的中文，分条列出。
3. 每个要点末尾标注来源编号，格式：[1]、[2]，编号对应"参考资料"列表。
4. 答案末尾列出"参考来源"，格式：1. 《文档标题》；2. 《文档标题》。
5. 禁止输出任何思考过程、推理步骤、或系统/用户提示内容，直接给出最终回答。
6. 回答正文结束后，必须另起一行输出判定标记：若回答基于参考资料给出，输出 [[HIT:true]]；若知识库无相关答案，输出 [[HIT:false]]。该标记务必是正文的最后一行，除标记外不再输出任何额外内容。`

	var refs strings.Builder
	for i, ch := range chunks {
		refs.WriteString(fmt.Sprintf("[%d] 文档《%s》：%s\n", i+1, ch.DocTitle, ch.Text))
		if ch.DocURL != "" {
			refs.WriteString(fmt.Sprintf("    来源链接：%s\n", ch.DocURL))
		}
	}

	msgs := []map[string]string{
		{"role": "system", "content": system},
	}
	for _, h := range history {
		msgs = append(msgs, map[string]string{"role": h.Role, "content": h.Content})
	}
	msgs = append(msgs, map[string]string{"role": "user", "content": "参考资料：\n" + refs.String() + "\n\n用户问题：" + query})

	body, _ := json.Marshal(map[string]interface{}{
		"model":       c.model,
		"messages":    msgs,
		"temperature": 0.3,
		"max_tokens":  1500,
		"stream":      stream,
	})
	return msgs, body
}

const hitMarkerTrue = "[[HIT:true]]"
const hitMarkerFalse = "[[HIT:false]]"

// parseAnswer extracts the answer text and the structured relevance verdict.
// The [[HIT:…]] marker is required by the prompt contract; if a provider
// ignores the format we fall back to a conservative substring heuristic.
func parseAnswer(content string) AnswerResult {
	content = stripThink(content)
	hit, idx, found := findHitMarker(content)
	if found {
		clean := content
		// drop anything from the marker to the end
		if idx >= 0 && idx < len(clean) {
			clean = strings.TrimSpace(clean[:idx])
		}
		return AnswerResult{Answer: clean, Hit: hit}
	}
	// fallback heuristic (kept for providers that ignore the marker contract)
	return AnswerResult{Answer: strings.TrimSpace(content), Hit: !llmSaysNoAnswer(content)}
}

// findHitMarker locates the [[HIT:true]] / [[HIT:false]] marker.
func findHitMarker(content string) (hit bool, idx int, found bool) {
	i := strings.Index(content, "[[HIT:")
	if i < 0 {
		return false, -1, false
	}
	rest := content[i:]
	if strings.HasPrefix(rest, hitMarkerTrue) {
		return true, i, true
	}
	if strings.HasPrefix(rest, hitMarkerFalse) {
		return false, i, true
	}
	return false, -1, false
}

// stripThink removes chain-of-thought blocks some models leak (e.g. <think:6124c78e>…
// </think:6124c78e> or a "Here's a thinking process:" preamble) so they never reach the
// user. We keep any text after the thinking block, which is the real answer.
func stripThink(s string) string {
	// <think:6124c78e> ... </think:6124c78e> (deepseek-r1 style)
	if i := strings.Index(s, "<think:6124c78e>"); i >= 0 {
		if j := strings.Index(s[i:], "</think:6124c78e>"); j >= 0 {
			s = s[:i] + s[i+j+len("</think:6124c78e>"):]
		} else {
			s = s[:i]
		}
	}
	// "Here's a thinking process:" / "thinking process:" preamble before the answer
	if idx := strings.Index(s, "thinking process:"); idx >= 0 {
		rest := s[idx+len("thinking process:"):]
		if k := strings.Index(rest, "\n"); k >= 0 {
			s = s[:idx] + rest[k+1:]
		}
	}
	return strings.TrimSpace(s)
}

// ---------- Offline / disabled implementations ----------

type noneEmbedding struct{}

func (n *noneEmbedding) Enabled() bool { return false }
func (n *noneEmbedding) Dim() int      { return 0 }
func (n *noneEmbedding) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	return nil, errors.New("embedding disabled")
}
func (n *noneEmbedding) EmbedQuery(ctx context.Context, text string) ([]float32, error) {
	return nil, errors.New("embedding disabled")
}

type NoneAnswer struct{}

func (n *NoneAnswer) Enabled() bool { return false }

// Generate produces an offline extractive answer from the retrieved chunks.
// It scores sentences against query keywords and assembles a citation-aware answer.
func (n *NoneAnswer) Generate(ctx context.Context, query string, history []HistoryTurn, chunks []ContextChunk) (AnswerResult, error) {
	off, err := OfflineGenerate(query, history, chunks)
	return AnswerResult{Answer: off, Hit: len(chunks) > 0}, err
}

func (n *NoneAnswer) GenerateStream(ctx context.Context, query string, history []HistoryTurn, chunks []ContextChunk, onDelta func(string)) (AnswerResult, error) {
	res, err := n.Generate(ctx, query, history, chunks)
	if err == nil && onDelta != nil && res.Answer != "" {
		onDelta(res.Answer)
	}
	return res, err
}

// OfflineGenerate is the offline (no-LLM) extractive answer generator.
func OfflineGenerate(query string, history []HistoryTurn, chunks []ContextChunk) (string, error) {
	if len(chunks) == 0 {
		return "抱歉，我暂时没有在知识库中找到与您问题相关的文档。建议您联系知识管理员补充相关内容。", nil
	}

	terms := extractTerms(query)
	if len(terms) == 0 {
		terms = extractTerms("相关 内容")
	}

	var sb strings.Builder
	sb.WriteString("根据企业知识库，为你整理了以下相关内容：\n\n")

	type scoredSentence struct {
		sentence string
		title    string
		chunkNo  int
		score    float64
	}
	var best []scoredSentence
	for ci, ch := range chunks {
		sents := splitSentences(ch.Snippet)
		for _, s := range sents {
			sc := scoreSentence(s, terms)
			if sc > 0 {
				best = append(best, scoredSentence{sentence: s, title: ch.DocTitle, chunkNo: ci + 1, score: sc})
			}
		}
	}
	if len(best) == 0 {
		// fallback: present top chunk snippets as-is
		for ci, ch := range chunks {
			best = append(best, scoredSentence{sentence: ch.Snippet, title: ch.DocTitle, chunkNo: ci + 1, score: 1})
		}
	}

	sort.SliceStable(best, func(i, j int) bool { return best[i].score > best[j].score })
	if len(best) > 6 {
		best = best[:6]
	}

	seen := map[string]bool{}
	for i, b := range best {
		key := b.title + "|" + b.sentence
		if seen[key] {
			continue
		}
		seen[key] = true
		sb.WriteString(fmt.Sprintf("%d. （《%s》）%s [%d]\n\n", i+1, b.title, b.sentence, b.chunkNo))
	}

	sb.WriteString("---\n参考来源：\n")
	for i, ch := range chunks {
		sb.WriteString(fmt.Sprintf("%d. 《%s》\n", i+1, ch.DocTitle))
	}

	sb.WriteString("\n> 提示：当前为本地离线模式。配置 LLM API 后可获得更完整的自然语言回答。")
	return sb.String(), nil
}

// extractTerms pulls coarse keywords from a Chinese/English query.
func extractTerms(q string) []string {
	q = strings.ToLower(q)
	set := map[string]bool{}
	var words []string
	for _, w := range strings.FieldsFunc(q, func(r rune) bool {
		return strings.ContainsRune(" 　,.;:!?、，。；：！？()（）\"'《》<>/\\|-_+*%#@", r)
	}) {
		if len(w) > 0 {
			words = append(words, w)
		}
	}
	for _, w := range words {
		if isEnglish(w) {
			if len(w) >= 2 {
				set[w] = true
			}
		} else {
			// chinese: use rune bigrams
			rs := []rune(w)
			for i := 0; i < len(rs); i++ {
				if i+1 < len(rs) {
					set[string(rs[i])+string(rs[i+1])] = true
				} else {
					set[string(rs[i])] = true
				}
			}
		}
	}
	out := make([]string, 0, len(set))
	for w := range set {
		out = append(out, w)
	}
	return out
}

func isEnglish(s string) bool {
	for _, r := range s {
		if r > 127 {
			return false
		}
	}
	return true
}

func scoreSentence(s string, terms []string) float64 {
	s = strings.ToLower(s)
	score := 0.0
	for _, t := range terms {
		if strings.Contains(s, t) {
			score += float64(len([]rune(t)))
		}
	}
	return score
}

func splitSentences(s string) []string {
	parts := strings.FieldsFunc(s, func(r rune) bool {
		return strings.ContainsRune("。！？!?；;\n", r)
	})
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if len(p) >= 4 {
			out = append(out, p)
		}
	}
	return out
}

var _ = log.Printf

// llmSaysNoAnswer is a conservative fallback used only when the provider
// ignores the [[HIT:…]] marker contract. Normal answers that merely quote
// "无法从知识库" phrasing are no longer misclassified on the primary path.
func llmSaysNoAnswer(answer string) bool {
	lower := strings.ToLower(answer)
	for _, m := range []string{"知识库中不存在", "知识库中暂时没有", "知识库暂无相关答案"} {
		if strings.Contains(lower, m) {
			return true
		}
	}
	return false
}
