package services

import (
	"errors"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"

	"github.com/enterprise-kb/backend/internal/config"
	"github.com/enterprise-kb/backend/internal/database"
	"github.com/enterprise-kb/backend/internal/middleware"
	"github.com/enterprise-kb/backend/internal/models"
	"github.com/google/uuid"
)

var (
	ErrEmailTaken       = errors.New("email already registered")
	ErrInvalidCreds     = errors.New("invalid email or password")
	ErrTenantNotFound   = errors.New("tenant not found")
	ErrAccountLocked    = errors.New("account temporarily locked due to too many failed attempts, try again later")
	ErrRegisterDisabled = errors.New("self-registration is disabled, contact an administrator")
	ErrWeakPassword     = errors.New("password must be at least 8 characters and contain both letters and numbers")
)

// loginGuard tracks repeated login failures per email (in-memory, best effort).
type loginGuard struct {
	mu        sync.Mutex
	fails     map[string]int
	lockUntil map[string]time.Time
	maxFails  int
	lockMins  int
}

func (g *loginGuard) locked(email string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	if until, ok := g.lockUntil[email]; ok {
		if time.Now().Before(until) {
			return true
		}
		delete(g.lockUntil, email)
		delete(g.fails, email)
	}
	return false
}

func (g *loginGuard) fail(email string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.fails[email]++
	if g.fails[email] >= g.maxFails {
		g.lockUntil[email] = time.Now().Add(time.Duration(g.lockMins) * time.Minute)
		delete(g.fails, email)
	}
}

func (g *loginGuard) ok(email string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	delete(g.fails, email)
	delete(g.lockUntil, email)
}

type AuthService struct {
	cfg             config.JWTConfig
	registerEnabled bool
	passwordLen     int
	guard           *loginGuard
}

func NewAuthService(cfg config.JWTConfig) *AuthService {
	return &AuthService{
		cfg:             cfg,
		registerEnabled: true,
		passwordLen:     8,
		guard: &loginGuard{
			fails:     map[string]int{},
			lockUntil: map[string]time.Time{},
			maxFails:  5,
			lockMins:  15,
		},
	}
}

// Configure applies auth hardening from config.
func (s *AuthService) Configure(registerEnabled bool, minPasswordLen, maxFailures, lockMinutes int) {
	s.registerEnabled = registerEnabled
	if minPasswordLen > 0 {
		s.passwordLen = minPasswordLen
	}
	if maxFailures > 0 {
		s.guard.maxFails = maxFailures
	}
	if lockMinutes > 0 {
		s.guard.lockMins = lockMinutes
	}
}

// validatePassword enforces password strength (length + letter & digit).
func (s *AuthService) validatePassword(pw string) error {
	if len(pw) < s.passwordLen {
		return ErrWeakPassword
	}
	hasLetter, hasDigit := false, false
	for _, r := range pw {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			hasLetter = true
		}
		if r >= '0' && r <= '9' {
			hasDigit = true
		}
	}
	if !hasLetter || !hasDigit {
		return ErrWeakPassword
	}
	return nil
}

type RegisterInput struct {
	TenantName string `json:"tenant_name"`
	Company    string `json:"company"`
	Email      string `json:"email"`
	Password   string `json:"password"`
	Name       string `json:"name"`
}

type LoginInput struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type AuthResult struct {
	Token  string        `json:"token"`
	User   models.User   `json:"user"`
	Tenant models.Tenant `json:"tenant"`
}

// Register creates a new tenant + super admin user.
func (s *AuthService) Register(in RegisterInput) (*AuthResult, error) {
	if !s.registerEnabled {
		return nil, ErrRegisterDisabled
	}
	email := strings.ToLower(strings.TrimSpace(in.Email))
	if email == "" || in.Password == "" {
		return nil, errors.New("email and password required")
	}
	if err := s.validatePassword(in.Password); err != nil {
		return nil, err
	}

	var existing models.User
	if err := database.DB.Where("lower(email) = ?", email).First(&existing).Error; err == nil {
		return nil, ErrEmailTaken
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}

	tenantName := strings.TrimSpace(in.TenantName)
	if tenantName == "" {
		tenantName = strings.TrimSpace(in.Company)
	}
	if tenantName == "" {
		tenantName = email
	}

	tenant := models.Tenant{Name: tenantName, Slug: slugify(tenantName)}
	if err := database.DB.Create(&tenant).Error; err != nil {
		return nil, err
	}

	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = email
	}
	user := models.User{
		TenantID:   tenant.ID,
		Email:      email,
		Name:       name,
		Password:   string(hash),
		Role:       models.RoleSuperAdmin,
		Department: "全员",
	}
	if err := database.DB.Create(&user).Error; err != nil {
		database.DB.Delete(&tenant)
		return nil, err
	}

	token, err := middleware.GenerateToken(s.cfg, &user)
	if err != nil {
		return nil, err
	}

	// create default knowledge base
	database.DB.Create(&models.KnowledgeBase{
		TenantID:    tenant.ID,
		Name:        "默认知识库",
		Description: "企业默认知识库",
		CreatorID:   user.ID,
	})

	return &AuthResult{Token: token, User: user, Tenant: tenant}, nil
}

