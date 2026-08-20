package handlers

import (
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/enterprise-kb/backend/internal/database"
	"github.com/enterprise-kb/backend/internal/middleware"
	"github.com/enterprise-kb/backend/internal/models"
)

// BotHandler exposes bot binding management for the web admin console.
type BotHandler struct{}

func NewBotHandler() *BotHandler { return &BotHandler{} }

func (h *BotHandler) ListBindings(c *fiber.Ctx) error {
	user := middleware.CurrentUser(c)
	var bindings []models.BotBinding
	if err := database.DB.Where("tenant_id = ?", user.TenantID).Order("created_at desc").Find(&bindings).Error; err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}

	// enrich with user names
	type row struct {
		models.BotBinding
		UserName string `json:"user_name"`
	}
	out := make([]row, 0, len(bindings))
	for _, b := range bindings {
		name := b.Email
		var u models.User
		if err := database.DB.First(&u, "id = ?", b.UserID).Error; err == nil {
			name = u.Name
		}
		out = append(out, row{BotBinding: b, UserName: name})
	}
	return c.JSON(out)
}

type bindingDecisionRequest struct {
	Email  string `json:"email"`
	Status string `json:"status"` // approved | rejected
}

// Decide approves/rejects a pending binding from the web admin console.
func (h *BotHandler) Decide(c *fiber.Ctx) error {
	user := middleware.CurrentUser(c)
	if user.Role != models.RoleSuperAdmin && user.Role != models.RoleKnowledgeAdmin {
		return c.Status(403).JSON(fiber.Map{"error": "admin role required"})
	}
	var req bindingDecisionRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "invalid request body"})
	}
	req.Email = strings.ToLower(strings.TrimSpace(req.Email))
	if req.Email == "" {
		return c.Status(400).JSON(fiber.Map{"error": "email required"})
	}
	if req.Status != models.BindingApproved && req.Status != models.BindingRejected {
		return c.Status(400).JSON(fiber.Map{"error": "status must be approved or rejected"})
	}

	var binding models.BotBinding
	if err := database.DB.Where("lower(email) = ? AND status = ?", req.Email, models.BindingPending).
		Order("created_at asc").First(&binding).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{"error": "no pending binding for " + req.Email})
	}
	var target models.User
	if err := database.DB.First(&target, "id = ?", binding.UserID).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{"error": "bound system account not found"})
	}
	// tiered approval: only super_admin may approve a super_admin binding
	if target.Role == models.RoleSuperAdmin && user.Role != models.RoleSuperAdmin {
		return c.Status(403).JSON(fiber.Map{"error": "only super_admin can approve admin account bindings"})
	}
	if err := database.DB.Model(&binding).Updates(map[string]interface{}{
		"status": req.Status,
	}).Error; err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	auditLog(user.TenantID, user.ID, user.Name, "bot_binding", req.Status+": "+req.Email)
	return c.JSON(fiber.Map{"ok": true, "status": req.Status, "email": req.Email})
}

// Unbind removes an existing binding.
func (h *BotHandler) Unbind(c *fiber.Ctx) error {
	user := middleware.CurrentUser(c)
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "invalid id"})
	}
	if user.Role != models.RoleSuperAdmin && user.Role != models.RoleKnowledgeAdmin {
		return c.Status(403).JSON(fiber.Map{"error": "admin role required"})
	}
	res := database.DB.Where("id = ? AND tenant_id = ?", id, user.TenantID).Delete(&models.BotBinding{})
	if res.RowsAffected == 0 {
		return c.Status(404).JSON(fiber.Map{"error": "binding not found"})
	}
	return c.JSON(fiber.Map{"ok": true})
}
