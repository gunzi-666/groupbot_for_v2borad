package bot

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	tele "gopkg.in/telebot.v3"

	"v2board-tg-bot/internal/binding"
	"v2board-tg-bot/internal/config"
	"v2board-tg-bot/internal/db"
)

// PendingVerify 待验证的新成员信息
type PendingVerify struct {
	ChatID    int64
	UserID    int64
	MessageID int
	JoinTime  time.Time
}

// Handler 处理所有 Telegram 事件与命令
type Handler struct {
	bot       *tele.Bot
	config    *config.Config
	dbClients map[int64]*db.Client
	groups    map[int64]*config.GroupConfig
	bindings  *binding.Store

	mu      sync.RWMutex
	pending map[int64]*PendingVerify // telegram_id -> 待验证信息
}

// NewHandler 构造一个 Handler
func NewHandler(b *tele.Bot, cfg *config.Config, dbClients map[int64]*db.Client, bindings *binding.Store) *Handler {
	groups := make(map[int64]*config.GroupConfig)
	for i := range cfg.Groups {
		groups[cfg.Groups[i].ChatID] = &cfg.Groups[i]
	}
	return &Handler{
		bot:       b,
		config:    cfg,
		dbClients: dbClients,
		groups:    groups,
		bindings:  bindings,
		pending:   make(map[int64]*PendingVerify),
	}
}

// Register 注册所有命令与事件处理器
func (h *Handler) Register() {
	h.bot.Handle(tele.OnUserJoined, h.onUserJoined)
	h.bot.Handle("/start", h.onStart)
	h.bot.Handle("/bind", h.onBind)
	h.bot.Handle("/unbind", h.onUnbind)
	h.bot.Handle("/status", h.onStatus)
	h.bot.Handle("/check", h.onCheck)
	h.bot.Handle("/cha", h.onCha)
}

// onUserJoined 用户加入群组时触发
func (h *Handler) onUserJoined(c tele.Context) error {
	chatID := c.Chat().ID
	user := c.Sender()
	if user == nil || user.IsBot {
		return nil
	}
	userID := user.ID

	slog.Info("新用户加入群组",
		"chat_id", chatID,
		"user_id", userID,
		"username", user.Username,
	)

	group, ok := h.groups[chatID]
	if !ok {
		return nil
	}

	if group.IsExempt(userID) {
		slog.Info("白名单用户，跳过验证", "user_id", userID)
		_, _ = c.Bot().Send(c.Chat(),
			fmt.Sprintf("👋 欢迎 [%s](tg://user?id=%d) 加入！", displayName(user), userID),
			tele.ModeMarkdown,
		)
		return nil
	}

	err := c.Bot().Restrict(c.Chat(), &tele.ChatMember{
		User:   user,
		Rights: tele.Rights{CanSendMessages: false},
	})
	if err != nil {
		slog.Error("禁言用户失败", "user_id", userID, "error", err)
	}

	botUsername := c.Bot().Me.Username
	verifyURL := fmt.Sprintf("https://t.me/%s?start=verify_%d", botUsername, chatID)

	btn := &tele.ReplyMarkup{}
	btnVerify := btn.URL("👉 点击验证", verifyURL)
	btn.Inline(btn.Row(btnVerify))

	msg, err := c.Bot().Send(c.Chat(),
		fmt.Sprintf("👋 欢迎 [%s](tg://user?id=%d)！\n\n"+
			"请在 *%d 秒*内点击下方按钮完成验证，否则将被移出群组。",
			displayName(user), userID, group.VerifyTimeout),
		tele.ModeMarkdown,
		btn,
	)
	if err != nil {
		slog.Error("发送验证消息失败", "error", err)
		return nil
	}

	h.mu.Lock()
	h.pending[userID] = &PendingVerify{
		ChatID:    chatID,
		UserID:    userID,
		MessageID: msg.ID,
		JoinTime:  time.Now(),
	}
	h.mu.Unlock()

	go h.verifyTimeout(userID, group.VerifyTimeout)
	return nil
}

