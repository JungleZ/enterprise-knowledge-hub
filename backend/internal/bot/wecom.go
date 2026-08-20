// WeCom (企业微信) smart robot integration via WebSocket long connection.
//
// The long-connection mode needs no public URL. Protocol is a simple JSON
// WebSocket channel:
//   subscribe: {cmd:"aibot_subscribe", body:{bot_id, secret}}
//   message:   {cmd:"aibot_msg_callback", headers:{req_id}, body:{msgid, chatid,
//               chattype, from:{userid}, msgtype, text:{content}}}
//   reply:     {cmd:"aibot_respond_msg", headers:{req_id}, body:{msgtype:"markdown",
//               markdown:{content}}}  // req_id MUST be the callback's req_id
//   heartbeat: {cmd:"ping"} every ~30s.
package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"

	"github.com/enterprise-kb/backend/internal/config"
	"github.com/enterprise-kb/backend/internal/database"
	"github.com/enterprise-kb/backend/internal/models"
	"github.com/enterprise-kb/backend/internal/services"
)

const wecomWSURL = "wss://openws.work.weixin.qq.com"

// WeComService runs the WeCom smart robot in a background goroutine.
type WeComService struct {
	cfg    config.BotConfig
	chat   *services.ChatService
	conn   *websocket.Conn
	mu     sync.Mutex // guards conn writes
	closed chan struct{}

	seenMu sync.Mutex
	seen   map[string]time.Time // msgid -> received time

	curReqID string // req_id of the callback currently being handled
}

func NewWeComService(cfg config.BotConfig, chat *services.ChatService) *WeComService {
	return &WeComService{
		cfg:    cfg,
		chat:   chat,
		closed: make(chan struct{}),
		seen:   map[string]time.Time{},
	}
}

// Start launches the WeCom long-connection client. It blocks; call in a goroutine.
func (s *WeComService) Start(ctx context.Context) error {
	if s.cfg.Platform != "wecom" || s.cfg.AppID == "" || s.cfg.AppSecret == "" {
		log.Printf("[wecom] disabled (BOT_PLATFORM/WECOM_BOT_ID/WECOM_BOT_SECRET not set)")
		return nil
	}
	log.Printf("[wecom] starting long-connection client (bot %s)", s.cfg.AppID)

	dialer := websocket.Dialer{
		HandshakeTimeout: 15 * time.Second,
	}

	backoff := time.Second
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-s.closed:
			return nil
		default:
		}

		conn, _, err := dialer.Dial(wecomWSURL, nil)
		if err != nil {
			log.Printf("[wecom] dial error: %v (retry in %v)", err, backoff)
			time.Sleep(backoff)
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		s.mu.Lock()
		s.conn = conn
		s.mu.Unlock()

		log.Printf("[wecom] connected to %s", wecomWSURL)
		if err := s.subscribe(); err != nil {
			log.Printf("[wecom] subscribe failed: %v", err)
			conn.Close()
			time.Sleep(backoff)
			continue
		}
		log.Printf("[wecom] subscribed, waiting for messages")

		backoff = time.Second
		err = s.run(ctx, conn)
		conn.Close()
		s.mu.Lock()
		s.conn = nil
		s.mu.Unlock()
		if err != nil && !isClosedErr(err) {
			log.Printf("[wecom] connection ended: %v (reconnecting)", err)
		}
	}
}

func isClosedErr(err error) bool {
	return websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) ||
		strings.Contains(strings.ToLower(err.Error()), "use of closed network connection")
}

