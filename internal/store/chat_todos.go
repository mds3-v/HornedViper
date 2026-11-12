package store

import (
	"context"
	"encoding/json"

	"hornedviper/internal/domain"
)

func (s *Store) SetChatTodos(ctx context.Context, ownerType string, ownerID int64, todos []domain.TodoItem) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	data, _ := json.Marshal(todos)
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO chat_todos (owner_type, owner_id, items_json, updated_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(owner_type, owner_id) DO UPDATE SET items_json=excluded.items_json, updated_at=excluded.updated_at`,
		ownerType, ownerID, string(data), nowMS())
	return err
}

func (s *Store) GetChatTodos(ctx context.Context, ownerType string, ownerID int64) ([]domain.TodoItem, error) {
	var data string
	err := s.db.QueryRowContext(ctx,
		`SELECT items_json FROM chat_todos WHERE owner_type=? AND owner_id=?`, ownerType, ownerID).Scan(&data)
	if err != nil {
		return nil, nil
	}
	var todos []domain.TodoItem
	_ = json.Unmarshal([]byte(data), &todos)
	return todos, nil
}
