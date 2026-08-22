package services

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/csv"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/enterprise-kb/backend/internal/database"
	"github.com/enterprise-kb/backend/internal/llm"
	"github.com/enterprise-kb/backend/internal/models"
	"github.com/enterprise-kb/backend/internal/search"
)

var (
	ErrUnsupportedFormat = errors.New("unsupported document format")
	ErrTooLarge          = errors.New("file too large")
)

const maxFileSize = 20 * 1024 * 1024 // 20MB

type IngestService struct {
	docsPath      string
	search        *search.Service
	embedder      llm.EmbeddingClient
	chunkSize     int
	chunkOverlap  int
	embedBatch    int
	embedSem      chan struct{}
}

func NewIngestService(docsPath string, searchSvc *search.Service, embedder llm.EmbeddingClient) *IngestService {
	return &IngestService{
		docsPath:     docsPath,
		search:       searchSvc,
		embedder:     embedder,
		chunkSize:    500,
		chunkOverlap: 60,
		embedBatch:   32,
		embedSem:     make(chan struct{}, 4),
	}
}

// Configure applies embedding batching / concurrency limits.
func (s *IngestService) Configure(batchSize, maxConcurrent int) {
	if batchSize > 0 {
		s.embedBatch = batchSize
	}
	if maxConcurrent > 0 {
		s.embedSem = make(chan struct{}, maxConcurrent)
	}
}

// SaveFile stores an uploaded file to disk and returns the full path.
func (s *IngestService) SaveFile(kbID uuid.UUID, filename string, data []byte) (string, error) {
	if len(data) > maxFileSize {
		return "", ErrTooLarge
	}
	if err := os.MkdirAll(s.docsPath, 0755); err != nil {
		return "", err
	}
	dir := filepath.Join(s.docsPath, kbID.String())
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, fmt.Sprintf("%s-%s", uuid.NewString()[:8], sanitizeFilename(filename)))
	if err := os.WriteFile(path, data, 0644); err != nil {
		return "", err
	}
	return path, nil
}

func sanitizeFilename(name string) string {
	name = filepath.Base(name)
	name = strings.ReplaceAll(name, " ", "_")
	if utf8.RuneCountInString(name) > 120 {
		rs := []rune(name)
		name = string(rs[:120])
	}
	return name
}

// Parse extracts plain text from a file based on its extension.
func (s *IngestService) Parse(path, contentType string) (string, error) {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".txt", ".md", ".markdown", ".csv":
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		text := string(data)
		if ext == ".csv" {
			return csvToText(bytes.NewReader(data)), nil
		}
		return text, nil
	case ".docx":
		return parseDocx(path)
	case ".pdf":
		return parsePDF(path)
	default:
		return "", ErrUnsupportedFormat
	}
}

func csvToText(r io.Reader) string {
	reader := csv.NewReader(r)
	reader.FieldsPerRecord = -1
	records, err := reader.ReadAll()
	if err != nil {
		return ""
	}
	var sb strings.Builder
	for i, rec := range records {
		sb.WriteString(fmt.Sprintf("第%d行：", i+1))
		for _, cell := range rec {
			sb.WriteString(cell)
			sb.WriteString(" | ")
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// ---------- DOCX ----------

type docxDocument struct {
	XMLName xml.Name `xml:"document"`
	Body    struct {
		Paragraphs []docxParagraph `xml:"p"`
	} `xml:"body"`
}

type docxParagraph struct {
	Texts []docxText `xml:"r"`
}

type docxText struct {
	Text string `xml:"t"`
}

func parseDocx(path string) (string, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return "", err
	}
	defer zr.Close()
	var file *zip.File
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			file = f
			break
		}
	}
	if file == nil {
		return "", errors.New("invalid docx: no document.xml")
	}
	rc, err := file.Open()
	if err != nil {
		return "", err
	}
	defer rc.Close()
	data, err := io.ReadAll(rc)
	if err != nil {
		return "", err
	}
	var doc docxDocument
	if err := xml.Unmarshal(data, &doc); err != nil {
		return "", err
	}
	var sb strings.Builder
	for _, p := range doc.Body.Paragraphs {
		for _, t := range p.Texts {
			sb.WriteString(t.Text)
		}
		sb.WriteString("\n")
	}
	return sb.String(), nil
}

// ---------- Chunking ----------

