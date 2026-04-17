package bot

import (
	"fmt"
	"strings"
	"time"

	tele "gopkg.in/telebot.v3"

	"v2board-tg-bot/internal/db"
)

// planNameOf 返回用户的套餐名，若未找到返回空字符串
func planNameOf(client *db.Client, user *db.V2User) string {
	if user == nil || !user.PlanID.Valid || user.PlanID.Int64 == 0 {
		return ""
	}
	return client.FindPlanNameByID(user.PlanID.Int64)
}

// describeInvalid 生成无效套餐的原因描述
func describeInvalid(user *db.V2User) string {
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

// displayName 生成 Telegram 用户的显示名称
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

// formatUserInfo 详细用户卡片（用于 /cha）
func formatUserInfo(client *db.Client, user *db.V2User) string {
	status := "✅ 有效"
	if !db.IsUserValid(user) {
		switch {
		case user.Banned != 0:
			status = "🚫 已封禁"
		case !user.PlanID.Valid || user.PlanID.Int64 == 0:
			status = "❌ 无套餐"
		default:
			status = "❌ 已过期"
		}
	}
	expiredStr := "永不过期"
	if user.ExpiredAt.Valid && user.ExpiredAt.Int64 != 0 {
		expiredStr = time.Unix(user.ExpiredAt.Int64, 0).Format("2006-01-02 15:04:05")
	}
	planStr := "无"
	if user.PlanID.Valid && user.PlanID.Int64 != 0 {
		if name := client.FindPlanNameByID(user.PlanID.Int64); name != "" {
			planStr = fmt.Sprintf("%s (ID: %d)", name, user.PlanID.Int64)
		} else {
			planStr = fmt.Sprintf("ID: %d", user.PlanID.Int64)
		}
	}
	return fmt.Sprintf(
		"👤 ID：%d\n📧 邮箱：%s\n📦 套餐：%s\n⏰ 到期：%s\n📌 状态：%s",
		user.ID, user.Email, planStr, expiredStr, status,
	)
}

// maskEmail 对邮箱进行脱敏：ab***@xx.com
func maskEmail(email string) string {
	at := strings.Index(email, "@")
	if at <= 0 {
		return "***"
	}
	local := email[:at]
	domain := email[at:]
	if len(local) <= 2 {
		return local[:1] + "***" + domain
	}
	return local[:2] + "***" + domain
}

// formatStatusLine 精简用户卡片（用于 /status），mask=true 时邮箱脱敏
func formatStatusLine(client *db.Client, user *db.V2User, mask bool) string {
	status := "✅ 有效"
	if !db.IsUserValid(user) {
		switch {
		case user.Banned != 0:
			status = "🚫 已封禁"
		case !user.PlanID.Valid || user.PlanID.Int64 == 0:
			status = "❌ 无套餐"
		default:
			status = "❌ 已过期"
		}
	}
	planStr := "无"
	if user.PlanID.Valid && user.PlanID.Int64 != 0 {
		if name := client.FindPlanNameByID(user.PlanID.Int64); name != "" {
			planStr = name
		} else {
			planStr = fmt.Sprintf("ID %d", user.PlanID.Int64)
		}
	}
	expiredStr := "永不过期"
	if user.ExpiredAt.Valid && user.ExpiredAt.Int64 != 0 {
		expiredStr = time.Unix(user.ExpiredAt.Int64, 0).Format("2006-01-02 15:04")
	}
	email := user.Email
	if mask {
		email = maskEmail(email)
	}
	return fmt.Sprintf(
		"👤 邮箱：`%s`\n📦 套餐：%s\n⏰ 到期：%s\n📌 状态：%s",
		email, planStr, expiredStr, status,
	)
}
