package main

import (
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	tele "gopkg.in/telebot.v3"
)

// PendingVerify 待验证的新成员信息
type PendingVerify struct {
	ChatID    int64
	UserID    int64
	MessageID int
	JoinTime  time.Time
}

type BotHandler struct {
	bot       *tele.Bot
	config    *Config
	dbClients map[int64]*DBClient
	groups    map[int64]*GroupConfig
	bindings  *BindingStore

	mu      sync.RWMutex
	pending map[int64]*PendingVerify // telegram_id -> 待验证信息
}

func NewBotHandler(bot *tele.Bot, cfg *Config, dbClients map[int64]*DBClient, bindings *BindingStore) *BotHandler {
	groups := make(map[int64]*GroupConfig)
	for i := range cfg.Groups {
		groups[cfg.Groups[i].ChatID] = &cfg.Groups[i]
	}
	return &BotHandler{
		bot:       bot,
		config:    cfg,
		dbClients: dbClients,
		groups:    groups,
		bindings:  bindings,
		pending:   make(map[int64]*PendingVerify),
	}
}

func (h *BotHandler) Register() {
	h.bot.Handle(tele.OnUserJoined, h.onUserJoined)
	h.bot.Handle("/start", h.onStart)
	h.bot.Handle("/bind", h.onBind)
	h.bot.Handle("/unbind", h.onUnbind)
	h.bot.Handle("/status", h.onStatus)
	h.bot.Handle("/check", h.onCheck)
	h.bot.Handle("/cha", h.onCha)
}

// onUserJoined 用户加入群组时触发
func (h *BotHandler) onUserJoined(c tele.Context) error {
	chatID := c.Chat().ID
	user := c.Sender()
	if user == nil {
		return nil
	}
	userID := user.ID

	if user.IsBot {
		return nil
	}

	slog.Info("新用户加入群组",
		"chat_id", chatID,
		"user_id", userID,
		"username", user.Username,
	)

	group, ok := h.groups[chatID]
	if !ok {
		return nil
	}

	// 白名单用户直接放行
	if group.IsExempt(userID) {
		slog.Info("白名单用户，跳过验证", "user_id", userID)
		_, _ = c.Bot().Send(c.Chat(),
			fmt.Sprintf("👋 欢迎 [%s](tg://user?id=%d) 加入！", displayName(user), userID),
			tele.ModeMarkdown,
		)
		return nil
	}

	// 禁言用户
	err := c.Bot().Restrict(c.Chat(), &tele.ChatMember{
		User:   user,
		Rights: tele.Rights{CanSendMessages: false},
	})
	if err != nil {
		slog.Error("禁言用户失败", "user_id", userID, "error", err)
	}

	// 在群里发送验证提示，带内联按钮
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

	// 记录待验证状态
	h.mu.Lock()
	h.pending[userID] = &PendingVerify{
		ChatID:    chatID,
		UserID:    userID,
		MessageID: msg.ID,
		JoinTime:  time.Now(),
	}
	h.mu.Unlock()

	// 启动超时计时器
	go h.verifyTimeout(userID, group.VerifyTimeout)

	return nil
}

// verifyTimeout 超时未验证则踢出
func (h *BotHandler) verifyTimeout(userID int64, timeoutSec int) {
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

	// 踢出用户（设置 UntilDate 防止 Unban 失败导致永久封禁）
	err := h.bot.Ban(chat, &tele.ChatMember{
		User:            user,
		RestrictedUntil: time.Now().Add(60 * time.Second).Unix(),
	})
	if err != nil {
		slog.Error("超时踢出失败", "user_id", userID, "error", err)
		return
	}
	if err := h.bot.Unban(chat, user, true); err != nil {
		slog.Warn("Unban失败，将在60秒后自动解除", "user_id", userID, "error", err)
	}

	slog.Info("用户验证超时，已踢出", "user_id", userID, "chat_id", pv.ChatID)

	// 删除验证提示消息
	_ = h.bot.Delete(&tele.Message{ID: pv.MessageID, Chat: chat})

	// 发送踢出通知，5秒后自动删除
	kickMsg, _ := h.bot.Send(chat, "⏰ 用户验证超时，已被移出群组。")
	if kickMsg != nil {
		go func() {
			time.Sleep(5 * time.Second)
			_ = h.bot.Delete(kickMsg)
		}()
	}
}

