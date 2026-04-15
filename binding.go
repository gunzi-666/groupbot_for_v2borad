package main

import (
	"encoding/json"
	"log/slog"
	"os"
	"sync"
)

type UserBinding struct {
	Email  string `json:"email"`
	DBName string `json:"db_name"`
}

type BindingStore struct {
	mu       sync.RWMutex
	filePath string
	// telegram_id -> UserBinding
	data map[int64]UserBinding
}

func NewBindingStore(filePath string) (*BindingStore, error) {
	store := &BindingStore{
		filePath: filePath,
		data:     make(map[int64]UserBinding),
	}
	if err := store.load(); err != nil {
		slog.Warn("绑定文件不存在或为空，将创建新文件", "path", filePath)
	}
	return store, nil
}

func (s *BindingStore) load() error {
	raw, err := os.ReadFile(s.filePath)
	if err != nil {
		return err
	}
	if len(raw) == 0 {
		return nil
	}
	return json.Unmarshal(raw, &s.data)
}

func (s *BindingStore) save() error {
	raw, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.filePath, raw, 0644)
}

func (s *BindingStore) Set(telegramID int64, email, dbName string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[telegramID] = UserBinding{Email: email, DBName: dbName}
	return s.save()
}

func (s *BindingStore) Get(telegramID int64) (UserBinding, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, ok := s.data[telegramID]
	return b, ok
}

func (s *BindingStore) Delete(telegramID int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, telegramID)
	return s.save()
}

// FindByEmail 检查某个邮箱是否已被其他 Telegram 账号绑定，返回绑定的 telegram_id，未找到返回 0
func (s *BindingStore) FindByEmail(email string) int64 {
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
func (s *BindingStore) GetAllForDB(dbName string) map[int64]string {
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
