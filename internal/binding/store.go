package binding

import (
	"encoding/json"
	"log/slog"
	"os"
	"sync"
)

// UserBinding 单个 Telegram 账号到面板账户的本地绑定
type UserBinding struct {
	Email  string `json:"email"`
	DBName string `json:"db_name"`
}

// Store 基于 JSON 文件的线程安全绑定存储
type Store struct {
	mu       sync.RWMutex
	filePath string
	// data key: telegram_id
	data map[int64]UserBinding
}

// New 打开（或初始化）绑定存储
func New(filePath string) (*Store, error) {
	s := &Store{
		filePath: filePath,
		data:     make(map[int64]UserBinding),
	}
	if err := s.load(); err != nil {
		slog.Warn("绑定文件不存在或为空，将创建新文件", "path", filePath)
	}
	return s, nil
}

func (s *Store) load() error {
	raw, err := os.ReadFile(s.filePath)
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, &s.data)
}

func (s *Store) save() error {
	raw, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.filePath, raw, 0644)
}

func (s *Store) Set(telegramID int64, email, dbName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[telegramID] = UserBinding{Email: email, DBName: dbName}
	return s.save()
}

func (s *Store) Get(telegramID int64) (UserBinding, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, ok := s.data[telegramID]
	return b, ok
}

func (s *Store) Delete(telegramID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, telegramID)
	return s.save()
}

// FindByEmail 检查某个邮箱是否已被其他 Telegram 账号绑定，返回 telegram_id；未找到返回 0
func (s *Store) FindByEmail(email string) int64 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for tgID, b := range s.data {
		if b.Email == email {
			return tgID
		}
	}
	return 0
}

// GetAllForDB 获取某个数据库下所有绑定的 telegram_id
func (s *Store) GetAllForDB(dbName string) map[int64]string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make(map[int64]string)
	for tgID, b := range s.data {
		if b.DBName == dbName {
			result[tgID] = b.Email
		}
	}
	return result
}
