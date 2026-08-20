package handlers

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/gofiber/fiber/v2"
	"github.com/google/uuid"

	"github.com/enterprise-kb/backend/internal/database"
	"github.com/enterprise-kb/backend/internal/llm"
	"github.com/enterprise-kb/backend/internal/middleware"
	"github.com/enterprise-kb/backend/internal/models"
	"github.com/enterprise-kb/backend/internal/services"
)

type ChatHandler struct {
	svc *services.ChatService
}

func NewChatHandler(svc *services.ChatService) *ChatHandler {
	return &ChatHandler{svc: svc}
}

type askRequest struct {
	SessionID string `json:"session_id"`
	KBID      string `json:"kb_id"`
	Question  string `json:"question"`
	WebSearch bool   `json:"web_search"`
	History   []struct {
		Role    string `json:"role"`
		Content string `json:"content"`
	} `json:"history"`
	// simulate_as allows admins to test as another user (QA 测试窗口)
	SimulateAs string `json:"simulate_as"`
}

// preparedAsk holds everything parsed from the request body.
type preparedAsk struct {
	req        askRequest
	history    []llm.HistoryTurn
	sessionID  uuid.UUID
	actingUser *models.User
	isAdmin    bool
}

// prepareAsk parses + validates the ask request body (shared by JSON and SSE paths).
func (h *ChatHandler) prepareAsk(c *fiber.Ctx, user *models.User) (*preparedAsk, error) {
	var req askRequest
	if err := c.BodyParser(&req); err != nil {
		return nil, errors.New("invalid request body")
	}

	history := make([]llm.HistoryTurn, 0, len(req.History))
	for _, m := range req.History {
		if m.Role != "user" && m.Role != "assistant" {
			continue
		}
		if m.Content == "" {
			continue
		}
		history = append(history, llm.HistoryTurn{Role: m.Role, Content: m.Content})
	}
	if len(history) > 10 {
		history = history[len(history)-10:]
	}

	var sessionID uuid.UUID
	if req.SessionID != "" {
		sessionID, _ = uuid.Parse(req.SessionID)
	}

	// simulate as another user (admin QA test)
	actingUser := user
	if req.SimulateAs != "" && (user.Role == models.RoleSuperAdmin || user.Role == models.RoleKnowledgeAdmin) {
		if simID, err := uuid.Parse(req.SimulateAs); err == nil {
			var sim models.User
			if err := database.DB.First(&sim, "id = ? AND tenant_id = ?", simID, user.TenantID).Error; err == nil {
				actingUser = &models.User{
					ID: sim.ID, TenantID: sim.TenantID, Email: sim.Email, Role: sim.Role,
					Department: sim.Department, Name: sim.Name,
				}
			}
		}
	}

	isAdmin := actingUser.Role == models.RoleSuperAdmin || actingUser.Role == models.RoleKnowledgeAdmin
	return &preparedAsk{req: req, history: history, sessionID: sessionID, actingUser: actingUser, isAdmin: isAdmin}, nil
}

func (h *ChatHandler) buildAskInput(p *preparedAsk, user *models.User) services.AskInput {
	return services.AskInput{
		TenantID:  user.TenantID,
		UserID:    p.actingUser.ID,
		UserName:  p.actingUser.Name,
		SessionID: p.sessionID,
		KBID:      p.req.KBID,
		Question:  p.req.Question,
		History:   p.history,
		IsAdmin:   p.isAdmin,
		Dept:      p.actingUser.Department,
		WebSearch: p.req.WebSearch,
	}
}

func askError(c *fiber.Ctx, err error) error {
	if err == services.ErrNoAccess {
		return c.Status(403).JSON(fiber.Map{"error": err.Error()})
	}
	return c.Status(500).JSON(fiber.Map{"error": err.Error()})
}

func (h *ChatHandler) Ask(c *fiber.Ctx) error {
	user := middleware.CurrentUser(c)
	p, err := h.prepareAsk(c, user)
	if err != nil {
		return c.Status(400).JSON(fiber.Map{"error": err.Error()})
	}

	// SSE streaming when the client asks for it, JSON otherwise
	if strings.Contains(c.Get("Accept"), "text/event-stream") {
		return h.askStream(c, p, user)
	}
	return h.askJSON(c, p)
}

func (h *ChatHandler) askJSON(c *fiber.Ctx, p *preparedAsk) error {
	result, err := h.svc.Ask(h.buildAskInput(p, middleware.CurrentUser(c)))
	if err != nil {
		return askError(c, err)
	}
	return c.JSON(result)
}

