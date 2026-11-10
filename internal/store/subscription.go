package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// SubscriptionCheckpoint is private runtime state, never an API projection.
type SubscriptionCheckpoint struct{ State, UserID, Key string }

func (s *Store) migrateSubscription() error {
	_, err := s.db.Exec(`CREATE TABLE IF NOT EXISTS subscription_checkpoints (
 account_id TEXT PRIMARY KEY REFERENCES accounts(id) ON DELETE CASCADE,
 state TEXT NOT NULL, user_id TEXT NOT NULL, api_key TEXT NOT NULL DEFAULT '')`)
	return err
}

func (s *Store) GetSubscription(id string) (SubscriptionCheckpoint, error) {
	var cp SubscriptionCheckpoint
	err := s.db.QueryRow(`SELECT state,user_id,api_key FROM subscription_checkpoints WHERE account_id=?`, id).Scan(&cp.State, &cp.UserID, &cp.Key)
	if errors.Is(err, sql.ErrNoRows) {
		return cp, nil
	}
	return cp, err
}

func (s *Store) BeginSubscriptionKey(id, userID string) error {
	if id == "" || userID == "" {
		return fmt.Errorf("账号身份无效")
	}
	// The unique account checkpoint fences ambiguous creates across process restarts.
	_, err := s.db.Exec(`INSERT INTO subscription_checkpoints(account_id,state,user_id) VALUES(?,'key_pending',?)`, id, userID)
	return err
}

func (s *Store) SaveSubscriptionKey(id, userID, key string) error {
	if strings.TrimSpace(key) == "" {
		return fmt.Errorf("Key 为空")
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	result, err := tx.Exec(`UPDATE subscription_checkpoints SET state='key_saved',api_key=? WHERE account_id=? AND user_id=? AND state='key_pending'`, key, id, userID)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return fmt.Errorf("Key 保存状态不一致")
	}
	now := time.Now().Unix()
	result, err = tx.Exec(`UPDATE accounts SET api_key=?,user_id=?,updated_at=? WHERE id=?`, key, userID, now, id)
	if err != nil {
		return err
	}
	n, _ = result.RowsAffected()
	if n != 1 {
		return fmt.Errorf("账号不存在")
	}
	return tx.Commit()
}

func (s *Store) MarkSubscriptionImported(id string) error {
	result, err := s.db.Exec(`UPDATE subscription_checkpoints SET state='imported' WHERE account_id=? AND state IN ('key_saved','imported') AND api_key<>''`, id)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n != 1 {
		return fmt.Errorf("Key 尚未保存")
	}
	return nil
}
