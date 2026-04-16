package main

import (
	"crypto/md5"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log/slog"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"golang.org/x/crypto/bcrypt"
)

type V2User struct {
	ID           int64
	Email        string
	Password     string
	PasswordAlgo sql.NullString
	PasswordSalt sql.NullString
	PlanID       sql.NullInt64
	ExpiredAt    sql.NullInt64
	Banned       int
}

type DBClient struct {
	db     *sql.DB
	config DatabaseConfig
}

func NewDBClient(cfg DatabaseConfig) (*DBClient, error) {
	db, err := sql.Open("mysql", cfg.DSN())
	if err != nil {
		return nil, fmt.Errorf("连接数据库失败 [%s]: %w", cfg.DBName, err)
	}

	db.SetMaxOpenConns(3)
	db.SetMaxIdleConns(2)
	db.SetConnMaxLifetime(5 * time.Minute)

	if err := db.Ping(); err != nil {
		return nil, fmt.Errorf("数据库连接测试失败 [%s]: %w", cfg.DBName, err)
	}

	slog.Info("数据库连接成功", "db", cfg.DBName, "host", cfg.Host)
	return &DBClient{db: db, config: cfg}, nil
}

// FindUserByEmail 通过邮箱查找用户（只读）
func (c *DBClient) FindUserByEmail(email string) (*V2User, error) {
	query := `
		SELECT id, email, password, password_algo, password_salt,
		       plan_id, expired_at, banned
		FROM v2_user
		WHERE email = ?
		LIMIT 1
	`
	var user V2User
	err := c.db.QueryRow(query, email).Scan(
		&user.ID, &user.Email,
		&user.Password, &user.PasswordAlgo, &user.PasswordSalt,
		&user.PlanID, &user.ExpiredAt, &user.Banned,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("查询用户失败: %w", err)
	}
	return &user, nil
}

// FindUserByTelegramID 通过 Telegram ID 查找用户（只读）
func (c *DBClient) FindUserByTelegramID(telegramID int64) (*V2User, error) {
	query := `
		SELECT id, email, password, password_algo, password_salt,
		       plan_id, expired_at, banned
		FROM v2_user
		WHERE telegram_id = ?
		LIMIT 1
	`
	var user V2User
	err := c.db.QueryRow(query, telegramID).Scan(
		&user.ID, &user.Email,
		&user.Password, &user.PasswordAlgo, &user.PasswordSalt,
		&user.PlanID, &user.ExpiredAt, &user.Banned,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("查询用户失败: %w", err)
	}
	return &user, nil
}

// VerifyPassword 兼容 v2board 的多算法密码验证
func VerifyPassword(algo, salt, inputPassword, storedHash string) bool {
	switch algo {
	case "md5":
		hash := md5.Sum([]byte(inputPassword))
		return hex.EncodeToString(hash[:]) == storedHash
	case "sha256":
		hash := sha256.Sum256([]byte(inputPassword))
		return hex.EncodeToString(hash[:]) == storedHash
	case "md5salt":
		hash := md5.Sum([]byte(inputPassword + salt))
		return hex.EncodeToString(hash[:]) == storedHash
	default:
		err := bcrypt.CompareHashAndPassword([]byte(storedHash), []byte(inputPassword))
		return err == nil
	}
}

// IsUserValid 检查用户套餐是否有效
func IsUserValid(user *V2User) bool {
	if user.Banned != 0 {
		return false
	}
	if !user.PlanID.Valid || user.PlanID.Int64 == 0 {
		return false
	}
	now := time.Now().Unix()
	if user.ExpiredAt.Valid && user.ExpiredAt.Int64 != 0 && user.ExpiredAt.Int64 < now {
		return false
	}
	return true
}

// FindPlanNameByID 通过套餐 ID 查询套餐名称，未找到返回空字符串
func (c *DBClient) FindPlanNameByID(planID int64) string {
	if planID == 0 {
		return ""
	}
	var name string
	err := c.db.QueryRow(`SELECT name FROM v2_plan WHERE id = ? LIMIT 1`, planID).Scan(&name)
	if err != nil {
		return ""
	}
	return name
}

// GetExpiredTelegramUsers 获取数据库中绑定了 telegram_id 且套餐已过期的用户
func (c *DBClient) GetExpiredTelegramUsers() (map[int64]string, error) {
	query := `
		SELECT telegram_id, email
		FROM v2_user
		WHERE telegram_id IS NOT NULL
		  AND telegram_id != 0
		  AND (
		    banned = 1
		    OR plan_id IS NULL
		    OR plan_id = 0
		    OR (expired_at IS NOT NULL AND expired_at != 0 AND expired_at < UNIX_TIMESTAMP())
		  )
	`
	rows, err := c.db.Query(query)
	if err != nil {
		return nil, fmt.Errorf("查询过期用户失败: %w", err)
	}
	defer rows.Close()

	result := make(map[int64]string)
	for rows.Next() {
		var tgID int64
		var email string
		if err := rows.Scan(&tgID, &email); err != nil {
			slog.Error("扫描用户数据失败", "error", err)
			continue
		}
		result[tgID] = email
	}
	return result, rows.Err()
}

func (c *DBClient) Close() error {
	return c.db.Close()
}
