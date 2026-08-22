package handlers

import (
	"fmt"
	"os"
	"time"

	"github.com/google/uuid"

	"github.com/enterprise-kb/backend/internal/database"
	"github.com/enterprise-kb/backend/internal/models"
)

func osIsNotExist(err error) bool { return os.IsNotExist(err) }

// logWarn prints a non-fatal warning (kept consistent with services.logWarn).
func logWarn(format string, args ...interface{}) { fmt.Printf("[warn] "+format+"\n", args...) }

// auditLog writes an audit entry.
func auditLog(tenantID, userID uuid.UUID, userName, action, detail string) {
	database.DB.Create(&models.AuditLog{
		TenantID: tenantID,
		UserID:   userID,
		UserName: userName,
		Action:   action,
		Detail:   detail,
	})
}

func nowStr() string { return time.Now().Format(time.RFC3339) }