// subscribe performs the aibot_subscribe handshake and waits for the ack.
func (s *WeComService) subscribe() error {
	reqID := fmt.Sprintf("sub_%d", time.Now().UnixNano())
	payload, _ := json.Marshal(map[string]interface{}{
		"cmd": "aibot_subscribe",
		"headers": map[string]string{
			"req_id": reqID,
		},
		"body": map[string]string{
			"bot_id": s.cfg.AppID,
			"secret": s.cfg.AppSecret,
		},
	})
	s.mu.Lock()
	if err := s.conn.SetWriteDeadline(time.Now().Add(10 * time.Second)); err != nil {
		s.mu.Unlock()
		return err
	}
	err := s.conn.WriteMessage(websocket.TextMessage, payload)
	s.mu.Unlock()
	if err != nil {
		return err
	}

	// wait for the subscribe ack
	s.conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	_, ack, err := s.conn.ReadMessage()
	if err != nil {
		return fmt.Errorf("subscribe ack read: %w", err)
	}
	// clear read deadline so the message loop can block indefinitely
	s.conn.SetReadDeadline(time.Time{})
	var resp struct {
		Errcode int    `json:"errcode"`
		Errmsg  string `json:"errmsg"`
	}
	_ = json.Unmarshal(ack, &resp)
	log.Printf("[wecom] subscribe ack: %s", string(ack))
	if resp.Errcode != 0 {
		return fmt.Errorf("subscribe rejected: errcode=%d errmsg=%s", resp.Errcode, resp.Errmsg)
	}
	return nil
}

// run reads frames, dispatches callbacks, and sends periodic heartbeats.
func (s *WeComService) run(ctx context.Context, conn *websocket.Conn) error {
	// heartbeat goroutine
	done := make(chan struct{})
	defer close(done)
	go func() {
		ticker := time.NewTicker(25 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				s.ping()
			case <-done:
				return
			case <-ctx.Done():
				return
			}
		}
	}()

	for {
		_, msg, err := conn.ReadMessage()
		if err != nil {
			return err
		}
		var frame struct {
			Cmd     string          `json:"cmd"`
			Headers struct {
				ReqID string `json:"req_id"`
			} `json:"headers"`
			Body json.RawMessage `json:"body"`
		}
		if err := json.Unmarshal(msg, &frame); err != nil {
			log.Printf("[wecom] bad frame: %v", err)
			continue
		}
		if frame.Cmd == "" {
			log.Printf("[wecom] frame (no cmd): %s", string(msg))
		}
		switch frame.Cmd {
		case "aibot_msg_callback":
			s.handleMessage(ctx, frame.Headers.ReqID, frame.Body)
		case "aibot_event_callback":
			s.handleEvent(frame.Body)
		case "ping", "pong":
			// server heartbeat, ignore
		}
	}
}

func (s *WeComService) ping() {
	reqID := fmt.Sprintf("p_%d", time.Now().UnixNano())
	payload, _ := json.Marshal(map[string]interface{}{
		"cmd": "ping",
		"headers": map[string]string{
			"req_id": reqID,
		},
	})
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil {
		return
	}
	_ = s.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err := s.conn.WriteMessage(websocket.TextMessage, payload); err != nil {
		log.Printf("[wecom] heartbeat error: %v", err)
	}
	log.Printf("[wecom] heartbeat sent %s", reqID)
}

// wecomMessage mirrors the aibot_msg_callback body for text messages.
type wecomMessage struct {
	MsgID    string `json:"msgid"`
	AibotID  string `json:"aibotid"`
	ChatID   string `json:"chatid"`
	ChatType string `json:"chattype"`
	From     struct {
		UserID string `json:"userid"`
	} `json:"from"`
	MsgType string `json:"msgtype"`
	Text    struct {
		Content string `json:"content"`
	} `json:"text"`
}

func (s *WeComService) handleMessage(ctx context.Context, reqID string, raw json.RawMessage) {
	var m wecomMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return
	}
	// dedup by msgid
	if m.MsgID != "" {
		s.seenMu.Lock()
		if t, ok := s.seen[m.MsgID]; ok && time.Since(t) < 10*time.Minute {
			s.seenMu.Unlock()
			log.Printf("[wecom] dedup: skip %s", m.MsgID)
			return
		}
		s.seen[m.MsgID] = time.Now()
		// opportunistic cleanup
		for id, t := range s.seen {
			if time.Since(t) >= 10*time.Minute {
				delete(s.seen, id)
			}
		}
		s.seenMu.Unlock()
	}

	log.Printf("[wecom] message: chat=%s mtype=%s sender=%s msg=%s", m.ChatType, m.MsgType, m.From.UserID, m.MsgID)

	if m.MsgType != "text" || m.From.UserID == "" {
		return
	}
	s.mu.Lock()
	s.curReqID = reqID
	s.mu.Unlock()
	content := strings.TrimSpace(m.Text.Content)
	if content == "" {
		return
	}
	// strip leading @bot mention in groups
	content = stripWeComMention(content)

	if handled := s.handleCommand(ctx, m.ChatID, m.From.UserID, content, m.ChatType); handled {
		return
	}
	if m.ChatType == "group" && !strings.Contains(m.Text.Content, "@") {
		return
	}
	s.replyToQuestion(ctx, reqID, m.ChatID, m.From.UserID, content)
}