// writeSSE writes a single framed SSE event and flushes.
func writeSSE(w *bufio.Writer, event string, data interface{}) {
	b, _ := json.Marshal(data)
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
	_ = w.Flush()
}

// askStream answers via SSE: meta(citations) → delta(tokens) → done(verdict).
func (h *ChatHandler) askStream(c *fiber.Ctx, p *preparedAsk, user *models.User) error {
	// prepare retrieval + session up-front so auth/access errors return JSON
	st, err := h.svc.AskStreamInit(h.buildAskInput(p, user))
	if err != nil {
		return askError(c, err)
	}

	// tie generation lifetime to the request so a disconnected client cancels it
	reqDone := c.Context().Done()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		select {
		case <-reqDone:
			cancel()
		case <-ctx.Done():
		}
	}()

	c.Set("Content-Type", "text/event-stream")
	c.Set("Cache-Control", "no-cache")
	c.Set("Connection", "keep-alive")
	c.Set("X-Accel-Buffering", "no")

	c.Context().SetBodyStreamWriter(func(w *bufio.Writer) {
		writeSSE(w, "meta", map[string]interface{}{
			"session_id":  st.Session.ID.String(),
			"user_msg_id": st.UserMsg.ID.String(),
			"citations":   st.Citations,
			"is_missed":   st.PreMissed,
		})

		result, err := h.svc.StreamAnswer(ctx, st, func(part string) {
			writeSSE(w, "delta", map[string]string{"content": part})
		})
		if err != nil {
			writeSSE(w, "error", map[string]string{"error": err.Error()})
			return
		}

		writeSSE(w, "done", map[string]interface{}{
			"message_id": result.AssistantMsg.ID.String(),
			"session_id": result.SessionID.String(),
			"answer":     result.Answer,
			"is_missed":  result.IsMissed,
			"citations":  result.Citations,
		})
	})
	return nil
}

func (h *ChatHandler) Sessions(c *fiber.Ctx) error {
	user := middleware.CurrentUser(c)
	sessions, err := services.RecentSessions(user.TenantID, user.ID)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(sessions)
}

func (h *ChatHandler) Messages(c *fiber.Ctx) error {
	user := middleware.CurrentUser(c)
	sid, err := uuid.Parse(c.Params("sessionId"))
	if err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "invalid session id"})
	}
	var session models.ChatSession
	if err := database.DB.First(&session, "id = ? AND tenant_id = ? AND user_id = ?", sid, user.TenantID, user.ID).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{"error": "session not found"})
	}
	msgs, err := services.SessionMessages(sid)
	if err != nil {
		return c.Status(500).JSON(fiber.Map{"error": err.Error()})
	}
	return c.JSON(msgs)
}

type feedbackRequest struct {
	Feedback string `json:"feedback"`
	Note     string `json:"note"`
}

func (h *ChatHandler) Feedback(c *fiber.Ctx) error {
	user := middleware.CurrentUser(c)
	mid, err := uuid.Parse(c.Params("messageId"))
	if err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "invalid message id"})
	}
	var req feedbackRequest
	if err := c.BodyParser(&req); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "invalid request body"})
	}
	var msg models.ChatMessage
	if err := database.DB.First(&msg, "id = ? AND tenant_id = ?", mid, user.TenantID).Error; err != nil {
		return c.Status(404).JSON(fiber.Map{"error": "message not found"})
	}
	if err := services.SetFeedback(mid, req.Feedback, req.Note); err != nil {
		return c.Status(400).JSON(fiber.Map{"error": err.Error()})
	}
	auditLog(user.TenantID, user.ID, user.Name, "feedback", "反馈("+req.Feedback+"): "+req.Note)
	return c.JSON(fiber.Map{"ok": true})
}

func (h *ChatHandler) DeleteSession(c *fiber.Ctx) error {
	user := middleware.CurrentUser(c)
	sid, err := uuid.Parse(c.Params("sessionId"))
	if err != nil {
		return c.Status(400).JSON(fiber.Map{"error": "invalid session id"})
	}
	database.DB.Where("id = ? AND tenant_id = ? AND user_id = ?", sid, user.TenantID, user.ID).Delete(&models.ChatMessage{})
	database.DB.Where("id = ? AND tenant_id = ? AND user_id = ?", sid, user.TenantID, user.ID).Delete(&models.ChatSession{})
	return c.JSON(fiber.Map{"ok": true})
}
