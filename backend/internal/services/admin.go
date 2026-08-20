package services

import (
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/enterprise-kb/backend/internal/database"
	"github.com/enterprise-kb/backend/internal/models"
)

type AdminService struct{}

func NewAdminService() *AdminService { return &AdminService{} }

type Stats struct {
	TenantName     string `json:"tenant_name"`
	TotalDocs      int64  `json:"total_docs"`
	ReadyDocs      int64  `json:"ready_docs"`
	FailedDocs     int64  `json:"failed_docs"`
	TotalChunks    int64  `json:"total_chunks"`
	TotalKBs       int64  `json:"total_kbs"`
	TotalMembers   int64  `json:"total_members"`
	TotalSessions  int64  `json:"total_sessions"`
	TotalMessages  int64  `json:"total_messages"`
	MissedMessages int64  `json:"missed_messages"`
	MissRate       float64 `json:"miss_rate"`
	Positive       int64  `json:"positive_feedback"`
	Negative       int64  `json:"negative_feedback"`
}

func (s *AdminService) Stats(tenantID uuid.UUID) (*Stats, error) {
	st := &Stats{}
	var count int64

	if err := database.DB.Model(&models.Tenant{}).Where("id = ?", tenantID).Count(&count).Error; err == nil {
		_ = count
	}
	var tenant models.Tenant
	if err := database.DB.First(&tenant, "id = ?", tenantID).Error; err == nil {
		st.TenantName = tenant.Name
	}

	database.DB.Model(&models.Document{}).Where("tenant_id = ?", tenantID).Count(&st.TotalDocs)
	database.DB.Model(&models.Document{}).Where("tenant_id = ? AND status = ?", tenantID, models.DocStatusReady).Count(&st.ReadyDocs)
	database.DB.Model(&models.Document{}).Where("tenant_id = ? AND status = ?", tenantID, models.DocStatusFailed).Count(&st.FailedDocs)
	database.DB.Model(&models.Chunk{}).Where("tenant_id = ?", tenantID).Count(&st.TotalChunks)
	database.DB.Model(&models.KnowledgeBase{}).Where("tenant_id = ?", tenantID).Count(&st.TotalKBs)
	database.DB.Model(&models.User{}).Where("tenant_id = ?", tenantID).Count(&st.TotalMembers)
	database.DB.Model(&models.ChatSession{}).Where("tenant_id = ?", tenantID).Count(&st.TotalSessions)
	database.DB.Model(&models.ChatMessage{}).Where("tenant_id = ? AND role = 'assistant'", tenantID).Count(&st.TotalMessages)
	database.DB.Model(&models.ChatMessage{}).Where("tenant_id = ? AND role = 'assistant' AND is_missed = true", tenantID).Count(&st.MissedMessages)
	if st.TotalMessages > 0 {
		st.MissRate = float64(st.MissedMessages) / float64(st.TotalMessages) * 100
	}
	database.DB.Model(&models.ChatMessage{}).Where("tenant_id = ? AND feedback = ?", tenantID, models.FeedbackUp).Count(&st.Positive)
	database.DB.Model(&models.ChatMessage{}).Where("tenant_id = ? AND feedback = ?", tenantID, models.FeedbackDown).Count(&st.Negative)
	return st, nil
}

type Gap struct {
	Question string `json:"question"`
	Count    int64  `json:"count"`
	LastSeen string `json:"last_seen"`
}