func (s *AuthService) Login(in LoginInput) (*AuthResult, error) {
	email := strings.ToLower(strings.TrimSpace(in.Email))
	if s.guard.locked(email) {
		return nil, ErrAccountLocked
	}
	var user models.User
	if err := database.DB.Where("lower(email) = ?", email).First(&user).Error; err != nil {
		// do not reveal whether the account exists
		s.guard.fail(email)
		return nil, ErrInvalidCreds
	}
	if bcrypt.CompareHashAndPassword([]byte(user.Password), []byte(in.Password)) != nil {
		s.guard.fail(email)
		return nil, ErrInvalidCreds
	}
	s.guard.ok(email)
	var tenant models.Tenant
	if err := database.DB.First(&tenant, "id = ?", user.TenantID).Error; err != nil {
		return nil, ErrTenantNotFound
	}
	token, err := middleware.GenerateToken(s.cfg, &user)
	if err != nil {
		return nil, err
	}
	return &AuthResult{Token: token, User: user, Tenant: tenant}, nil
}

type CreateMemberInput struct {
	Email      string `json:"email"`
	Name       string `json:"name"`
	Password   string `json:"password"`
	Role       string `json:"role"`
	Department string `json:"department"`
	Title      string `json:"title"`
}

func (s *AuthService) CreateMember(tenantID uuid.UUID, in CreateMemberInput) error {
	email := strings.ToLower(strings.TrimSpace(in.Email))
	if email == "" || in.Password == "" {
		return errors.New("email and password required")
	}
	if err := s.validatePassword(in.Password); err != nil {
		return err
	}
	var existing models.User
	if err := database.DB.Where("lower(email) = ? AND tenant_id = ?", email, tenantID).First(&existing).Error; err == nil {
		return ErrEmailTaken
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(in.Password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	role := in.Role
	if role != models.RoleKnowledgeAdmin && role != models.RoleMember {
		role = models.RoleMember
	}
	dept := strings.TrimSpace(in.Department)
	if dept == "" {
		dept = "全员"
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = email
	}
	user := models.User{
		TenantID:   tenantID,
		Email:      email,
		Name:       name,
		Password:   string(hash),
		Role:       role,
		Department: dept,
		Title:      in.Title,
	}
	return database.DB.Create(&user).Error
}

func (s *AuthService) DeleteMember(tenantID, userID uuid.UUID) error {
	var user models.User
	if err := database.DB.First(&user, "id = ? AND tenant_id = ?", userID, tenantID).Error; err != nil {
		return err
	}
	if user.Role == models.RoleSuperAdmin {
		return errors.New("cannot delete super admin")
	}
	return database.DB.Delete(&user).Error
}

type UpdateMemberInput struct {
	Name       string `json:"name"`
	Role       string `json:"role"`
	Department string `json:"department"`
	Title      string `json:"title"`
}

func (s *AuthService) UpdateMember(tenantID, userID uuid.UUID, in UpdateMemberInput) error {
	var user models.User
	if err := database.DB.First(&user, "id = ? AND tenant_id = ?", userID, tenantID).Error; err != nil {
		return err
	}
	if user.Role == models.RoleSuperAdmin && in.Role != "" && in.Role != models.RoleSuperAdmin {
		return errors.New("cannot downgrade super admin")
	}
	updates := map[string]interface{}{}
	if in.Name != "" {
		updates["name"] = in.Name
	}
	if in.Department != "" {
		updates["department"] = in.Department
	}
	if in.Title != "" {
		updates["title"] = in.Title
	}
	if in.Role != "" {
		updates["role"] = in.Role
	}
	return database.DB.Model(&user).Updates(updates).Error
}

func ListMembers(tenantID uuid.UUID) ([]models.User, error) {
	var users []models.User
	err := database.DB.Where("tenant_id = ?", tenantID).Order("created_at asc").Find(&users).Error
	return users, err
}

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		out = "org"
	}
	if len(out) > 40 {
		out = out[:40]
	}
	return out + "-" + uuid.NewString()[:8]
}
