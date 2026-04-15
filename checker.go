package main

import (
	"log/slog"
	"time"

	tele "gopkg.in/telebot.v3"
)

type Checker struct {
	bot       *tele.Bot
	config    *Config
	dbClients map[int64]*DBClient
	groups    map[int64]*GroupConfig
	bindings  *BindingStore
}

func NewChecker(bot *tele.Bot, cfg *Config, dbClients map[int64]*DBClient, bindings *BindingStore) *Checker {
	groups := make(map[int64]*GroupConfig)
	for i := range cfg.Groups {
		groups[cfg.Groups[i].ChatID] = &cfg.Groups[i]
	}
	return &Checker{
		bot:       bot,
		config:    cfg,
		dbClients: dbClients,
		groups:    groups,
		bindings:  bindings,
	}
}

func (c *Checker) Start() {
	interval := time.Duration(c.config.CheckInterval) * time.Second
	slog.Info("定时巡检已启动", "interval", interval)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for range ticker.C {
		c.RunCheck()
	}
}

func (c *Checker) RunCheck() {
	slog.Info("开始巡检...")
	totalKicked := 0

	for chatID, db := range c.dbClients {
		group := c.groups[chatID]
		kicked := c.checkGroup(chatID, db, group)
		totalKicked += kicked
	}

	slog.Info("巡检完成", "total_kicked", totalKicked)
}

func (c *Checker) checkGroup(chatID int64, db *DBClient, group *GroupConfig) int {
	// 合并两个来源的过期用户：数据库 telegram_id + 本地绑定
	expiredUsers := make(map[int64]string)

	// 来源1：数据库中绑定了 telegram_id 的过期用户
	dbExpired, err := db.GetExpiredTelegramUsers()
	if err != nil {
		slog.Error("查询数据库过期用户失败", "chat_id", chatID, "error", err)
	} else {
		for tgID, email := range dbExpired {
			expiredUsers[tgID] = email
		}
	}

	// 来源2：本地绑定文件中的用户，逐个检查是否过期
	dbName := group.Database.DBName
	boundUsers := c.bindings.GetAllForDB(dbName)
	for tgID, email := range boundUsers {
		if _, already := expiredUsers[tgID]; already {
			continue
		}
		user, err := db.FindUserByEmail(email)
		if err != nil {
			continue
		}
		if user == nil || !IsUserValid(user) {
			expiredUsers[tgID] = email
		}
	}

	if len(expiredUsers) == 0 {
		slog.Debug("无过期用户", "chat_id", chatID)
		return 0
	}

	kicked := 0
	for tgID, email := range expiredUsers {
		if group.IsExempt(tgID) {
			continue
		}

		chat := &tele.Chat{ID: chatID}
		tgUser := &tele.User{ID: tgID}
		member, err := c.bot.ChatMemberOf(chat, tgUser)
		if err != nil {
			continue
		}

		if member.Role == tele.Left {
			continue
		}
		// 已被其他管理员/bot封禁，跳过避免误解除
		if member.Role == tele.Kicked {
			slog.Debug("用户已被其他来源封禁，跳过", "user_id", tgID)
			continue
		}

		err = c.bot.Ban(chat, &tele.ChatMember{
			User:            tgUser,
			RestrictedUntil: time.Now().Add(60 * time.Second).Unix(),
		})
		if err != nil {
			slog.Error("踢出用户失败", "user_id", tgID, "email", email, "error", err)
			continue
		}

		if err := c.bot.Unban(chat, tgUser, true); err != nil {
			slog.Warn("Unban失败，将在60秒后自动解除", "user_id", tgID, "error", err)
		}

		kicked++
		slog.Info("已踢出过期用户", "chat_id", chatID, "user_id", tgID, "email", email)

		_, _ = c.bot.Send(&tele.User{ID: tgID},
			"⚠️ 您已被移出群组\n原因：套餐已过期\n\n请前往官网续费后重新申请加入。",
		)

		time.Sleep(500 * time.Millisecond)
	}

	return kicked
}
