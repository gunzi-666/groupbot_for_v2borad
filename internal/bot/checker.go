package bot

import (
	"fmt"
	"log/slog"
	"strings"
	"time"

	tele "gopkg.in/telebot.v3"

	"v2board-tg-bot/internal/binding"
	"v2board-tg-bot/internal/config"
	"v2board-tg-bot/internal/db"
)

// Checker 负责定时巡检，踢出过期/封禁的用户
type Checker struct {
	bot       *tele.Bot
	config    *config.Config
	dbClients map[int64]*db.Client
	groups    map[int64]*config.GroupConfig
	bindings  *binding.Store
}

// NewChecker 构造一个 Checker
func NewChecker(b *tele.Bot, cfg *config.Config, dbClients map[int64]*db.Client, bindings *binding.Store) *Checker {
	groups := make(map[int64]*config.GroupConfig)
	for i := range cfg.Groups {
		groups[cfg.Groups[i].ChatID] = &cfg.Groups[i]
	}
	return &Checker{
		bot:       b,
		config:    cfg,
		dbClients: dbClients,
		groups:    groups,
		bindings:  bindings,
	}
}

// Start 启动定时巡检，阻塞运行
func (c *Checker) Start() {
	interval := time.Duration(c.config.CheckInterval) * time.Second
	slog.Info("定时巡检已启动", "interval", interval)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for range ticker.C {
		c.RunCheck()
	}
}

// RunCheck 执行一次巡检
func (c *Checker) RunCheck() {
	slog.Info("开始巡检...")
	totalKicked := 0

	for chatID, client := range c.dbClients {
		group := c.groups[chatID]
		totalKicked += c.checkGroup(chatID, client, group)
	}

	slog.Info("巡检完成", "total_kicked", totalKicked)
}

func (c *Checker) checkGroup(chatID int64, client *db.Client, group *config.GroupConfig) int {
	expiredUsers := make(map[int64]string)

	// 来源1：数据库中绑定了 telegram_id 的过期用户
	dbExpired, err := client.GetExpiredTelegramUsers()
	if err != nil {
		slog.Error("查询数据库过期用户失败", "chat_id", chatID, "error", err)
	} else {
		for tgID, email := range dbExpired {
			expiredUsers[tgID] = email
		}
	}

	// 来源2：本地绑定文件中的用户，逐个检查是否过期
	// 同时处理 DB 过期但本地绑定有效的情况（用户可能换了账号绑定）
	dbName := group.Database.DBName
	boundUsers := c.bindings.GetAllForDB(dbName)
	for tgID, email := range boundUsers {
		user, err := client.FindUserByEmail(email)
		if err != nil {
			continue
		}
		if user != nil && db.IsUserValid(user) {
			delete(expiredUsers, tgID)
		} else if _, already := expiredUsers[tgID]; !already {
			expiredUsers[tgID] = email
		}
	}

	if len(expiredUsers) == 0 {
		slog.Debug("无过期用户", "chat_id", chatID)
		return 0
	}

	chat := &tele.Chat{ID: chatID}
	kicked := 0
	var kickedLines []string

	for tgID, email := range expiredUsers {
		if group.IsExempt(tgID) {
			continue
		}

		tgUser := &tele.User{ID: tgID}
		member, err := c.bot.ChatMemberOf(chat, tgUser)
		if err != nil {
			continue
		}

		if member.Role == tele.Left {
			continue
		}
		if member.Role == tele.Kicked {
			slog.Debug("用户已被其他来源封禁，跳过", "user_id", tgID)
			continue
		}

		name := fmt.Sprintf("用户%d", tgID)
		if member.User != nil {
			name = displayName(member.User)
		}

		err = c.bot.Ban(chat, &tele.ChatMember{
			User:            tgUser,
			RestrictedUntil: time.Now().Add(60 * time.Second).Unix(),
		})
		if err != nil {
			slog.Error("踢出用户失败", "user_id", tgID, "email", email, "error", err)
			continue
		}

		time.Sleep(500 * time.Millisecond)
		if err := c.bot.Unban(chat, tgUser); err != nil {
			slog.Warn("Unban失败，重试一次", "user_id", tgID, "error", err)
			time.Sleep(1 * time.Second)
			if err := c.bot.Unban(chat, tgUser); err != nil {
				slog.Error("Unban再次失败，将在60秒后自动解除", "user_id", tgID, "error", err)
			}
		}

		kicked++
		kickedLines = append(kickedLines, fmt.Sprintf("• %s 因套餐过期被移除", name))
		slog.Info("已踢出过期用户", "chat_id", chatID, "user_id", tgID, "email", email)

		_, _ = c.bot.Send(tgUser,
			"⚠️ 您已被移出群组\n原因：套餐已过期\n\n请前往官网续费后重新申请加入。",
		)

		time.Sleep(500 * time.Millisecond)
	}

	// 在群组中发送踢出通知，合并为一条消息，注意 TG 4096 字符限制
	if len(kickedLines) > 0 {
		const maxLen = 4096 - 10
		var batch []string
		currentLen := 0

		for _, line := range kickedLines {
			lineLen := len([]rune(line)) + 1
			if currentLen+lineLen > maxLen && len(batch) > 0 {
				_, _ = c.bot.Send(chat, strings.Join(batch, "\n"))
				batch = batch[:0]
				currentLen = 0
				time.Sleep(500 * time.Millisecond)
			}
			batch = append(batch, line)
			currentLen += lineLen
		}
		if len(batch) > 0 {
			_, _ = c.bot.Send(chat, strings.Join(batch, "\n"))
		}
	}

	return kicked
}
