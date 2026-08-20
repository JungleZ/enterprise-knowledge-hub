// Package bot integrates the knowledge base with IM chatbots (Feishu).
//
// It uses the Feishu long-connection (WebSocket) mode so no public callback
// URL is required. Users bind their Feishu open_id to an internal account
// (via the `绑定 <email>` command, approved by an admin), after which every
// @bot question is answered using the bound account's role/department, so
// visibility permissions are enforced exactly like in the web UI.
package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/larksuite/oapi-sdk-go/v3/ws"

	"github.com/enterprise-kb/backend/internal/config"
	"github.com/enterprise-kb/backend/internal/database"
	"github.com/enterprise-kb/backend/internal/models"
	"github.com/enterprise-kb/backend/internal/services"
)

const maxReplyLen = 1500

// Service runs the Feishu chatbot in a background goroutine.
type Service struct {
	cfg    config.BotConfig
	chat   *services.ChatService
	lark   *lark.Client
	prefix string // e.g. "绑定 " / "批准 " / "拒绝 " / "解绑 "

	// dedup guards against Feishu duplicate event pushes (same message_id).
	seenMu sync.Mutex
	seen   map[string]time.Time
}

func NewService(cfg config.BotConfig, chat *services.ChatService) *Service {
	return &Service{cfg: cfg, chat: chat, seen: map[string]time.Time{}}
}

// Start launches the Feishu long-connection client. It blocks; call in a goroutine.
func (s *Service) Start(ctx context.Context) error {
	if s.cfg.Platform != "feishu" || s.cfg.AppID == "" || s.cfg.AppSecret == "" {
		log.Printf("[bot] feishu disabled (BOT_PLATFORM/FEISHU_APP_ID/FEISHU_APP_SECRET not set)")
		return nil
	}

	handler := dispatcher.NewEventDispatcher("", "").OnP2MessageReceiveV1(s.onMessage)
	cli := lark.NewClient(s.cfg.AppID, s.cfg.AppSecret)
	s.lark = cli

	wsCli := ws.NewClient(s.cfg.AppID, s.cfg.AppSecret,
		ws.WithEventHandler(handler),
		ws.WithLogLevel(larkcore.LogLevelInfo),
	)
	wsCli.SetOnError(func(err error) { log.Printf("[bot] feishu ws error: %v", err) })
	wsCli.SetOnReady(func() { log.Printf("[bot] feishu connected, waiting for @bot messages") })

	log.Printf("[bot] starting feishu long-connection client (app %s)", s.cfg.AppID)
	return wsCli.Start(ctx)
}

// onMessage handles im.message.receive_v1 events.
func (s *Service) onMessage(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
	ev := event.Event
	if ev == nil || ev.Message == nil || ev.Sender == nil {
		return nil
	}

	chatType := ""
	if ev.Message.ChatType != nil {
		chatType = *ev.Message.ChatType
	}
	msgType := ""
	if ev.Message.MessageType != nil {
		msgType = *ev.Message.MessageType
	}
	openID := ""
	if ev.Sender.SenderId != nil && ev.Sender.SenderId.OpenId != nil {
		openID = *ev.Sender.SenderId.OpenId
	}
	messageID := ""
	if ev.Message.MessageId != nil {
		messageID = *ev.Message.MessageId
	}
	if messageID != "" && s.isSeen(messageID) {
		log.Printf("[bot] dedup: skip already-processed message %s", messageID)
		return nil
	}
	if messageID != "" {
		s.markSeen(messageID)
	}
	log.Printf("[bot] onMessage received: chat=%s mtype=%s sender=%s msg=%s", chatType, msgType, openID, messageID)

	// only handle messages sent by real users
	senderType := ""
	if ev.Sender.SenderType != nil {
		senderType = *ev.Sender.SenderType
	}
	if senderType != "user" {
		return nil
	}

	// only handle text messages
	if ev.Message.MessageType == nil || *ev.Message.MessageType != "text" {
		return nil
	}

	chatID := ""
	if ev.Message.ChatId != nil {
		chatID = *ev.Message.ChatId
	}
	if openID == "" {
		return nil
	}

	text, err := extractText(ev.Message.Content)
	if err != nil {
		return nil
	}
	text = strings.TrimSpace(stripMentions(text))
	if text == "" {
		return nil
	}

	// handle commands regardless of @ mention (p2p or direct commands)
	if handled := s.handleCommand(ctx, chatID, openID, text, msgType); handled {
		return nil
	}

	// only respond to @bot in groups, or any message in p2p chat
	if msgType == "group" && !isBotMentioned(event) {
		return nil
	}

	s.replyToQuestion(ctx, chatID, openID, text)
	return nil
}

