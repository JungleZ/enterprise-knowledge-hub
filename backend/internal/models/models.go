package models

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"gorm.io/gorm"
)

// Vector is a pgvector wrapper (float32 array).
type Vector []float32

func (v Vector) Value() (driver.Value, error) {
	if v == nil {
		return "[]", nil
	}
	parts := make([]string, len(v))
	for i, f := range v {
		parts[i] = fmt.Sprintf("%g", f)
	}
	return "[" + strings.Join(parts, ",") + "]", nil
}

func (v *Vector) Scan(value interface{}) error {
	if value == nil {
		*v = nil
		return nil
	}
	var s string
	switch t := value.(type) {
	case string:
		s = t
	case []byte:
		s = string(t)
	default:
		return errors.New("invalid vector type")
	}
	s = strings.TrimPrefix(s, "[")
	s = strings.TrimSuffix(s, "]")
	if s == "" {
		*v = nil
		return nil
	}
	parts := strings.Split(s, ",")
	vec := make(Vector, len(parts))
	for i, p := range parts {
		var f float32
		if _, err := fmt.Sscanf(strings.TrimSpace(p), "%g", &f); err != nil {
			return err
		}
		vec[i] = f
	}
	*v = vec
	return nil
}

// Roles
const (
	RoleSuperAdmin      = "super_admin"
	RoleKnowledgeAdmin  = "knowledge_admin"
	RoleMember          = "member"
)

