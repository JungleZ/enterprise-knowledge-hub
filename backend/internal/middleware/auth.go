package middleware

import (
	"strings"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/enterprise-kb/backend/internal/config"
	"github.com/enterprise-kb/backend/internal/database"
	"github.com/enterprise-kb/backend/internal/models"
)

type Claims struct {
	UserID   uuid.UUID `json:"user_id"`
	TenantID uuid.UUID `json:"tenant_id"`
	Email    string    `json:"email"`
	Role     string    `json:"role"`
	jwt.RegisteredClaims
}

func GenerateToken(cfg config.JWTConfig, user *models.User) (string, error) {
	claims := Claims{
		UserID:   user.ID,
		TenantID: user.TenantID,
		Email:    user.Email,
		Role:     user.Role,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(cfg.Expiration)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(cfg.Secret))
}

func AuthRequired(cfg config.JWTConfig) fiber.Handler {
	return func(c *fiber.Ctx) error {
		authHeader := c.Get("Authorization")
		if authHeader == "" {
			return c.Status(401).JSON(fiber.Map{"error": "missing authorization header"})
		}

		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || parts[0] != "Bearer" {
			return c.Status(401).JSON(fiber.Map{"error": "invalid authorization format"})
		}

		token, err := jwt.ParseWithClaims(parts[1], &Claims{}, func(t *jwt.Token) (interface{}, error) {
			return []byte(cfg.Secret), nil
		})
		if err != nil {
			return c.Status(401).JSON(fiber.Map{"error": "invalid or expired token"})
		}

		claims, ok := token.Claims.(*Claims)
		if !ok || !token.Valid {
			return c.Status(401).JSON(fiber.Map{"error": "invalid token claims"})
		}

		var user models.User
		if err := database.DB.First(&user, "id = ?", claims.UserID).Error; err != nil {
			return c.Status(401).JSON(fiber.Map{"error": "user not found"})
		}

		c.Locals("user_id", user.ID)
		c.Locals("tenant_id", user.TenantID)
		c.Locals("user_email", user.Email)
		c.Locals("user_role", user.Role)
		c.Locals("user_dept", user.Department)
		c.Locals("user_name", user.Name)
		return c.Next()
	}
}

// CurrentUser is a convenience helper to fetch the current user.
func CurrentUser(c *fiber.Ctx) *models.User {
	return &models.User{
		ID:         c.Locals("user_id").(uuid.UUID),
		TenantID:   c.Locals("tenant_id").(uuid.UUID),
		Email:      c.Locals("user_email").(string),
		Role:       c.Locals("user_role").(string),
		Department: c.Locals("user_dept").(string),
		Name:       c.Locals("user_name").(string),
	}
}

// RequireAdmin rejects non-admin (super_admin / knowledge_admin) users.
func RequireAdmin() fiber.Handler {
	return func(c *fiber.Ctx) error {
		role := c.Locals("user_role").(string)
		if role != models.RoleSuperAdmin && role != models.RoleKnowledgeAdmin {
			return c.Status(403).JSON(fiber.Map{"error": "admin permission required"})
		}
		return c.Next()
	}
}

// RequireSuperAdmin rejects non super_admin users.
func RequireSuperAdmin() fiber.Handler {
	return func(c *fiber.Ctx) error {
		role := c.Locals("user_role").(string)
		if role != models.RoleSuperAdmin {
			return c.Status(403).JSON(fiber.Map{"error": "super admin permission required"})
		}
		return c.Next()
	}
}