// handleCommand processes 绑定/批准/拒绝/解绑/状态 commands.
// Returns true if the message was a command (consumed).
func (s *Service) handleCommand(ctx context.Context, chatID, openID, text, chatType string) bool {
	trimmed := strings.TrimSpace(text)
	switch {
	case strings.HasPrefix(trimmed, "绑定"):
		return s.cmdBind(ctx, chatID, openID, strings.TrimSpace(strings.TrimPrefix(trimmed, "绑定")))
	case strings.HasPrefix(trimmed, "批准"):
		return s.cmdApprove(ctx, chatID, openID, strings.TrimSpace(strings.TrimPrefix(trimmed, "批准")))
	case strings.HasPrefix(trimmed, "拒绝"):
		return s.cmdReject(ctx, chatID, openID, strings.TrimSpace(strings.TrimPrefix(trimmed, "拒绝")))
	case strings.HasPrefix(trimmed, "解绑"):
		return s.cmdUnbind(ctx, chatID, openID, strings.TrimSpace(strings.TrimPrefix(trimmed, "解绑")))
	case strings.HasPrefix(trimmed, "绑定状态") || trimmed == "状态":
		return s.cmdStatus(ctx, chatID, openID)
	}
	return false
}

// ---------- commands ----------

func (s *Service) cmdBind(ctx context.Context, chatID, openID, email string) bool {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		s.sendText(ctx, chatID, "用法：绑定 <系统邮箱>\n例如：绑定 admin@demo.local")
		return true
	}
	var user models.User
	if err := database.DB.First(&user, "lower(email) = ?", email).Error; err != nil {
		s.sendText(ctx, chatID, fmt.Sprintf("系统中不存在邮箱 %s 的账号，请联系管理员。", email))
		return true
	}

	// reject if already approved
	var existing models.BotBinding
	if err := database.DB.Where("open_id = ? AND status = ?", openID, models.BindingApproved).First(&existing).Error; err == nil {
		s.sendText(ctx, chatID, fmt.Sprintf("你已绑定 %s，如需更换请先发送：解绑", existing.Email))
		return true
	}

	binding := models.BotBinding{
		TenantID: user.TenantID,
		Platform: "feishu",
		OpenID:   openID,
		UserID:   user.ID,
		Email:    user.Email,
		Status:   models.BindingPending,
	}
	if err := database.DB.Where("open_id = ? AND status = ?", openID, models.BindingPending).
		Assign(binding).FirstOrCreate(&binding).Error; err != nil {
		log.Printf("[bot] bind create error: %v", err)
		s.sendText(ctx, chatID, "绑定请求创建失败，请稍后再试。")
		return true
	}

	s.sendText(ctx, chatID, fmt.Sprintf("已提交绑定申请：%s（%s）。\n请管理员在飞书中发送：批准 %s 完成绑定。", user.Name, user.Email, user.Email))
	return true
}

func (s *Service) cmdApprove(ctx context.Context, chatID, openID, email string) bool {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		s.sendText(ctx, chatID, "用法：批准 <邮箱>\n例如：批准 admin@demo.local")
		return true
	}
	if !s.isAdmin(openID) {
		s.sendText(ctx, chatID, "只有 super_admin / knowledge_admin 角色可以批准绑定。")
		return true
	}
	var binding models.BotBinding
	if err := database.DB.Where("lower(email) = ? AND status = ?", email, models.BindingPending).
		Order("created_at asc").First(&binding).Error; err != nil {
		s.sendText(ctx, chatID, fmt.Sprintf("没有找到 %s 的待审批绑定申请。", email))
		return true
	}
	var target models.User
	if err := database.DB.First(&target, "id = ?", binding.UserID).Error; err != nil {
		s.sendText(ctx, chatID, "绑定申请关联的系统账号不存在，无法批准。")
		return true
	}
	approverRole := s.boundRole(openID)
	if !canApproveRole(approverRole, target.Role) {
		if target.Role == models.RoleSuperAdmin {
			s.sendText(ctx, chatID, "该绑定是管理员账号，只有 super_admin 角色可以批准。")
		} else {
			s.sendText(ctx, chatID, "只有 super_admin / knowledge_admin 角色可以批准绑定。")
		}
		return true
	}
	if err := database.DB.Model(&binding).Updates(map[string]interface{}{
		"status": models.BindingApproved, "updated_at": time.Now(),
	}).Error; err != nil {
		log.Printf("[bot] approve error: %v", err)
		s.sendText(ctx, chatID, "批准失败，请稍后再试。")
		return true
	}
	s.sendText(ctx, chatID, fmt.Sprintf("已批准 %s 的绑定申请。", email))
	return true
}