// Gaps clusters missed questions into candidate knowledge gaps.
func (s *AdminService) Gaps(tenantID uuid.UUID, limit int) ([]Gap, error) {
	if limit <= 0 {
		limit = 20
	}
	var msgs []models.ChatMessage
	if err := database.DB.Where("tenant_id = ? AND role = 'user'", tenantID).
		Order("created_at desc").
		Limit(500).
		Find(&msgs).Error; err != nil {
		return nil, err
	}
	// group by normalized question
	groups := map[string]*Gap{}
	var order []string
	for _, m := range msgs {
		norm := normalizeQuestion(m.Content)
		if norm == "" {
			continue
		}
		if _, ok := groups[norm]; !ok {
			groups[norm] = &Gap{Question: norm, LastSeen: m.CreatedAt.Format("2006-01-02 15:04")}
			order = append(order, norm)
		}
		groups[norm].Count++
	}
	out := make([]Gap, 0, len(order))
	for _, k := range order {
		out = append(out, *groups[k])
	}
	// simple desc sort by count
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].Count > out[i].Count {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func normalizeQuestion(q string) string {
	q = strings.TrimSpace(q)
	rs := []rune(q)
	if len(rs) > 30 {
		rs = rs[:30]
	}
	return string(rs)
}

type AuditEntry struct {
	models.AuditLog
}

func (s *AdminService) RecentAudit(tenantID uuid.UUID, limit int) ([]models.AuditLog, error) {
	if limit <= 0 {
		limit = 50
	}
	var logs []models.AuditLog
	err := database.DB.Where("tenant_id = ?", tenantID).
		Order("created_at desc").
		Limit(limit).
		Find(&logs).Error
	return logs, err
}

// CleanupAudit deletes audit logs older than retentionDays across all
// tenants. Returns the number of rows removed.
func CleanupAudit(retentionDays int) (int64, error) {
	if retentionDays <= 0 {
		return 0, nil
	}
	cutoff := time.Now().Add(-time.Duration(retentionDays) * 24 * time.Hour)
	res := database.DB.Where("created_at < ?", cutoff).Delete(&models.AuditLog{})
	return res.RowsAffected, res.Error
}

type FeedbackEntry struct {
	ID        uuid.UUID `json:"id"`
	Question  string    `json:"question"`
	Answer    string    `json:"answer"`
	Feedback  string    `json:"feedback"`
	Note      string    `json:"note"`
	UserName  string    `json:"user_name"`
	CreatedAt string    `json:"created_at"`
}

func (s *AdminService) FeedbackList(tenantID uuid.UUID) ([]FeedbackEntry, error) {
	var msgs []models.ChatMessage
	err := database.DB.Where("tenant_id = ? AND feedback != ?", tenantID, models.FeedbackNone).
		Order("created_at desc").
		Limit(100).
		Find(&msgs).Error
	if err != nil {
		return nil, err
	}
	if len(msgs) == 0 {
		return []FeedbackEntry{}, nil
	}

	// batch-load users (avoid 1 query per message)
	userIDs := make([]string, 0, len(msgs))
	sessionIDs := make([]string, 0, len(msgs))
	for _, m := range msgs {
		userIDs = appendIfMissing(userIDs, m.UserID.String())
		sessionIDs = appendIfMissing(sessionIDs, m.SessionID.String())
	}
	var users []models.User
	database.DB.Where("id IN ?", userIDs).Find(&users)
	nameByID := make(map[string]string, len(users))
	for _, u := range users {
		nameByID[u.ID.String()] = u.Name
	}

	// batch-load all user messages of the involved sessions (avoid 1 query
	// per feedback message to find the preceding question)
	var prevs []models.ChatMessage
	database.DB.Where("session_id IN ? AND role = 'user'", sessionIDs).
		Order("created_at asc").Find(&prevs)
	prevBySession := make(map[string][]models.ChatMessage)
	for _, p := range prevs {
		prevBySession[p.SessionID.String()] = append(prevBySession[p.SessionID.String()], p)
	}

	out := make([]FeedbackEntry, 0, len(msgs))
	for _, m := range msgs {
		question := ""
		if list, ok := prevBySession[m.SessionID.String()]; ok {
			for i := len(list) - 1; i >= 0; i-- {
				if list[i].CreatedAt.Before(m.CreatedAt) {
					question = list[i].Content
					break
				}
			}
		}
		out = append(out, FeedbackEntry{
			ID:        m.ID,
			Question:  question,
			Answer:    truncate(m.Content, 200),
			Feedback:  m.Feedback,
			Note:      m.FeedbackNote,
			UserName:  nameByID[m.UserID.String()],
			CreatedAt: m.CreatedAt.Format("2006-01-02 15:04"),
		})
	}
	return out, nil
}

// RecentSessions returns recent chat sessions for a user or tenant.
func RecentSessions(tenantID, userID uuid.UUID) ([]models.ChatSession, error) {
	var sessions []models.ChatSession
	q := database.DB.Where("tenant_id = ?", tenantID)
	if userID != uuid.Nil {
		q = q.Where("user_id = ?", userID)
	}
	err := q.Order("updated_at desc").Limit(50).Find(&sessions).Error
	return sessions, err
}

// RecentSessionsWithUsers returns recent sessions across the whole tenant,
// joined with the owning user's name/email for the admin console.
type SessionWithMeta struct {
	models.ChatSession
	UserName  string `json:"user_name"`
	UserEmail string `json:"user_email"`
	MsgCount  int64  `json:"msg_count"`
}

func RecentSessionsWithUsers(tenantID uuid.UUID, limit int) ([]SessionWithMeta, error) {
	if limit <= 0 {
		limit = 100
	}
	var sessions []models.ChatSession
	if err := database.DB.Where("tenant_id = ?", tenantID).
		Order("updated_at desc").Limit(limit).Find(&sessions).Error; err != nil {
		return nil, err
	}
	if len(sessions) == 0 {
		return []SessionWithMeta{}, nil
	}

	// batch-load users (avoid 1 query per session)
	userIDs := make([]string, 0, len(sessions))
	sessionIDs := make([]string, 0, len(sessions))
	for _, s := range sessions {
		userIDs = appendIfMissing(userIDs, s.UserID.String())
		sessionIDs = appendIfMissing(sessionIDs, s.ID.String())
	}
	var users []models.User
	database.DB.Where("id IN ?", userIDs).Find(&users)
	userByID := make(map[string]models.User, len(users))
	for _, u := range users {
		userByID[u.ID.String()] = u
	}

	// message counts in a single GROUP BY (avoid 1 count query per session)
	var counts []struct {
		SessionID uuid.UUID
		Cnt       int64
	}
	database.DB.Model(&models.ChatMessage{}).
		Select("session_id, count(*) as cnt").
		Where("session_id IN ?", sessionIDs).
		Group("session_id").
		Scan(&counts)
	cntByID := make(map[string]int64, len(counts))
	for _, c := range counts {
		cntByID[c.SessionID.String()] = c.Cnt
	}

	out := make([]SessionWithMeta, 0, len(sessions))
	for _, s := range sessions {
		u := userByID[s.UserID.String()]
		out = append(out, SessionWithMeta{
			ChatSession: s,
			UserName:    u.Name,
			UserEmail:   u.Email,
			MsgCount:    cntByID[s.ID.String()],
		})
	}
	return out, nil
}

// appendIfMissing appends s to list if not already present.
func appendIfMissing(list []string, s string) []string {
	for _, v := range list {
		if v == s {
			return list
		}
	}
	return append(list, s)
}

// SessionMessages returns messages for a session (ascending).
func SessionMessages(sessionID uuid.UUID) ([]models.ChatMessage, error) {
	var msgs []models.ChatMessage
	err := database.DB.Where("session_id = ?", sessionID).Order("created_at asc").Find(&msgs).Error
	return msgs, err
}

// SetFeedback records like/dislike on an assistant message.
func SetFeedback(msgID uuid.UUID, feedback, note string) error {
	if feedback != models.FeedbackUp && feedback != models.FeedbackDown {
		return errInvalidFeedback
	}
	return database.DB.Model(&models.ChatMessage{}).Where("id = ? AND role = 'assistant'", msgID).
		Updates(map[string]interface{}{"feedback": feedback, "feedback_note": note}).Error
}
