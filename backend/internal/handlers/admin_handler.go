package handlers

import (
	"fmt"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/enterprise-kb/backend/internal/config"
	"github.com/enterprise-kb/backend/internal/database"
	"github.com/enterprise-kb/backend/internal/middleware"
	"github.com/enterprise-kb/backend/internal/models"
	"github.com/enterprise-kb/backend/internal/services"
)

type AdminHandler struct {
	svc    *services.AdminService
	ingest *services.IngestService
	cfg    *config.Config
}

func NewAdminHandler(svc *services.AdminService, cfg *config.Config) *AdminHandler {
	return &AdminHandler{svc: svc, cfg: cfg}
}

// SetIngest wires the ingest service for the reindex endpoint.
func (h *AdminHandler) SetIngest(ingest *services.IngestService) {
	h.ingest = ingest
}

// Reindex reconciles PostgreSQL and Meilisearch by re-processing every
// document in the tenant (admin-only maintenance endpoint).
func (h *AdminHandler) Reindex(c *fiber.Ctx) error {
	user := middleware.CurrentUser(c)
	if h.ingest == nil {
		return c.Status(500).JSON(fiber.Map{"error": "ingest service not available"})
	}
	n, err := h.ingest.ReindexTenant(user.TenantID)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	auditLog(user.TenantID, user.ID, user.Name, "reindex", fmt.Sprintf("重建索引 %d 篇文档", n))
	return c.JSON(fiber.Map{"started": n})
}

func (h *AdminHandler) Stats(c *fiber.Ctx) error {
	user := middleware.CurrentUser(c)
	stats, err := h.svc.Stats(user.TenantID)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(stats)
}

func (h *AdminHandler) Audit(c *fiber.Ctx) error {
	user := middleware.CurrentUser(c)
	limit := c.QueryInt("limit", 50)
	logs, err := h.svc.RecentAudit(user.TenantID, limit)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(logs)
}

func (h *AdminHandler) Gaps(c *fiber.Ctx) error {
	user := middleware.CurrentUser(c)
	gaps, err := h.svc.Gaps(user.TenantID, 20)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(gaps)
}

func (h *AdminHandler) Feedback(c *fiber.Ctx) error {
	user := middleware.CurrentUser(c)
	list, err := h.svc.FeedbackList(user.TenantID)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(list)
}

// Sessions lists all chat sessions in the tenant (admin detail view).
func (h *AdminHandler) Sessions(c *fiber.Ctx) error {
	user := middleware.CurrentUser(c)
	sessions, err := services.RecentSessionsWithUsers(user.TenantID, 200)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(sessions)
}

// SessionMessages lists messages of any session in the tenant (admin detail view).
func (h *AdminHandler) SessionMessages(c *fiber.Ctx) error {
	user := middleware.CurrentUser(c)
	sid, err := uuid.Parse(c.Params("sessionId"))
	if err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "invalid session id"})
	}
	var session models.ChatSession
	if err := database.DB.First(&session, "id = ? AND tenant_id = ?", sid, user.TenantID).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{"error": "session not found"})
	}
	msgs, err := services.SessionMessages(sid)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(msgs)
}

// AllTenantsAudit lets super admins view cross-tenant audit (only from admin console placeholder).
func (h *AdminHandler) Members(c *fiber.Ctx) error {
	user := middleware.CurrentUser(c)
	members, err := services.ListMembers(user.TenantID)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(members)
}

// Contact returns the tenant's contact admins (visible to any logged-in
// member so the chat page can show a "contact admin" link on missed answers).
func (h *AdminHandler) Contact(c *fiber.Ctx) error {
	user := middleware.CurrentUser(c)
	var admins []models.User
	if err := database.DB.Where("tenant_id = ? AND role IN ?", user.TenantID,
		[]string{models.RoleSuperAdmin, models.RoleKnowledgeAdmin}).
		Order("role asc, created_at asc").Find(&admins).Error; err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	out := make([]fiber.Map, 0, len(admins))
	for _, a := range admins {
		out = append(out, fiber.Map{
			"name":       a.Name,
			"email":      a.Email,
			"title":      a.Title,
			"department": a.Department,
		})
	}
	return c.JSON(fiber.Map{
		"admins":       out,
		"contact_link": h.cfg.Contact.Link,
		"contact_text": h.cfg.Contact.Text,
	})
}

var _ = models.RoleSuperAdmin
var _ = uuid.Nil