func (s *Service) cmdReject(ctx context.Context, chatID, openID, email string) bool {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		s.sendText(ctx, chatID, "用法：拒绝 <邮箱>")
		return true
	}
	if !s.isAdmin(openID) {
		s.sendText(ctx, chatID, "只有 super_admin / knowledge_admin 角色可以拒绝绑定。")
		return true
	}
	var binding models.BotBinding
	if err := database.DB.Where("lower(email) = ? AND status = ?", email, models.BindingPending).
		Order("created_at asc").First(&binding).Error; err != nil {
		s.sendText(ctx, chatID, fmt.Sprintf("没有找到 %s 的待审批绑定申请。", email))
		return true
	}
	if err := database.DB.Model(&binding).Updates(map[string]interface{}{
		"status": models.BindingRejected, "updated_at": time.Now(),
	}).Error; err != nil {
		log.Printf("[bot] reject error: %v", err)
		s.sendText(ctx, chatID, "拒绝失败，请稍后再试。")
		return true
	}
	s.sendText(ctx, chatID, fmt.Sprintf("已拒绝 %s 的绑定申请。", email))
	return true
}

func (s *Service) cmdUnbind(ctx context.Context, chatID, openID, _ string) bool {
	res := database.DB.Where("open_id = ?", openID).Delete(&models.BotBinding{})
	if res.RowsAffected > 0 {
		s.sendText(ctx, chatID, "已解除绑定。之后@我提问前需重新绑定。")
	} else {
		s.sendText(ctx, chatID, "当前没有绑定记录。")
	}
	return true
}

func (s *Service) cmdStatus(ctx context.Context, chatID, openID string) bool {
	var binding models.BotBinding
	if err := database.DB.Where("open_id = ?", openID).Order("updated_at desc").First(&binding).Error; err != nil {
		s.sendText(ctx, chatID, "你还没有绑定系统账号。\n请发送：绑定 <系统邮箱>\n然后请管理员发送：批准 <系统邮箱>")
		return true
	}
	var user models.User
	userName := binding.Email
	if err := database.DB.First(&user, "id = ?", binding.UserID).Error; err == nil {
		userName = fmt.Sprintf("%s（%s，%s）", user.Name, user.Email, user.Role)
	}
	switch binding.Status {
	case models.BindingApproved:
		s.sendText(ctx, chatID, fmt.Sprintf("已绑定：%s。可以@我提问了。", userName))
	case models.BindingPending:
		s.sendText(ctx, chatID, fmt.Sprintf("绑定申请待审批：%s。请管理员发送：批准 %s", binding.Email, binding.Email))
	default:
		s.sendText(ctx, chatID, fmt.Sprintf("绑定申请已被拒绝：%s。可重新发送：绑定 %s", binding.Email, binding.Email))
	}
	return true
}

func (s *Service) isAdmin(openID string) bool {
	role := s.boundRole(openID)
	return role == models.RoleSuperAdmin || role == models.RoleKnowledgeAdmin
}

// boundRole returns the system role bound to the given bot open id ("" if none).
func (s *Service) boundRole(openID string) string {
	var binding models.BotBinding
	if err := database.DB.Where("open_id = ? AND status = ?", openID, models.BindingApproved).First(&binding).Error; err != nil {
		return ""
	}
	var user models.User
	if err := database.DB.First(&user, "id = ?", binding.UserID).Error; err != nil {
		return ""
	}
	return user.Role
}

// canApproveRole reports whether an approver with role approverRole may approve
// a binding for a target user with role targetRole. Super admin bindings can
// only be approved by a super admin (tiered approval).
func canApproveRole(approverRole, targetRole string) bool {
	if targetRole == models.RoleSuperAdmin {
		return approverRole == models.RoleSuperAdmin
	}
	return approverRole == models.RoleSuperAdmin || approverRole == models.RoleKnowledgeAdmin
}

// ---------- question answering ----------

