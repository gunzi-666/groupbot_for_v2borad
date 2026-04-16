package main

import (
	"flag"
	"log/slog"
	"os"
	"time"

	tele "gopkg.in/telebot.v3"
)

func main() {
	configPath := flag.String("config", "config.yaml", "配置文件路径")
	bindingPath := flag.String("bindings", "bindings.json", "绑定数据文件路径")
	flag.Parse()

	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})))

	slog.Info("V2Board 群组管理机器人启动中...")

	cfg, err := LoadConfig(*configPath)
	if err != nil {
		slog.Error("加载配置失败", "error", err)
		os.Exit(1)
	}
	slog.Info("配置加载成功", "groups", len(cfg.Groups), "check_interval", cfg.CheckInterval)

	bindings, err := NewBindingStore(*bindingPath)
	if err != nil {
		slog.Error("加载绑定数据失败", "error", err)
		os.Exit(1)
	}
	slog.Info("绑定数据加载成功", "path", *bindingPath)

	dbClients := make(map[int64]*DBClient)
	for _, g := range cfg.Groups {
		client, err := NewDBClient(g.Database)
		if err != nil {
			slog.Error("数据库连接失败", "chat_id", g.ChatID, "error", err)
			os.Exit(1)
		}
		dbClients[g.ChatID] = client
	}
	defer func() {
		for _, c := range dbClients {
			_ = c.Close()
		}
	}()

	bot, err := tele.NewBot(tele.Settings{
		Token:  cfg.Telegram.BotToken,
		Poller: &tele.LongPoller{Timeout: 30 * time.Second},
	})
	if err != nil {
		slog.Error("创建 Bot 失败", "error", err)
		os.Exit(1)
	}
	slog.Info("Bot 连接成功", "username", bot.Me.Username)

	applyBotProfile(bot, cfg.Telegram.Profile)

	handler := NewBotHandler(bot, cfg, dbClients, bindings)
	handler.Register()

	checker := NewChecker(bot, cfg, dbClients, bindings)
	go checker.Start()

	slog.Info("机器人已启动，等待事件...")
	bot.Start()
}

// applyBotProfile 应用 Bot 的介绍卡片与命令菜单设置
func applyBotProfile(bot *tele.Bot, p BotProfileConfig) {
	if p.Description != "" {
		if err := bot.SetMyDescription(p.Description, ""); err != nil {
			slog.Warn("设置 Bot 介绍失败", "error", err)
		} else {
			slog.Info("Bot 介绍已更新")
		}
	}
	if p.ShortDescription != "" {
		if err := bot.SetMyShortDescription(p.ShortDescription, ""); err != nil {
			slog.Warn("设置 Bot 简介失败", "error", err)
		} else {
			slog.Info("Bot 简介已更新")
		}
	}
	if len(p.Commands) > 0 {
		cmds := make([]tele.Command, 0, len(p.Commands))
		for _, c := range p.Commands {
			cmds = append(cmds, tele.Command{Text: c.Command, Description: c.Description})
		}
		if err := bot.SetCommands(cmds); err != nil {
			slog.Warn("设置命令菜单失败", "error", err)
		} else {
			slog.Info("命令菜单已更新", "count", len(cmds))
		}
	}
}
