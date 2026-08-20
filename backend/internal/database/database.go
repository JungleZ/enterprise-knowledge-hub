package database

import (
	"fmt"
	"log"
	"time"

	"github.com/google/uuid"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"github.com/enterprise-kb/backend/internal/models"
)

var DB *gorm.DB

func Connect(dsn string) *gorm.DB {
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Warn),
	})
	if err != nil {
		log.Fatalf("failed to connect to database: %v", err)
	}

	sqlDB, err := db.DB()
	if err != nil {
		log.Fatalf("failed to get sql db: %v", err)
	}
	sqlDB.SetMaxOpenConns(20)
	sqlDB.SetMaxIdleConns(5)
	sqlDB.SetConnMaxLifetime(time.Hour)

	// Enable pgvector
	if err := db.Exec("CREATE EXTENSION IF NOT EXISTS vector").Error; err != nil {
		log.Printf("warning: failed to enable pgvector: %v", err)
	}

	// Migrate the legacy email-only unique index to a composite
	// (tenant_id, email) unique index so the same email may exist in
	// different tenants. The old single-column index is dropped first so
	// AutoMigrate can create the composite one under the same name.
	db.Exec("DROP INDEX IF EXISTS idx_tenant_email")

	DB = db
	return db
}

// ParseUUID is a small helper to validate a UUID string.
func ParseUUID(s string) (uuid.UUID, error) {
	return uuid.Parse(s)
}

// IDFromString returns parsed UUID or error.
func IDFromString(s string) (uuid.UUID, error) {
	if s == "" {
		return uuid.Nil, fmt.Errorf("empty id")
	}
	return uuid.Parse(s)
}

func init() {
	// keep import used in case models are referenced
	_ = models.RoleMember
}