// chunkSeg is a chunk together with the 1-based page it starts on (0 = unknown).
type chunkSeg struct {
	Text string
	Page int
}

// ChunkText splits text into overlapping chunks at paragraph boundaries while
// tracking the source page (via pageBreak markers inserted by paginated
// parsers such as parsePDF). Non-paginated sources yield Page = 0.
func (s *IngestService) ChunkText(text string, size, overlap int) []chunkSeg {
	if size <= 0 {
		size = s.chunkSize
	}
	if overlap < 0 {
		overlap = s.chunkOverlap
	}
	if overlap >= size {
		overlap = size / 5
	}

	hasPages := strings.Contains(text, pageBreak)
	type para struct {
		text string
		page int
	}
	var paras []para
	ffCount := 0
	for _, p := range strings.Split(text, "\n") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		page := 0
		if hasPages {
			page = ffCount + 1
		}
		paras = append(paras, para{p, page})
		ffCount += strings.Count(p, pageBreak)
	}

	var blocks []chunkSeg
	var cur chunkSeg
	for _, pr := range paras {
		for si, sub := range strings.Split(pr.text, pageBreak) {
			sub = strings.TrimSpace(sub)
			if sub == "" {
				continue
			}
			sp := pr.page
			if hasPages && si > 0 {
				sp = pr.page + si
			}
			if cur.Text != "" {
				cur.Text += "\n"
			}
			cur.Text += sub
			cur.Page = sp
			if utf8.RuneCountInString(cur.Text) >= size {
				blocks = append(blocks, cur)
				cur = chunkSeg{}
			}
		}
	}
	if cur.Text != "" {
		blocks = append(blocks, cur)
	}

	if len(blocks) == 1 {
		rb := []rune(blocks[0].Text)
		if len(rb) > size*2 {
			return splitLongSeg(rb, size, overlap, blocks[0].Page)
		}
		return blocks
	}

	// merge small blocks, then re-split oversized blocks with overlap
	var merged []chunkSeg
	for _, b := range blocks {
		rb := []rune(b.Text)
		if len(rb) > size*2 {
			merged = append(merged, splitLongSeg(rb, size, overlap, b.Page)...)
			continue
		}
		if len(merged) > 0 {
			last := merged[len(merged)-1]
			if utf8.RuneCountInString(last.Text)+utf8.RuneCountInString(b.Text) <= size+overlap {
				merged[len(merged)-1] = chunkSeg{Text: last.Text + "\n" + b.Text, Page: last.Page}
				continue
			}
		}
		merged = append(merged, b)
	}
	return merged
}

func splitLongSeg(rs []rune, size, overlap, page int) []chunkSeg {
	var out []chunkSeg
	start := 0
	for start < len(rs) {
		end := start + size
		if end > len(rs) {
			end = len(rs)
		}
		out = append(out, chunkSeg{Text: string(rs[start:end]), Page: page})
		if end == len(rs) {
			break
		}
		start = end - overlap
	}
	return out
}

// ---------- Indexing pipeline ----------

