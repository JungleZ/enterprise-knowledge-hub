package handlers

import (
	"errors"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/enterprise-kb/backend/internal/database"
	"github.com/enterprise-kb/backend/internal/middleware"
	"github.com/enterprise-kb/backend/internal/models"
	"github.com/enterprise-kb/backend/internal/services"
)

type AuthHandler struct {
	svc *services.AuthService
}

func NewAuthHandler(svc *services.AuthService) *AuthHandler {
	return &AuthHandler{svc: svc}
}

func (h *AuthHandler) Register(c *fiber.Ctx) error {
	var in services.RegisterInput
	if err := c.BodyParser(&in); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "invalid request body"})
	}
	res, err := h.svc.Register(in)
	if err != nil {
		if errors.Is(err, services.ErrEmailTaken) {
			return c.Status(409).JSON(fiber.Map{"error": err.Error()})
		}
		if errors.Is(err, services.ErrRegisterDisabled) {
			return c.Status(403).JSON(fiber.Map{"error": err.Error()})
		}
		return c.Status(400).JSON(fiber.Map{"error": err.Error()})
	}
	return c.Status(201).JSON(res)
}

func (h *AuthHandler) Login(c *fiber.Ctx) error {
	var in services.LoginInput
	if err := c.BodyParser(&in); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "invalid request body"})
	}
	res, err := h.svc.Login(in)
	if err != nil {
		if errors.Is(err, services.ErrAccountLocked) {
			return c.Status(429).JSON(fiber.Map{"error": err.Error()})
		}
		return c.Status(401).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(res)
}

func (h *AuthHandler) Me(c *fiber.Ctx) error {
	user := middleware.CurrentUser(c)
	var full models.User
	if err := database.DB.First(&full, "id = ?", user.ID).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{"error": "user not found"})
	}
	var tenant models.Tenant
	if err := database.DB.First(&tenant, "id = ?", user.TenantID).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{"error": "tenant not found"})
	}
	return c.JSON(fiber.Map{"user": full, "tenant": tenant})
}

// ---- members ----

func (h *AuthHandler) ListMembers(c *fiber.Ctx) error {
	user := middleware.CurrentUser(c)
	members, err := services.ListMembers(user.TenantID)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(members)
}

func (h *AuthHandler) CreateMember(c *fiber.Ctx) error {
	user := middleware.CurrentUser(c)
	var in services.CreateMemberInput
	if err := c.BodyParser(&in); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "invalid request body"})
	}
	if err := h.svc.CreateMember(user.TenantID, in); err != nil {
		if errors.Is(err, services.ErrEmailTaken) {
			return c.Status(409).JSON(fiber.Map{"error": err.Error()})
		}
		return c.Status(400).JSON(fiber.Map{"error": err.Error()})
	}
	// audit
	auditLog(user.TenantID, user.ID, user.Name, "member_add", "新增成员 "+in.Email)
	return c.Status(201).JSON(fiber.Map{"ok": true})
}

func (h *AuthHandler) UpdateMember(c *fiber.Ctx) error {
	user := middleware.CurrentUser(c)
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "invalid id"})
	}
	var in services.UpdateMemberInput
	if err := c.BodyParser(&in); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "invalid request body"})
	}
	if err := h.svc.UpdateMember(user.TenantID, id, in); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": err.Error()})
	}
	auditLog(user.TenantID, user.ID, user.Name, "member_update", "更新成员 "+id.String())
	return c.JSON(fiber.Map{"ok": true})
}

func (h *AuthHandler) DeleteMember(c *fiber.Ctx) error {
	user := middleware.CurrentUser(c)
	id, err := uuid.Parse(c.Params("id"))
	if err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "invalid id"})
	}
	if err := h.svc.DeleteMember(user.TenantID, id); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": err.Error()})
	}
	auditLog(user.TenantID, user.ID, user.Name, "member_delete", "删除成员 "+id.String())
	return c.JSON(fiber.Map{"ok": true})
}