// Tenant is the multi-tenant org base.
type Tenant struct {
	ID        uuid.UUID `gorm:"type:uuid;primary_key" json:"id"`
	Name      string    `gorm:"not null" json:"name"`
	Slug      string    `gorm:"uniqueIndex;not null" json:"slug"`
	Plan      string    `gorm:"default:free" json:"plan"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type User struct {
	ID         uuid.UUID `gorm:"type:uuid;primary_key" json:"id"`
	TenantID   uuid.UUID `gorm:"type:uuid;not null;uniqueIndex:idx_tenant_email" json:"tenant_id"`
	Email      string    `gorm:"not null;uniqueIndex:idx_tenant_email" json:"email"`
	Password   string    `gorm:"not null" json:"-"`
	Name       string    `gorm:"not null" json:"name"`
	Role       string    `gorm:"default:member" json:"role"`
	Department string    `json:"department"`
	Title      string    `json:"title"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type KnowledgeBase struct {
	ID          uuid.UUID `gorm:"type:uuid;primary_key" json:"id"`
	TenantID    uuid.UUID `gorm:"type:uuid;not null;index" json:"tenant_id"`
	Name        string    `gorm:"not null" json:"name"`
	Description string    `json:"description"`
	// AllowedDepartments limits which departments may see this KB. empty = all.
	AllowedDepartments string `json:"allowed_departments"`
	CreatorID          uuid.UUID `gorm:"type:uuid" json:"creator_id"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

const (
	DocStatusProcessing = "processing"
	DocStatusReady      = "ready"
	DocStatusFailed     = "failed"
)

type Document struct {
	ID          uuid.UUID `gorm:"type:uuid;primary_key" json:"id"`
	TenantID    uuid.UUID `gorm:"type:uuid;not null;index" json:"tenant_id"`
	KBID        uuid.UUID `gorm:"type:uuid;not null;index" json:"kb_id"`
	Title       string    `gorm:"not null" json:"title"`
	Filename    string    `gorm:"not null" json:"filename"`
	FileSize    int64     `json:"file_size"`
	ContentType string    `json:"content_type"`
	Status      string    `gorm:"default:processing" json:"status"`
	Error       string    `json:"error"`
	ChunkCount  int       `json:"chunk_count"`
	// AccessTags comma separated dept tags. empty = public ("all").
	AccessTags string    `gorm:"default:all" json:"access_tags"`
	CreatedBy  uuid.UUID `gorm:"type:uuid" json:"created_by"`
	CreatedAt  time.Time `json:"created_at"`
	UpdatedAt  time.Time `json:"updated_at"`
}

type Chunk struct {
	ID         uuid.UUID `gorm:"type:uuid;primary_key" json:"id"`
	TenantID   uuid.UUID `gorm:"type:uuid;not null;index" json:"tenant_id"`
	KBID       uuid.UUID `gorm:"type:uuid;not null;index" json:"kb_id"`
	DocID      uuid.UUID `gorm:"type:uuid;not null;index" json:"doc_id"`
	ChunkIndex int       `json:"chunk_index"`
	Page       int       `json:"page"` // 1-based page in source doc (0 = unknown / not applicable)
	Text       string    `gorm:"type:text;not null" json:"text"`
	Title      string    `json:"title"`
	// Visibility list for retrieval filter. e.g. ["public"] or ["研发","财务"]
	Visibility []string `gorm:"serializer:json" json:"visibility"`
	Embedding  Vector    `gorm:"type:vector(1024);column:embedding" json:"-"`
	CreatedAt  time.Time `json:"created_at"`
}

type ChatSession struct {
	ID        uuid.UUID `gorm:"type:uuid;primary_key" json:"id"`
	TenantID  uuid.UUID `gorm:"type:uuid;not null;index" json:"tenant_id"`
	UserID    uuid.UUID `gorm:"type:uuid;not null;index" json:"user_id"`
	Title     string    `json:"title"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

const (
	FeedbackNone = "none"
	FeedbackUp   = "up"
	FeedbackDown = "down"
)

type Citation struct {
	DocID     string `json:"doc_id"`
	DocTitle  string `json:"doc_title"`
	ChunkID   string `json:"chunk_id"`
	Snippet   string `json:"snippet"`
	Score     float64 `json:"score"`
	ChunkText string `json:"chunk_text,omitempty"`
	// ChunkIndex is the position of the chunk inside its document (0-based).
	ChunkIndex int `json:"chunk_index"`
	// Page is the 1-based page number inside the source document (0 = unknown).
	Page int `json:"page,omitempty"`
	// URL is set for web-search citations (联网搜索来源), empty for KB docs.
	URL string `json:"url,omitempty"`
}

type ChatMessage struct {
	ID        uuid.UUID `gorm:"type:uuid;primary_key" json:"id"`
	SessionID uuid.UUID `gorm:"type:uuid;not null;index" json:"session_id"`
	TenantID  uuid.UUID `gorm:"type:uuid;not null;index" json:"tenant_id"`
	UserID    uuid.UUID `gorm:"type:uuid;not null;index" json:"user_id"`
	Role      string    `gorm:"not null" json:"role"` // user | assistant
	Content   string    `gorm:"type:text;not null" json:"content"`
	Citations []Citation `gorm:"serializer:json" json:"citations"`
	// Missed = retrieval found nothing confident enough (knowledge gap signal)
	IsMissed    bool      `json:"is_missed"`
	Feedback    string    `gorm:"default:none" json:"feedback"`
	FeedbackNote string   `json:"feedback_note"`
	CreatedAt   time.Time `json:"created_at"`
}

type AuditLog struct {
	ID        uuid.UUID `gorm:"type:uuid;primary_key" json:"id"`
	TenantID  uuid.UUID `gorm:"type:uuid;not null;index" json:"tenant_id"`
	UserID    uuid.UUID `gorm:"type:uuid" json:"user_id"`
	UserName  string    `json:"user_name"`
	Action    string    `gorm:"not null" json:"action"`
	Detail    string    `gorm:"type:text" json:"detail"`
	CreatedAt time.Time `json:"created_at"`
}

// Bot binding statuses
const (
	BindingPending  = "pending"
	BindingApproved = "approved"
	BindingRejected = "rejected"
)

// BotBinding maps an IM platform user (e.g. Feishu open_id) to an internal
// account so that chatbot answers respect the bound user's role/department.
type BotBinding struct {
	ID        uuid.UUID `gorm:"type:uuid;primary_key" json:"id"`
	TenantID  uuid.UUID `gorm:"type:uuid;not null;index" json:"tenant_id"`
	Platform  string    `gorm:"default:feishu" json:"platform"`
	OpenID    string    `gorm:"not null;index" json:"open_id"`
	UserID    uuid.UUID `gorm:"type:uuid;not null;index" json:"user_id"`
	Email     string    `json:"email"`
	Status    string    `gorm:"default:pending" json:"status"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func AutoMigrate(db *gorm.DB) error {
	return db.AutoMigrate(
		&Tenant{},
		&User{},
		&KnowledgeBase{},
		&Document{},
		&Chunk{},
		&ChatSession{},
		&ChatMessage{},
		&AuditLog{},
		&BotBinding{},
	)
}

func (t *Tenant) BeforeCreate(tx *gorm.DB) error { if t.ID == uuid.Nil { t.ID = uuid.New() }; return nil }
func (u *User) BeforeCreate(tx *gorm.DB) error  { if u.ID == uuid.Nil { u.ID = uuid.New() }; return nil }
func (k *KnowledgeBase) BeforeCreate(tx *gorm.DB) error { if k.ID == uuid.Nil { k.ID = uuid.New() }; return nil }
func (d *Document) BeforeCreate(tx *gorm.DB) error { if d.ID == uuid.Nil { d.ID = uuid.New() }; return nil }
func (c *Chunk) BeforeCreate(tx *gorm.DB) error { if c.ID == uuid.Nil { c.ID = uuid.New() }; return nil }
func (s *ChatSession) BeforeCreate(tx *gorm.DB) error { if s.ID == uuid.Nil { s.ID = uuid.New() }; return nil }
func (m *ChatMessage) BeforeCreate(tx *gorm.DB) error { if m.ID == uuid.Nil { m.ID = uuid.New() }; return nil }
func (a *AuditLog) BeforeCreate(tx *gorm.DB) error { if a.ID == uuid.Nil { a.ID = uuid.New() }; return nil }
func (b *BotBinding) BeforeCreate(tx *gorm.DB) error { if b.ID == uuid.Nil { b.ID = uuid.New() }; return nil }