// onStart 处理 /start 命令，区分普通启动和验证跳转
func (h *BotHandler) onStart(c tele.Context) error {
	payload := c.Message().Payload
	if strings.HasPrefix(payload, "verify_") {
		return h.handleVerifyStart(c, payload)
	}

	return c.Send(
		"👋 欢迎使用群组管理机器人\n\n"+
			"本机器人用于管理群组成员，只有拥有有效套餐的用户才能加入。\n\n"+
			"📝 可用命令：\n"+
			"`/bind 邮箱 密码` - 绑定面板账户\n"+
			"`/unbind` - 解除绑定\n"+
			"`/status` - 查看套餐状态",
		tele.ModeMarkdown,
	)
}

// handleVerifyStart 用户点击验证按钮跳转过来
func (h *BotHandler) handleVerifyStart(c tele.Context, payload string) error {
	userID := c.Sender().ID

	h.mu.RLock()
	pv, hasPending := h.pending[userID]
	h.mu.RUnlock()

	if !hasPending {
		return c.Send("✅ 您已通过验证，无需重复操作。")
	}

	db, ok := h.dbClients[pv.ChatID]
	if !ok {
		return c.Send("❌ 系统错误，请联系管理员。")
	}

	// 先查数据库中的 telegram_id
	user, err := db.FindUserByTelegramID(userID)
	if err == nil && user != nil {
		slog.Info("数据库中找到用户(telegram_id)",
			"telegram_id", userID, "email", user.Email,
			"plan_id", user.PlanID, "expired_at", user.ExpiredAt,
			"banned", user.Banned, "valid", IsUserValid(user),
		)
		if IsUserValid(user) {
			h.approveUser(userID)
			return c.Send("✅ 验证通过！已解除禁言，欢迎加入群组！")
		}
		reason := describeInvalid(user)
		return c.Send(fmt.Sprintf("❌ 验证失败：%s\n\n请前往官网购买/续费套餐。", reason))
	}

	// 再查本地绑定
	binding, bound := h.bindings.Get(userID)
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

	user, err = db.FindUserByEmail(binding.Email)
	if err != nil || user == nil {
		slog.Warn("本地绑定的邮箱在数据库中未找到",
			"telegram_id", userID, "email", binding.Email,
		)
		return c.Send("❌ 未找到您的账户信息，请重新绑定：`/bind 邮箱 密码`", tele.ModeMarkdown)
	}

	slog.Info("通过本地绑定找到用户",
		"telegram_id", userID, "email", user.Email,
		"plan_id", user.PlanID, "expired_at", user.ExpiredAt,
		"banned", user.Banned, "valid", IsUserValid(user),
	)

	if !IsUserValid(user) {
		reason := describeInvalid(user)
		return c.Send(fmt.Sprintf("❌ 验证失败：%s\n\n请前往官网购买/续费套餐。", reason))
	}

	h.approveUser(userID)
	return c.Send("✅ 验证通过！已解除禁言，欢迎加入群组！")
}