func (s *Service) replyToQuestion(ctx context.Context, chatID, openID, question string) {
	var binding models.BotBinding
	if err := database.DB.Where("open_id = ? AND status = ?", openID, models.BindingApproved).First(&binding).Error; err != nil {
		s.sendText(ctx, chatID, "你尚未绑定系统账号，无法提问。\n请先发送：绑定 <系统邮箱>\n然后请管理员发送：批准 <系统邮箱>")
		return
	}
	var user models.User
	if err := database.DB.First(&user, "id = ?", binding.UserID).Error; err != nil {
		s.sendText(ctx, chatID, "绑定的账号不存在，请重新绑定。")
		return
	}

	isAdmin := user.Role == models.RoleSuperAdmin || user.Role == models.RoleKnowledgeAdmin

	result, err := s.chat.Ask(services.AskInput{
		TenantID: user.TenantID,
		UserID:   user.ID,
		UserName: user.Name,
		KBID:     "",
		Question: question,
		History:  nil,
		IsAdmin:  isAdmin,
		Dept:     user.Department,
	})
	if err != nil {
		log.Printf("[bot] ask error (user=%s): %v", user.Email, err)
		s.sendText(ctx, chatID, "抱歉，我暂时无法回答这个问题，请稍后再试。")
		return
	}

	reply := buildReply(result)
	s.sendText(ctx, chatID, reply)
}

func buildReply(r *services.AskResult) string {
	var sb strings.Builder
	sb.WriteString("[回复 " + time.Now().Format("15:04:05") + "]\n")
	sb.WriteString(strings.TrimSpace(r.Answer))
	if len(r.Citations) > 0 && !r.IsMissed {
		sb.WriteString("\n\n参考来源：")
		seen := map[string]bool{}
		n := 0
		for _, c := range r.Citations {
			if seen[c.DocTitle] {
				continue
			}
			seen[c.DocTitle] = true
			n++
			sb.WriteString(fmt.Sprintf("\n%d. 《%s》", n, c.DocTitle))
			if n >= 5 {
				break
			}
		}
	}
	out := strings.TrimSpace(sb.String())
	if len([]rune(out)) > maxReplyLen {
		out = string([]rune(out)[:maxReplyLen]) + "…"
	}
	return out
}

// ---------- helpers ----------

// dedupWindow is how long a message_id is remembered to absorb duplicate pushes.
const dedupWindow = 10 * time.Minute

// isSeen reports whether messageID was already handled within dedupWindow.
func (s *Service) isSeen(messageID string) bool {
	s.seenMu.Lock()
	defer s.seenMu.Unlock()
	t, ok := s.seen[messageID]
	if !ok {
		return false
	}
	return time.Since(t) < dedupWindow
}

// markSeen records messageID so duplicate pushes are dropped.
func (s *Service) markSeen(messageID string) {
	s.seenMu.Lock()
	defer s.seenMu.Unlock()
	// opportunistic cleanup of expired entries
	for id, t := range s.seen {
		if time.Since(t) >= dedupWindow {
			delete(s.seen, id)
		}
	}
	s.seen[messageID] = time.Now()
}

func (s *Service) sendText(ctx context.Context, chatID, text string) {
	if s.lark == nil || chatID == "" || text == "" {
		return
	}
	content, _ := json.Marshal(map[string]string{"text": text})
	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType("chat_id").
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(chatID).
			MsgType("text").
			Content(string(content)).
			Build()).
		Build()
	resp, err := s.lark.Im.Message.Create(ctx, req)
	if err != nil {
		log.Printf("[bot] send message error: %v", err)
		return
	}
	if !resp.Success() {
		log.Printf("[bot] send message failed: code=%d msg=%s", resp.Code, resp.Msg)
	}
}

// extractText parses the JSON content of a text message.
func extractText(content *string) (string, error) {
	if content == nil {
		return "", nil
	}
	var m struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(*content), &m); err != nil {
		return "", err
	}
	return m.Text, nil
}

// stripMentions removes @placeholders like "@_user_1" and @bot text.
func stripMentions(text string) string {
	text = strings.ReplaceAll(text, "@_user_", "@")
	return text
}

// isBotMentioned reports whether the bot was @mentioned in the message.
func isBotMentioned(event *larkim.P2MessageReceiveV1) bool {
	ev := event.Event
	if ev == nil || ev.Message == nil {
		return false
	}
	for _, m := range ev.Message.Mentions {
		if m == nil || m.MentionedType == nil || m.Id == nil || m.Id.OpenId == nil {
			continue
		}
		if *m.MentionedType == "bot" {
			return true
		}
	}
	return false
}