// stripWeComMention removes a leading "@BotName" token if present.
func stripWeComMention(text string) string {
	fields := strings.Fields(text)
	for len(fields) > 0 && strings.HasPrefix(fields[0], "@") {
		fields = fields[1:]
	}
	return strings.Join(fields, " ")
}

func (s *WeComService) handleEvent(raw json.RawMessage) {
	// enter_chat etc. - nothing to do yet
}

// ---------- commands (same semantics as Feishu) ----------

func (s *WeComService) handleCommand(ctx context.Context, chatID, userID, text, chatType string) bool {
	trimmed := strings.TrimSpace(text)
	switch {
	case strings.HasPrefix(trimmed, "绑定"):
		return s.cmdBind(ctx, chatID, userID, strings.TrimSpace(strings.TrimPrefix(trimmed, "绑定")))
	case strings.HasPrefix(trimmed, "批准"):
		return s.cmdApprove(ctx, chatID, userID, strings.TrimSpace(strings.TrimPrefix(trimmed, "批准")))
	case strings.HasPrefix(trimmed, "拒绝"):
		return s.cmdReject(ctx, chatID, userID, strings.TrimSpace(strings.TrimPrefix(trimmed, "拒绝")))
	case strings.HasPrefix(trimmed, "解绑"):
		return s.cmdUnbind(ctx, chatID, userID)
	case strings.HasPrefix(trimmed, "绑定状态") || trimmed == "状态":
		return s.cmdStatus(ctx, chatID, userID)
	}
	return false
}

func (s *WeComService) cmdBind(ctx context.Context, chatID, userID, email string) bool {
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
	var existing models.BotBinding
	if err := database.DB.Where("platform = ? AND open_id = ? AND status = ?", "wecom", userID, models.BindingApproved).
		First(&existing).Error; err == nil {
		s.sendText(ctx, chatID, fmt.Sprintf("你已绑定 %s，如需更换请先发送：解绑", existing.Email))
		return true
	}
	binding := models.BotBinding{
		TenantID: user.TenantID,
		Platform: "wecom",
		OpenID:   userID,
		UserID:   user.ID,
		Email:    user.Email,
		Status:   models.BindingPending,
	}
	if err := database.DB.Where("platform = ? AND open_id = ? AND status = ?", "wecom", userID, models.BindingPending).
		Assign(binding).FirstOrCreate(&binding).Error; err != nil {
		log.Printf("[wecom] bind create error: %v", err)
		s.sendText(ctx, chatID, "绑定请求创建失败，请稍后再试。")
		return true
	}
	s.sendText(ctx, chatID, fmt.Sprintf("已提交绑定申请：%s（%s）。\n请管理员在后台「机器人绑定」页面批准。", user.Name, user.Email))
	return true
}

func (s *WeComService) cmdApprove(ctx context.Context, chatID, userID, email string) bool {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		s.sendText(ctx, chatID, "用法：批准 <邮箱>")
		return true
	}
	if !s.isAdmin(userID) {
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
	approverRole := s.boundRole(userID)
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
		s.sendText(ctx, chatID, "批准失败，请稍后再试。")
		return true
	}
	s.sendText(ctx, chatID, fmt.Sprintf("已批准 %s 的绑定申请。", email))
	return true
}

