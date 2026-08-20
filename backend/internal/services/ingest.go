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

// ChunkText splits text into overlapping chunks at paragraph boundaries.
func (s *IngestService) ChunkText(text string, size, overlap int) []string {
	if size <= 0 {
		size = s.chunkSize
	}
	if overlap < 0 {
		overlap = s.chunkOverlap
	}
	if overlap >= size {
		overlap = size / 5
	}

	paragraphs := strings.Split(text, "\n")
	var blocks []string
	var cur strings.Builder
	for _, p := range paragraphs {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if cur.Len() > 0 {
			cur.WriteString("\n")
		}
		cur.WriteString(p)
		if cur.Len() >= size {
			blocks = append(blocks, cur.String())
			cur.Reset()
		}
	}
	if cur.Len() > 0 {
		blocks = append(blocks, cur.String())
	}

	if len(blocks) == 1 {
		// single paragraph block: force-split if it exceeds 2x size
		// (a huge newline-free paragraph would otherwise never be chunked)
		rb := []rune(blocks[0])
		if len(rb) > size*2 {
			return splitLong(rb, size, overlap)
		}
		return blocks
	}

	// merge small blocks, then re-split oversized blocks with overlap
	var merged []string
	for _, b := range blocks {
		rb := []rune(b)
		if len(rb) > size*2 {
			merged = append(merged, splitLong(rb, size, overlap)...)
			continue
		}
		if len(merged) > 0 {
			last := merged[len(merged)-1]
			if utf8.RuneCountInString(last)+utf8.RuneCountInString(b) <= size+overlap {
				merged[len(merged)-1] = last + "\n" + b
				continue
			}
		}
		merged = append(merged, b)
	}
	return merged
}

func splitLong(rs []rune, size, overlap int) []string {
	var out []string
	start := 0
	for start < len(rs) {
		end := start + size
		if end > len(rs) {
			end = len(rs)
		}
		out = append(out, string(rs[start:end]))
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

	chunks := s.ChunkText(text, s.chunkSize, s.chunkOverlap)
	visibility := visibilityFromTags(doc.AccessTags)

	// optional embeddings (batched to respect provider request size limits,
	// throttled by a semaphore so concurrent uploads don't hammer the API)
	vectors := make(map[string][]float32)
	if s.embedder.Enabled() {
		s.embedSem <- struct{}{}
		defer func() { <-s.embedSem }()
		for i := 0; i < len(chunks); i += s.embedBatch {
			end := i + s.embedBatch
			if end > len(chunks) {
				end = len(chunks)
			}
			texts := chunks[i:end]
			embeds, err := s.embedder.Embed(context.Background(), texts)
			if err != nil {
				// don't fail the whole doc on embedding error; degrade to BM25
				logWarn("embedding batch %d/%d failed, falling back to BM25-only for the rest: %v", i/s.embedBatch+1, (len(chunks)+s.embedBatch-1)/s.embedBatch, err)
				break
			}
			for j, e := range embeds {
				if i+j < len(chunks) {
					vectors[fmt.Sprintf("%d", i+j)] = e
				}
			}
		}
	}

	title := doc.Title
	if title == "" {
		title = doc.Filename
	}

	chunkModels := make([]models.Chunk, 0, len(chunks))
	for i, c := range chunks {
		cm := models.Chunk{
			TenantID:   doc.TenantID,
			KBID:       doc.KBID,
			DocID:      doc.ID,
			ChunkIndex: i,
			Text:       c,
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