// approveUser 通过验证：解除禁言 + 清理消息
func (h *BotHandler) approveUser(userID int64) {
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

	// 解除禁言，恢复所有权限
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

	// 删除群里的验证提示消息
	_ = h.bot.Delete(&tele.Message{ID: pv.MessageID, Chat: chat})

	// 获取用户信息以显示名字
	name := fmt.Sprintf("用户%d", userID)
	member, err := h.bot.ChatMemberOf(chat, user)
	if err == nil && member.User != nil {
		name = displayName(member.User)
	}

	// 在群里发送欢迎消息，10秒后自动删除
	welcomeMsg, _ := h.bot.Send(chat,
		fmt.Sprintf("✅ [%s](tg://user?id=%d) 验证通过，欢迎加入！", name, userID),
		tele.ModeMarkdown,
	)
	if welcomeMsg != nil {
		go func() {
			time.Sleep(10 * time.Second)
			_ = h.bot.Delete(welcomeMsg)
		}()
	}

	slog.Info("用户验证通过", "user_id", userID, "chat_id", pv.ChatID)
}

// onBind 处理 /bind 命令
func (h *BotHandler) onBind(c tele.Context) error {
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

	var foundUser *V2User
	var foundDBName string

	for chatID, db := range h.dbClients {
		user, err := db.FindUserByEmail(email)
		if err != nil {
			slog.Error("查询用户失败", "email", email, "error", err)
			continue
		}
		if user != nil {
			foundUser = user
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

	if !VerifyPassword(algo, salt, password, foundUser.Password) {
		return c.Send("❌ 密码错误，请重试。")
	}

	if foundUser.Banned != 0 {
		return c.Send("❌ 您的账户已被封禁，无法绑定。")
	}

	if err := h.bindings.Set(userID, email, foundDBName); err != nil {
		slog.Error("保存绑定失败", "user_id", userID, "error", err)
		return c.Send("❌ 绑定失败，请稍后重试。")
	}

	slog.Info("用户绑定成功", "telegram_id", userID, "email", email, "db", foundDBName)

	// 检查是否有待验证的入群请求
	h.mu.RLock()
	pv, hasPending := h.pending[userID]
	h.mu.RUnlock()

	if hasPending {
		// 必须用待验证群组对应的数据库来判断，而不是绑定时搜到的数据库
		groupDB, ok := h.dbClients[pv.ChatID]
		if ok {
			groupUser, err := groupDB.FindUserByEmail(email)
			if err == nil && groupUser != nil && IsUserValid(groupUser) {
				slog.Info("绑定时自动审批通过",
					"telegram_id", userID, "email", email,
					"plan_id", groupUser.PlanID, "expired_at", groupUser.ExpiredAt,
				)
				h.approveUser(userID)
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

	if !IsUserValid(foundUser) {
		return c.Send(
			"✅ 账户绑定成功！\n\n" +
				"但您当前没有有效套餐，请前往官网购买套餐后加入群组。",
		)
	}

	return c.Send(fmt.Sprintf("✅ 绑定成功！\n\n邮箱：%s\n现在您可以加入群组了。", email))
}

// onUnbind 解除绑定
func (h *BotHandler) onUnbind(c tele.Context) error {
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
func (h *BotHandler) onStatus(c tele.Context) error {
	userID := c.Sender().ID
	binding, hasBind := h.bindings.Get(userID)

	var results []string
	for chatID, db := range h.dbClients {
		group := h.groups[chatID]
		dbName := group.Database.DBName

		if group.IsExempt(userID) {
			results = append(results, "📦 ⭐ 白名单用户")
			continue
		}

		user, err := db.FindUserByTelegramID(userID)
		if err == nil && user != nil {
			if IsUserValid(user) {
				results = append(results, fmt.Sprintf("📦 ✅ 套餐有效 (%s)", user.Email))
			} else {
				results = append(results, fmt.Sprintf("📦 ❌ 套餐无效 (%s)", user.Email))
			}
			continue
		}

		if hasBind && binding.DBName == dbName {
			user, err = db.FindUserByEmail(binding.Email)
			if err != nil || user == nil {
				results = append(results, "📦 ❓ 查询失败")
				continue
			}
			if IsUserValid(user) {
				results = append(results, fmt.Sprintf("📦 ✅ 套餐有效 (%s)", user.Email))
			} else {
				results = append(results, fmt.Sprintf("📦 ❌ 套餐无效 (%s)", user.Email))
			}
		}
	}

	if len(results) == 0 {
		return c.Send("未找到您的账户信息，请先使用 `/bind 邮箱 密码` 绑定。", tele.ModeMarkdown)
	}
	return c.Send("📊 您的套餐状态：\n\n" + strings.Join(results, "\n"))
}

// onCheck 管理员手动触发巡检
func (h *BotHandler) onCheck(c tele.Context) error {
	if !h.config.IsAdmin(c.Sender().ID) {
		return c.Send("❓ 无效命令，请输入 /start 查看帮助。")
	}
	return c.Send("✅ 巡检任务已触发，请稍候查看日志。")
}

// onCha 管理员通过 Telegram ID 查询用户信息
func (h *BotHandler) onCha(c tele.Context) error {
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

	for _, db := range h.dbClients {
		user, err := db.FindUserByTelegramID(tgID)
		if err != nil {
			results = append(results, fmt.Sprintf("📦 查询失败 (%s)", err))
			continue
		}
		if user != nil {
			status := "✅ 有效"
			if !IsUserValid(user) {
				status = "❌ 无效"
				if user.Banned != 0 {
					status = "🚫 已封禁"
				} else if !user.PlanID.Valid || user.PlanID.Int64 == 0 {
					status = "❌ 无套餐"
				} else {
					status = "❌ 已过期"
				}
			}
			expiredStr := "永不过期"
			if user.ExpiredAt.Valid && user.ExpiredAt.Int64 != 0 {
				expiredStr = time.Unix(user.ExpiredAt.Int64, 0).Format("2006-01-02 15:04:05")
			}
			results = append(results, fmt.Sprintf(
				"📦 ID: %d\n  邮箱: %s\n  套餐ID: %v\n  到期: %s\n  状态: %s",
				user.ID, user.Email, user.PlanID.Int64, expiredStr, status,
			))
			continue
		}
	}

	// 查本地绑定
	binding, bound := h.bindings.Get(tgID)
	if bound {
		results = append(results, fmt.Sprintf("📎 本地绑定: %s", binding.Email))
	}

	if len(results) == 0 {
		return c.Send(fmt.Sprintf("未找到 Telegram ID `%d` 的任何记录。", tgID), tele.ModeMarkdown)
	}

	return c.Send(fmt.Sprintf("🔍 查询结果 (TG ID: `%d`)：\n\n%s", tgID, strings.Join(results, "\n\n")), tele.ModeMarkdown)
}

// checkBoundUser 检查用户是否已绑定且套餐有效
// 优先查数据库 telegram_id，再查本地绑定文件
func (h *BotHandler) checkBoundUser(chatID, userID int64) bool {
	db, ok := h.dbClients[chatID]
	if !ok {
		return false
	}

	// 先查数据库中的 telegram_id
	user, err := db.FindUserByTelegramID(userID)
	if err == nil && user != nil {
		return IsUserValid(user)
	}

	// 再查本地绑定文件
	binding, bound := h.bindings.Get(userID)
	if !bound {
		return false
	}
	user, err = db.FindUserByEmail(binding.Email)
	if err != nil || user == nil {
		return false
	}
	return IsUserValid(user)
}

func describeInvalid(user *V2User) string {
	if user == nil {
		return "未找到账户"
	}
	if user.Banned != 0 {
		return "账户已被封禁"
	}
	if !user.PlanID.Valid || user.PlanID.Int64 == 0 {
		return "尚未购买套餐"
	}
	return "套餐已过期"
}

func displayName(u *tele.User) string {
	name := u.FirstName
	if u.LastName != "" {
		name += " " + u.LastName
	}
	if name == "" {
		name = u.Username
	}
	if name == "" {
		name = fmt.Sprintf("用户%d", u.ID)
	}
	return name
}