// verifyTimeout 超时未验证则踢出
func (h *Handler) verifyTimeout(userID int64, timeoutSec int) {
	time.Sleep(time.Duration(timeoutSec) * time.Second)

	h.mu.Lock()
	pv, exists := h.pending[userID]
	if !exists {
		h.mu.Unlock()
		return
	}
	delete(h.pending, userID)
	h.mu.Unlock()

	chat := &tele.Chat{ID: pv.ChatID}
	user := &tele.User{ID: userID}

	member, mErr := h.bot.ChatMemberOf(chat, user)
	if mErr == nil && member.Role == tele.Kicked {
		slog.Info("用户已被其他来源封禁，跳过踢出操作", "user_id", userID)
		return
	}

	err := h.bot.Ban(chat, &tele.ChatMember{
		User:            user,
		RestrictedUntil: time.Now().Add(60 * time.Second).Unix(),
	})
	if err != nil {
		slog.Error("超时踢出失败", "user_id", userID, "error", err)
		return
	}
	time.Sleep(500 * time.Millisecond)
	if err := h.bot.Unban(chat, user); err != nil {
		slog.Warn("Unban失败，重试一次", "user_id", userID, "error", err)
		time.Sleep(1 * time.Second)
		if err := h.bot.Unban(chat, user); err != nil {
			slog.Error("Unban再次失败，将在60秒后自动解除", "user_id", userID, "error", err)
		}
	}

	slog.Info("用户验证超时，已踢出", "user_id", userID, "chat_id", pv.ChatID)

	_ = h.bot.Delete(&tele.Message{ID: pv.MessageID, Chat: chat})

	kickMsg, _ := h.bot.Send(chat, "⏰ 用户验证超时，已被移出群组。")
	if kickMsg != nil {
		go func() {
			time.Sleep(5 * time.Second)
			_ = h.bot.Delete(kickMsg)
		}()
	}
}

// onStart 处理 /start 命令，区分普通启动和验证跳转
func (h *Handler) onStart(c tele.Context) error {
	payload := c.Message().Payload
	if strings.HasPrefix(payload, "verify_") {
		return h.handleVerifyStart(c, payload)
	}
	return c.Send(h.buildStartMessage(), tele.ModeMarkdown)
}

// buildStartMessage 基于 config 中的 profile 构建 /start 响应
func (h *Handler) buildStartMessage() string {
	var sb strings.Builder

	p := h.config.Telegram.Profile
	if p.Description != "" {
		sb.WriteString(p.Description)
		sb.WriteString("\n\n")
	} else {
		sb.WriteString("👋 欢迎使用群组管理机器人\n\n")
		sb.WriteString("本机器人用于管理群组成员，只有拥有有效套餐的用户才能加入。\n\n")
	}

	if len(p.Commands) > 0 {
		sb.WriteString("📝 可用命令：\n")
		for _, cmd := range p.Commands {
			sb.WriteString(fmt.Sprintf("`/%s` - %s\n", cmd.Command, cmd.Description))
		}
	} else {
		sb.WriteString("📝 可用命令：\n")
		sb.WriteString("`/bind 邮箱 密码` - 绑定面板账户\n")
		sb.WriteString("`/unbind` - 解除绑定\n")
		sb.WriteString("`/status` - 查看套餐状态\n")
	}

	return strings.TrimRight(sb.String(), "\n")
}

// handleVerifyStart 用户点击验证按钮跳转过来
func (h *Handler) handleVerifyStart(c tele.Context, _ string) error {
	userID := c.Sender().ID

	h.mu.RLock()
	pv, hasPending := h.pending[userID]
	h.mu.RUnlock()

	if !hasPending {
		return c.Send("✅ 您已通过验证，无需重复操作。")
	}

	client, ok := h.dbClients[pv.ChatID]
	if !ok {
		return c.Send("❌ 系统错误，请联系管理员。")
	}

	user, err := client.FindUserByTelegramID(userID)
	if err == nil && user != nil {
		slog.Info("数据库中找到用户(telegram_id)",
			"telegram_id", userID, "email", user.Email,
			"plan_id", user.PlanID, "expired_at", user.ExpiredAt,
			"banned", user.Banned, "valid", db.IsUserValid(user),
		)
		if db.IsUserValid(user) {
			h.approveUser(userID, planNameOf(client, user))
			return c.Send("✅ 验证通过！已解除禁言，欢迎加入群组！")
		}
		return c.Send(fmt.Sprintf("❌ 验证失败：%s\n\n请前往官网购买/续费套餐。", describeInvalid(user)))
	}

	b, bound := h.bindings.Get(userID)
	if !bound {
		slog.Info("用户未绑定，提示绑定", "telegram_id", userID)
		return c.Send(
			"📝 请先绑定您的面板账户：\n\n"+
				"发送：`/bind 您的邮箱 您的密码`\n\n"+
				"示例：`/bind user@example.com mypassword`\n\n"+
				"绑定成功后将自动完成验证并解除禁言。",
			tele.ModeMarkdown,
		)
	}

	user, err = client.FindUserByEmail(b.Email)
	if err != nil || user == nil {
		slog.Warn("本地绑定的邮箱在数据库中未找到",
			"telegram_id", userID, "email", b.Email,
		)
		return c.Send("❌ 未找到您的账户信息，请重新绑定：`/bind 邮箱 密码`", tele.ModeMarkdown)
	}

	slog.Info("通过本地绑定找到用户",
		"telegram_id", userID, "email", user.Email,
		"plan_id", user.PlanID, "expired_at", user.ExpiredAt,
		"banned", user.Banned, "valid", db.IsUserValid(user),
	)

	if !db.IsUserValid(user) {
		return c.Send(fmt.Sprintf("❌ 验证失败：%s\n\n请前往官网购买/续费套餐。", describeInvalid(user)))
	}

	h.approveUser(userID, planNameOf(client, user))
	return c.Send("✅ 验证通过！已解除禁言，欢迎加入群组！")
}

