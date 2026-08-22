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
	"regexp"
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

// parseDocx extracts plain text from a .docx, preserving paragraphs and table
// rows. Tables (w:tbl) are flattened to "cell | cell | cell" lines so tabular
// policy content survives chunking instead of being dropped. Uses a streaming
// decoder so we can see table structure, not just top-level paragraphs.
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

	dec := xml.NewDecoder(rc)
	var sb strings.Builder
	var cur strings.Builder // text of the current paragraph or table cell
	var cells []string      // cell texts of the current table row
	inTable := false
	inCell := false
	flushPara := func() {
		if !inTable {
			if t := strings.TrimSpace(cur.String()); t != "" {
				sb.WriteString(t)
				sb.WriteString("\n")
			}
		}
		cur.Reset()
	}

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		switch el := tok.(type) {
		case xml.StartElement:
			switch el.Name.Local {
			case "tbl":
				inTable = true
			case "tc":
				inCell = true
				cur.Reset()
			}
		case xml.CharData:
			cur.Write(el)
		case xml.EndElement:
			switch el.Name.Local {
			case "p":
				if inCell {
					cur.WriteString(" ") // keep multi-paragraph cells readable
				} else {
					flushPara()
				}
			case "tc":
				inCell = false
				cells = append(cells, strings.TrimSpace(cur.String()))
				cur.Reset()
			case "tr":
				if len(cells) > 0 {
					sb.WriteString(strings.Join(cells, " | "))
					sb.WriteString("\n")
				}
				cells = cells[:0]
			case "tbl":
				inTable = false
			}
		}
	}
	return sb.String(), nil
}

// ---------- Chunking ----------

// chunkSeg is a chunk together with the 1-based page it starts on (0 = unknown)
// and the nearest section heading it falls under ("" when none detected).
type chunkSeg struct {
	Text    string
	Page    int
	Heading string
}

// ChunkText splits text into chunks at paragraph boundaries while tracking the
// source page (via pageBreak markers inserted by paginated parsers such as
// parsePDF) and the nearest section heading. Consecutive chunks share a true
// trailing overlap so a sentence cut at a chunk boundary is not lost.
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
		text    string
		page    int
		heading string
	}
	var paras []para
	ffCount := 0
	curHeading := ""
	for _, p := range strings.Split(text, "\n") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		page := 0
		if hasPages {
			page = ffCount + 1
		}
		if h := detectHeading(strings.ReplaceAll(p, pageBreak, "")); h != "" {
			curHeading = h
		}
		paras = append(paras, para{p, page, curHeading})
		ffCount += strings.Count(p, pageBreak)
	}
	if len(paras) == 0 {
		return nil
	}

	// Pack paragraphs into blocks at paragraph boundaries. Oversized single
	// blocks are force-split with their own sliding overlap (marked so we don't
	// double-apply the inter-block overlap below).
	var blocks []chunkSeg
	var fromSplit []bool
	var cur strings.Builder
	curPage, curHead := 0, ""
	flush := func() {
		t := strings.TrimSpace(cur.String())
		cur.Reset()
		if t == "" {
			return
		}
		if rb := []rune(t); len(rb) > size*2 {
			for _, w := range splitLongSeg(rb, size, overlap, curPage, curHead) {
				blocks = append(blocks, w)
				fromSplit = append(fromSplit, true)
			}
			return
		}
		blocks = append(blocks, chunkSeg{Text: t, Page: curPage, Heading: curHead})
		fromSplit = append(fromSplit, false)
	}

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
			if cur.Len() == 0 {
				curPage, curHead = sp, pr.heading
			}
			if cur.Len() > 0 && utf8.RuneCountInString(cur.String()+"\n"+sub) > size {
				flush()
				curPage, curHead = sp, pr.heading
				cur.WriteString(sub)
			} else {
				if cur.Len() > 0 {
					cur.WriteString("\n")
				}
				cur.WriteString(sub)
			}
		}
	}
	flush()

	// True inter-chunk overlap: prepend the trailing `overlap` runes of the
	// previous block to each block (skipped for force-split blocks, which
	// already carry their own overlap).
	if overlap > 0 {
		for i := 1; i < len(blocks); i++ {
			if fromSplit[i] || fromSplit[i-1] {
				continue
			}
			prev := []rune(blocks[i-1].Text)
			if len(prev) > overlap {
				blocks[i].Text = string(prev[len(prev)-overlap:]) + "\n" + blocks[i].Text
			}
		}
	}
	return blocks
}

func splitLongSeg(rs []rune, size, overlap, page int, heading string) []chunkSeg {
	var out []chunkSeg
	start := 0
	for start < len(rs) {
		end := start + size
		if end > len(rs) {
			end = len(rs)
		}
		out = append(out, chunkSeg{Text: string(rs[start:end]), Page: page, Heading: heading})
		if end == len(rs) {
			break
		}
		start = end - overlap
	}
	return out
}

var (
	reChapter = regexp.MustCompile(`^第[0-9一二三四五六七八九十百千万]+[章节条款部分编回]`)
	reCnEnum  = regexp.MustCompile(`^[一二三四五六七八九十]+、`)
	reCnParen = regexp.MustCompile(`^[（(][一二三四五六七八九十0-9]+[)）]`)
	reNumEnum = regexp.MustCompile(`^\d+(\.\d+)*[、.\s]`)
)

// detectHeading returns the heading text when a paragraph looks like a section
// heading (markdown "#" or common Chinese numbering), else "". Conservative —
// only short lines qualify, to avoid treating body text as a heading.
func detectHeading(s string) string {
	t := strings.TrimSpace(s)
	if t == "" {
		return ""
	}
	if strings.HasPrefix(t, "#") {
		return strings.TrimSpace(strings.TrimLeft(t, "#"))
	}
	if len([]rune(t)) > 40 {
		return ""
	}
	if reChapter.MatchString(t) || reCnEnum.MatchString(t) || reCnParen.MatchString(t) || reNumEnum.MatchString(t) {
		return t
	}
	return ""
}

// contextPrefix prefixes a chunk with its document title (and section heading
// when detected) so the embedding captures which document/section the chunk
// belongs to — a large retrieval-quality win for long, multi-section policy
// documents. BM25 already indexes the title field, so this targets vectors.
func contextPrefix(title, heading string) string {
	if heading != "" && heading != title {
		return title + " > " + heading + "\n"
	}
	return title + "\n"
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

	title := doc.Title
	if title == "" {
		title = doc.Filename
	}

	// optional embeddings (batched to respect provider request size limits,
	// throttled by a semaphore so concurrent uploads don't hammer the API).
	// Each chunk is embedded with a doc/section context prefix so vector recall
	// knows which document and section it came from.
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
				texts = append(texts, contextPrefix(title, seg.Heading)+seg.Text)
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