func (s *WeComService) cmdReject(ctx context.Context, chatID, userID, email string) bool {
	email = strings.ToLower(strings.TrimSpace(email))
	if email == "" {
		s.sendText(ctx, chatID, "用法：拒绝 <邮箱>")
		return true
	}
	if !s.isAdmin(userID) {
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
		s.sendText(ctx, chatID, "拒绝失败，请稍后再试。")
		return true
	}
	s.sendText(ctx, chatID, fmt.Sprintf("已拒绝 %s 的绑定申请。", email))
	return true
}

func (s *WeComService) cmdUnbind(ctx context.Context, chatID, userID string) bool {
	res := database.DB.Where("platform = ? AND open_id = ?", "wecom", userID).Delete(&models.BotBinding{})
	if res.RowsAffected > 0 {
		s.sendText(ctx, chatID, "已解除绑定。之后@我提问前需重新绑定。")
	} else {
		s.sendText(ctx, chatID, "当前没有绑定记录。")
	}
	return true
}

func (s *WeComService) cmdStatus(ctx context.Context, chatID, userID string) bool {
	var binding models.BotBinding
	if err := database.DB.Where("platform = ? AND open_id = ?", "wecom", userID).Order("updated_at desc").First(&binding).Error; err != nil {
		s.sendText(ctx, chatID, "你还没有绑定系统账号。\n请发送：绑定 <系统邮箱>\n然后请管理员在后台批准。")
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
		s.sendText(ctx, chatID, fmt.Sprintf("绑定申请待审批：%s。请管理员在后台批准。", binding.Email))
	default:
		s.sendText(ctx, chatID, fmt.Sprintf("绑定申请已被拒绝：%s。可重新发送：绑定 %s", binding.Email, binding.Email))
	}
	return true
}

func (s *WeComService) isAdmin(userID string) bool {
	role := s.boundRole(userID)
	return role == models.RoleSuperAdmin || role == models.RoleKnowledgeAdmin
}

// boundRole returns the system role bound to the given WeCom user id ("" if none).
func (s *WeComService) boundRole(userID string) string {
	var binding models.BotBinding
	if err := database.DB.Where("platform = ? AND open_id = ? AND status = ?", "wecom", userID, models.BindingApproved).
		First(&binding).Error; err != nil {
		return ""
	}
	var user models.User
	if err := database.DB.First(&user, "id = ?", binding.UserID).Error; err != nil {
		return ""
	}
	return user.Role
}

// ---------- question answering ----------

func (s *WeComService) replyToQuestion(ctx context.Context, reqID, chatID, userID, question string) {
	var binding models.BotBinding
	if err := database.DB.Where("platform = ? AND open_id = ? AND status = ?", "wecom", userID, models.BindingApproved).
		First(&binding).Error; err != nil {
		s.sendText(ctx, chatID, "你尚未绑定系统账号，无法提问。\n请先发送：绑定 <系统邮箱>\n然后请管理员在后台「机器人绑定」页面批准。")
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
		log.Printf("[wecom] ask error (user=%s): %v", user.Email, err)
		s.sendText(ctx, chatID, "抱歉，我暂时无法回答这个问题，请稍后再试。")
		return
	}
	reply := buildReply(result)
	s.sendText(ctx, chatID, reply)
}

// sendText sends a text reply via aibot_respond_msg.
// Per WeCom protocol, headers.req_id must be the req_id of the triggering
// aibot_msg_callback (it correlates the reply to the request).
func (s *WeComService) sendText(ctx context.Context, chatID, text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil || text == "" {
		return
	}
	reqID := s.curReqID
	if reqID == "" {
		reqID = fmt.Sprintf("r_%d", time.Now().UnixNano())
	}
	payload, _ := json.Marshal(map[string]interface{}{
		"cmd": "aibot_respond_msg",
		"headers": map[string]string{
			"req_id": reqID,
		},
		"body": map[string]interface{}{
			"msgtype": "markdown",
			"markdown": map[string]string{
				"content": text,
			},
		},
	})
	_ = s.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	if err := s.conn.WriteMessage(websocket.TextMessage, payload); err != nil {
		log.Printf("[wecom] send error: %v", err)
	}
}