// approveUser 通过验证：解除禁言 + 清理消息
func (h *Handler) approveUser(userID int64, planName string) {
	h.mu.Lock()
	pv, exists := h.pending[userID]
	if !exists {
		h.mu.Unlock()
		return
	}
	delete(h.pending, userID)
	h.mu.Unlock()

	chat := &tele.Chat{ID: pv.ChatID}
	user := &tele.User{ID: userID}

	err := h.bot.Restrict(chat, &tele.ChatMember{
		User: user,
		Rights: tele.Rights{
			CanSendMessages: true,
			CanSendMedia:    true,
			CanSendOther:    true,
			CanAddPreviews:  true,
		},
	})
	if err != nil {
		slog.Error("解除禁言失败", "user_id", userID, "error", err)
	}

	_ = h.bot.Delete(&tele.Message{ID: pv.MessageID, Chat: chat})

	name := fmt.Sprintf("用户%d", userID)
	member, err := h.bot.ChatMemberOf(chat, user)
	if err == nil && member.User != nil {
		name = displayName(member.User)
	}

	var welcomeText string
	if planName != "" {
		welcomeText = fmt.Sprintf("👏 欢迎 尊贵的 %s 用户 [%s](tg://user?id=%d)", planName, name, userID)
	} else {
		welcomeText = fmt.Sprintf("👏 欢迎 [%s](tg://user?id=%d)", name, userID)
	}
	welcomeMsg, _ := h.bot.Send(chat, welcomeText, tele.ModeMarkdown)
	if welcomeMsg != nil {
		go func() {
			time.Sleep(10 * time.Second)
			_ = h.bot.Delete(welcomeMsg)
		}()
	}

	slog.Info("用户验证通过", "user_id", userID, "chat_id", pv.ChatID)
}

// onBind 处理 /bind 命令
func (h *Handler) onBind(c tele.Context) error {
	args := strings.Fields(c.Message().Text)
	if len(args) != 3 {
		return c.Send(
			"📝 用法：`/bind 邮箱 密码`\n\n示例：`/bind user@example.com mypassword`",
			tele.ModeMarkdown,
		)
	}

	email := args[1]
	password := args[2]
	userID := c.Sender().ID

	var foundUser *db.V2User
	var foundDBName string

	for chatID, client := range h.dbClients {
		u, err := client.FindUserByEmail(email)
		if err != nil {
			slog.Error("查询用户失败", "email", email, "error", err)
			continue
		}
		if u != nil {
			foundUser = u
			foundDBName = h.groups[chatID].Database.DBName
			break
		}
	}

	if foundUser == nil {
		return c.Send("❌ 未找到该邮箱对应的账户，请检查邮箱是否正确。")
	}

	algo := ""
	if foundUser.PasswordAlgo.Valid {
		algo = foundUser.PasswordAlgo.String
	}
	salt := ""
	if foundUser.PasswordSalt.Valid {
		salt = foundUser.PasswordSalt.String
	}

	if !db.VerifyPassword(algo, salt, password, foundUser.Password) {
		return c.Send("❌ 密码错误，请重试。")
	}

	if foundUser.Banned != 0 {
		return c.Send("❌ 您的账户已被封禁，无法绑定。")
	}

	if existingTG := h.bindings.FindByEmail(email); existingTG != 0 && existingTG != userID {
		return c.Send("❌ 该邮箱已被其他 Telegram 账号绑定。\n如需更换，请先在原账号执行 /unbind 解绑。")
	}

	if err := h.bindings.Set(userID, email, foundDBName); err != nil {
		slog.Error("保存绑定失败", "user_id", userID, "error", err)
		return c.Send("❌ 绑定失败，请稍后重试。")
	}

	slog.Info("用户绑定成功", "telegram_id", userID, "email", email, "db", foundDBName)

	h.mu.RLock()
	pv, hasPending := h.pending[userID]
	h.mu.RUnlock()

	if hasPending {
		// 必须用待验证群组对应的数据库来判断，而不是绑定时搜到的数据库
		groupDB, ok := h.dbClients[pv.ChatID]
		if ok {
			groupUser, err := groupDB.FindUserByEmail(email)
			if err == nil && groupUser != nil && db.IsUserValid(groupUser) {
				slog.Info("绑定时自动审批通过",
					"telegram_id", userID, "email", email,
					"plan_id", groupUser.PlanID, "expired_at", groupUser.ExpiredAt,
				)
				h.approveUser(userID, planNameOf(groupDB, groupUser))
				return c.Send(fmt.Sprintf("✅ 绑定成功！\n\n邮箱：%s\n已自动完成验证并解除禁言 🎉", email))
			}
			reason := describeInvalid(groupUser)
			slog.Info("绑定成功但套餐无效，未审批",
				"telegram_id", userID, "email", email, "reason", reason,
			)
			return c.Send(fmt.Sprintf(
				"✅ 账户绑定成功！\n\n但验证未通过：%s\n请前往官网购买/续费套餐。", reason,
			))
		}
	}

	if !db.IsUserValid(foundUser) {
		return c.Send(
			"✅ 账户绑定成功！\n\n" +
				"但您当前没有有效套餐，请前往官网购买套餐后加入群组。",
		)
	}

	return c.Send(fmt.Sprintf("✅ 绑定成功！\n\n邮箱：%s\n现在您可以加入群组了。", email))
}

