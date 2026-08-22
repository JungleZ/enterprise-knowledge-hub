package handlers

import (
	"errors"
	"os"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/enterprise-kb/backend/internal/database"
	"github.com/enterprise-kb/backend/internal/middleware"
	"github.com/enterprise-kb/backend/internal/models"
	"github.com/enterprise-kb/backend/internal/search"
)

type KBHandler struct {
	search *search.Service
}

func NewKBHandler(searchSvc *search.Service) *KBHandler { return &KBHandler{search: searchSvc} }

type kbInput struct {
	Name               string `json:"name"`
	Description        string `json:"description"`
	AllowedDepartments string `json:"allowed_departments"`
}

func (h *KBHandler) List(c *fiber.Ctx) error {
	user := middleware.CurrentUser(c)
	var kbs []models.KnowledgeBase
	q := database.DB.Where("tenant_id = ?", user.TenantID)
	if user.Role == models.RoleMember {
		// members only see KBs allowed for their department or all
		if user.Department != "" && user.Department != "全员" && user.Department != "全部" {
			q = q.Where("allowed_departments = '' OR allowed_departments LIKE ? OR allowed_departments LIKE ?",
				"%全员%", "%"+user.Department+"%")
		}
	}
	if err := q.Order("created_at asc").Find(&kbs).Error; err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(kbs)
}

func (h *KBHandler) Create(c *fiber.Ctx) error {
	user := middleware.CurrentUser(c)
	if user.Role == models.RoleMember {
		return c.Status(403).JSON(fiber.Map{"error": "admin permission required"})
	}
	var in kbInput
	if err := c.BodyParser(&in); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "invalid request body"})
	}
	if in.Name == "" {
		return c.Status(400).JSON(fiber.Map{"error": "name required"})
	}
	kb := models.KnowledgeBase{
		TenantID:           user.TenantID,
		Name:               in.Name,
		Description:        in.Description,
		AllowedDepartments: in.AllowedDepartments,
		CreatorID:          user.ID,
	}
	if err := database.DB.Create(&kb).Error; err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	auditLog(user.TenantID, user.ID, user.Name, "kb_create", "创建知识库 "+kb.Name)
	return c.Status(201).JSON(kb)
}

func (h *KBHandler) Update(c *fiber.Ctx) error {
	user := middleware.CurrentUser(c)
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "invalid id"})
	}
	var in kbInput
	if err := c.BodyParser(&in); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "invalid request body"})
	}
	var kb models.KnowledgeBase
	if err := database.DB.First(&kb, "id = ? AND tenant_id = ?", id, user.TenantID).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{"error": "not found"})
	}
	updates := map[string]interface{}{}
	if in.Name != "" {
		updates["name"] = in.Name
	}
	updates["description"] = in.Description
	updates["allowed_departments"] = in.AllowedDepartments
	if err := database.DB.Model(&kb).Updates(updates).Error; err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	auditLog(user.TenantID, user.ID, user.Name, "kb_update", "更新知识库 "+kb.Name)
	return c.JSON(kb)
}

func (h *KBHandler) Delete(c *fiber.Ctx) error {
	user := middleware.CurrentUser(c)
	if user.Role == models.RoleMember {
		return c.Status(403).JSON(fiber.Map{"error": "admin permission required"})
	}
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "invalid id"})
	}
	var kb models.KnowledgeBase
	if err := database.DB.First(&kb, "id = ? AND tenant_id = ?", id, user.TenantID).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{"error": "not found"})
	}
	// Fetch docs first so we can remove their files from disk too.
	var docs []models.Document
	database.DB.Where("kb_id = ? AND tenant_id = ?", id, user.TenantID).Find(&docs)

	// Clean up the Meilisearch index (otherwise deleted-KB chunks still surface
	// in tenant-wide search) and remove the source files from disk.
	if h.search != nil {
		if err := h.search.DeleteByKBID(id); err != nil {
			logWarn("meili delete by kb %s failed: %v", id, err)
		}
	}
	for _, d := range docs {
		if err := os.Remove(d.Filename); err != nil && !osIsNotExist(err) {
			logWarn("remove doc file %s failed: %v", d.Filename, err)
		}
	}

	// cascade chunks + docs in PostgreSQL
	database.DB.Where("kb_id = ? AND tenant_id = ?", id, user.TenantID).Delete(&models.Chunk{})
	for _, d := range docs {
		database.DB.Delete(&d)
	}
	if err := database.DB.Delete(&kb).Error; err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	auditLog(user.TenantID, user.ID, user.Name, "kb_delete", "删除知识库 "+kb.Name)
	return c.JSON(fiber.Map{"ok": true})
}

func (h *KBHandler) Get(c *fiber.Ctx) error {
	user := middleware.CurrentUser(c)
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "invalid id"})
	}
	var kb models.KnowledgeBase
	if err := database.DB.First(&kb, "id = ? AND tenant_id = ?", id, user.TenantID).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{"error": "not found"})
	}
	return c.JSON(kb)
}

var _ = errors.New