// ProcessDocument parses, chunks, embeds and indexes a stored document.
func (s *IngestService) ProcessDocument(doc *models.Document) error {
	text, err := s.Parse(doc.Filename, doc.ContentType)
	if err != nil {
		s.markFailed(doc, err)
		return err
	}
	if strings.TrimSpace(text) == "" {
		s.markFailed(doc, errors.New("no extractable text"))
		return errors.New("no extractable text")
	}

	segs := s.ChunkText(text, s.chunkSize, s.chunkOverlap)
	visibility := visibilityFromTags(doc.AccessTags)

	// optional embeddings (batched to respect provider request size limits,
	// throttled by a semaphore so concurrent uploads don't hammer the API)
	vectors := make(map[string][]float32)
	if s.embedder.Enabled() {
		s.embedSem <- struct{}{}
		defer func() { <-s.embedSem }()
		for i := 0; i < len(segs); i += s.embedBatch {
			end := i + s.embedBatch
			if end > len(segs) {
				end = len(segs)
			}
			texts := make([]string, 0, end-i)
			for _, seg := range segs[i:end] {
				texts = append(texts, seg.Text)
			}
			embeds, err := s.embedder.Embed(context.Background(), texts)
			if err != nil {
				// don't fail the whole doc on embedding error; degrade to BM25
				logWarn("embedding batch %d/%d failed, falling back to BM25-only for the rest: %v", i/s.embedBatch+1, (len(segs)+s.embedBatch-1)/s.embedBatch, err)
				break
			}
			for j, e := range embeds {
				if i+j < len(segs) {
					vectors[fmt.Sprintf("%d", i+j)] = e
				}
			}
		}
	}

	title := doc.Title
	if title == "" {
		title = doc.Filename
	}

	chunkModels := make([]models.Chunk, 0, len(segs))
	for i, seg := range segs {
		cm := models.Chunk{
			TenantID:   doc.TenantID,
			KBID:       doc.KBID,
			DocID:      doc.ID,
			ChunkIndex: i,
			Page:       seg.Page,
			Text:       seg.Text,
			Title:      title,
			Visibility: visibility,
		}
		if v, ok := vectors[fmt.Sprintf("%d", i)]; ok && s.embedder.Enabled() {
			cm.Embedding = v
		}
		chunkModels = append(chunkModels, cm)
	}

	// delete old chunks for doc first (idempotency)
	database.DB.Where("doc_id = ?", doc.ID).Delete(&models.Chunk{})
	if err := database.DB.Create(&chunkModels).Error; err != nil {
		s.markFailed(doc, err)
		return err
	}

	if err := s.search.DeleteByDocID(doc.ID); err != nil {
		logWarn("search delete by doc failed: %v", err)
	}
	if err := s.search.IndexChunks(chunkModels); err != nil {
		s.markFailed(doc, err)
		return err
	}

	database.DB.Model(doc).Updates(map[string]interface{}{
		"status":      models.DocStatusReady,
		"error":       "",
		"chunk_count": len(chunkModels),
	})
	return nil
}

func (s *IngestService) markFailed(doc *models.Document, err error) {
	database.DB.Model(doc).Updates(map[string]interface{}{
		"status": models.DocStatusFailed,
		"error":  err.Error(),
	})
}

// ReindexTenant re-processes every document of a tenant so PostgreSQL and
// Meilisearch are reconciled (used when a previous processing pass died
// halfway and left the two stores inconsistent). Runs asynchronously.
// Returns the number of docs kicked off.
func (s *IngestService) ReindexTenant(tenantID uuid.UUID) (int, error) {
	var docs []models.Document
	if err := database.DB.Where("tenant_id = ? AND status != ?", tenantID, models.DocStatusProcessing).
		Find(&docs).Error; err != nil {
		return 0, err
	}
	for i := range docs {
		doc := docs[i]
		database.DB.Model(&doc).Updates(map[string]interface{}{
			"status": models.DocStatusProcessing,
			"error":  "",
		})
		go s.ProcessDocument(&doc)
	}
	return len(docs), nil
}

func (s *IngestService) DeleteDocument(doc *models.Document) error {
	database.DB.Where("doc_id = ?", doc.ID).Delete(&models.Chunk{})
	if err := s.search.DeleteByDocID(doc.ID); err != nil {
		logWarn("search delete failed: %v", err)
	}
	return os.Remove(doc.Filename)
}

// visibilityFromTags converts access tags to a chunk visibility list.
func visibilityFromTags(accessTags string) []string {
	tags := splitTags(accessTags)
	if len(tags) == 0 {
		return []string{"public"}
	}
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		t = strings.TrimSpace(t)
		if t == "" {
			continue
		}
		if t == "全部" || t == "全员" || t == "all" || t == "公开" {
			out = append(out, "public")
			continue
		}
		out = append(out, t)
	}
	if len(out) == 0 {
		out = append(out, "public")
	}
	return out
}

func splitTags(s string) []string {
	if strings.TrimSpace(s) == "" || s == "all" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(s, ",") {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// visibleTagsForUser computes the visibility set a user may see.
func visibleTagsForUser(dept string, isAdmin bool) []string {
	if isAdmin {
		// admins see everything: include a wildcard handled in handler query
		return nil
	}
	tags := []string{"public"}
	if dept != "" && dept != "全员" && dept != "全部" {
		tags = append(tags, dept)
	}
	sort.Strings(tags)
	return tags
}

// Ensure a helper for logging.
func logWarn(format string, args ...interface{}) {
	fmt.Printf("[warn] "+format+"\n", args...)
}