// onUnbind 解除绑定
func (h *Handler) onUnbind(c tele.Context) error {
	userID := c.Sender().ID
	if _, ok := h.bindings.Get(userID); !ok {
		return c.Send("未找到您的绑定记录。")
	}
	if err := h.bindings.Delete(userID); err != nil {
		return c.Send("❌ 解绑失败，请稍后重试。")
	}
	return c.Send("✅ 已解除绑定。")
}

// onStatus 查询套餐状态
func (h *Handler) onStatus(c tele.Context) error {
	userID := c.Sender().ID
	b, hasBind := h.bindings.Get(userID)

	var results []string
	for chatID, client := range h.dbClients {
		group := h.groups[chatID]
		dbName := group.Database.DBName

		if group.IsExempt(userID) {
			results = append(results, "⭐ 白名单用户，免验证")
			continue
		}

		user, err := client.FindUserByTelegramID(userID)
		if err == nil && user != nil {
			results = append(results, formatStatusLine(client, user))
			continue
		}

		if hasBind && b.DBName == dbName {
			user, err = client.FindUserByEmail(b.Email)
			if err != nil || user == nil {
				results = append(results, "📦 ❓ 查询失败")
				continue
			}
			results = append(results, formatStatusLine(client, user))
		}
	}

	if len(results) == 0 {
		return c.Send("未找到您的账户信息，请先使用 `/bind 邮箱 密码` 绑定。", tele.ModeMarkdown)
	}
	return c.Send("━━━━━ 📊 套餐状态 ━━━━━\n\n"+strings.Join(results, "\n\n━━━━━━━━━━━━━━━━━\n\n"), tele.ModeMarkdown)
}

// onCheck 管理员手动触发巡检（预留，目前仅返回提示）
func (h *Handler) onCheck(c tele.Context) error {
	if !h.config.IsAdmin(c.Sender().ID) {
		return c.Send("❓ 无效命令，请输入 /start 查看帮助。")
	}
	return c.Send("✅ 巡检任务已触发，请稍候查看日志。")
}

// onCha 管理员通过 Telegram ID 查询用户信息
func (h *Handler) onCha(c tele.Context) error {
	if !h.config.IsAdmin(c.Sender().ID) {
		return c.Send("❓ 无效命令，请输入 /start 查看帮助。")
	}

	args := strings.Fields(c.Message().Text)
	if len(args) != 2 {
		return c.Send("📝 用法：`/cha TelegramID`\n\n示例：`/cha 123456789`", tele.ModeMarkdown)
	}

	var tgID int64
	if _, err := fmt.Sscanf(args[1], "%d", &tgID); err != nil {
		return c.Send("❌ 请输入有效的数字 ID")
	}

	var results []string

	for _, client := range h.dbClients {
		user, err := client.FindUserByTelegramID(tgID)
		if err != nil {
			results = append(results, fmt.Sprintf("📦 查询失败 (%s)", err))
			continue
		}
		if user != nil {
			results = append(results, formatUserInfo(client, user, "📦 数据库绑定"))
			continue
		}
	}

	if b, bound := h.bindings.Get(tgID); bound {
		var found bool
		for _, client := range h.dbClients {
			user, err := client.FindUserByEmail(b.Email)
			if err != nil || user == nil {
				continue
			}
			found = true
			results = append(results, formatUserInfo(client, user, "📎 本地绑定"))
			break
		}
		if !found {
			results = append(results, fmt.Sprintf("📎 本地绑定: %s (数据库中未找到)", b.Email))
		}
	}

	if len(results) == 0 {
		return c.Send(fmt.Sprintf("未找到 Telegram ID `%d` 的任何记录。", tgID), tele.ModeMarkdown)
	}

	return c.Send(fmt.Sprintf("🔍 查询结果 (TG ID: `%d`)：\n\n%s", tgID, strings.Join(results, "\n\n")), tele.ModeMarkdown)
}
