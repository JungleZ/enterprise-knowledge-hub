package handlers

import (
	"errors"
	"io"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/enterprise-kb/backend/internal/database"
	"github.com/enterprise-kb/backend/internal/middleware"
	"github.com/enterprise-kb/backend/internal/models"
	"github.com/enterprise-kb/backend/internal/services"
)

type DocHandler struct {
	ingest *services.IngestService
}

func NewDocHandler(ingest *services.IngestService) *DocHandler {
	return &DocHandler{ingest: ingest}
}

func (h *DocHandler) Upload(c *fiber.Ctx) error {
	user := middleware.CurrentUser(c)
	kbID, err := uuid.Parse(c.Params("kbId"))
	if err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "invalid kb id"})
	}
	var kb models.KnowledgeBase
	if err := database.DB.First(&kb, "id = ? AND tenant_id = ?", kbID, user.TenantID).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{"error": "knowledge base not found"})
	}

	fileHeader, err := c.FormFile("file")
	if err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "file field required"})
	}
	f, err := fileHeader.Open()
	if err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "failed to open file"})
	}
	defer f.Close()
	// io.ReadAll reads until EOF; a single f.Read may return short reads.
	buf, err := io.ReadAll(f)
	if err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "failed to read file"})
	}

	path, err := h.ingest.SaveFile(kbID, fileHeader.Filename, buf)
	if err != nil {
		if errors.Is(err, services.ErrTooLarge) {
			return c.Status(413).JSON(fiber.Map{"error": err.Error()})
		}
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}

	title := c.FormValue("title")
	if title == "" {
		title = fileHeader.Filename
	}
	tags := c.FormValue("access_tags") // comma separated dept tags, empty = public

	doc := models.Document{
		TenantID:    user.TenantID,
		KBID:        kbID,
		Title:       title,
		Filename:    path,
		FileSize:    fileHeader.Size,
		ContentType: fileHeader.Header.Get("Content-Type"),
		Status:      models.DocStatusProcessing,
		AccessTags:  tags,
		CreatedBy:   user.ID,
	}
	if err := database.DB.Create(&doc).Error; err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}

	// process synchronously (MVP)
	go h.ingest.ProcessDocument(&doc)

	auditLog(user.TenantID, user.ID, user.Name, "doc_upload", "上传文档 "+doc.Title+" → "+kb.Name)
	return c.Status(201).JSON(doc)
}

func (h *DocHandler) List(c *fiber.Ctx) error {
	user := middleware.CurrentUser(c)
	kbID, err := uuid.Parse(c.Params("kbId"))
	if err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "invalid kb id"})
	}
	var docs []models.Document
	if err := database.DB.Where("tenant_id = ? AND kb_id = ?", user.TenantID, kbID).
		Order("created_at desc").Find(&docs).Error; err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(docs)
}

func (h *DocHandler) Delete(c *fiber.Ctx) error {
	user := middleware.CurrentUser(c)
	docID, err := uuid.Parse(c.Params("docId"))
	if err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "invalid doc id"})
	}
	var doc models.Document
	if err := database.DB.First(&doc, "id = ? AND tenant_id = ?", docID, user.TenantID).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{"error": "document not found"})
	}
	if err := h.ingest.DeleteDocument(&doc); err != nil && !osIsNotExist(err) {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	if err := database.DB.Delete(&doc).Error; err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	auditLog(user.TenantID, user.ID, user.Name, "doc_delete", "删除文档 "+doc.Title)
	return c.JSON(fiber.Map{"ok": true})
}

func (h *DocHandler) Reprocess(c *fiber.Ctx) error {
	user := middleware.CurrentUser(c)
	docID, err := uuid.Parse(c.Params("docId"))
	if err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "invalid doc id"})
	}
	var doc models.Document
	if err := database.DB.First(&doc, "id = ? AND tenant_id = ?", docID, user.TenantID).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{"error": "document not found"})
	}
	database.DB.Model(&doc).Update("status", models.DocStatusProcessing)
	go h.ingest.ProcessDocument(&doc)
	auditLog(user.TenantID, user.ID, user.Name, "doc_reprocess", "重新处理文档 "+doc.Title)
	return c.JSON(fiber.Map{"ok": true})
}

// Chunks returns a document's chunks (with positions) so the frontend can
// link citations back to the exact location inside the document.
func (h *DocHandler) Chunks(c *fiber.Ctx) error {
	user := middleware.CurrentUser(c)
	docID, err := uuid.Parse(c.Params("docId"))
	if err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "invalid doc id"})
	}
	var doc models.Document
	if err := database.DB.First(&doc, "id = ? AND tenant_id = ?", docID, user.TenantID).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{"error": "document not found"})
	}
	var chunks []models.Chunk
	if err := database.DB.Where("doc_id = ?", docID).Order("chunk_index asc").Find(&chunks).Error; err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}

	type chunkOut struct {
		ID         string `json:"id"`
		ChunkIndex int    `json:"chunk_index"`
		Text       string `json:"text"`
	}
	out := make([]chunkOut, 0, len(chunks))
	for _, ch := range chunks {
		out = append(out, chunkOut{ID: ch.ID.String(), ChunkIndex: ch.ChunkIndex, Text: ch.Text})
	}
	return c.JSON(fiber.Map{
		"doc_id":      doc.ID.String(),
		"doc_title":   doc.Title,
		"kb_id":       doc.KBID.String(),
		"chunk_count": len(chunks),
		"chunks":      out,
	})
}
