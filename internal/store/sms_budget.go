package store

import (
	"math"
	"time"
)

// AddSMSDebit 记一笔接码花费。units 以分为单位，避免浮点累计误差。
func (s *Store) AddSMSDebit(accountID, batchID string, cost float64) error {
	units := int64(math.Round(cost * 100))
	if units <= 0 {
		return nil
	}
	_, err := s.db.Exec(
		`INSERT INTO registration_sms_debits (id, account_id, batch_id, units, created_at) VALUES (?, ?, ?, ?, ?)`,
		newID(), accountID, batchID, units, time.Now().Unix())
	return err
}

// SMSSpent 返回账号与批次已发生的接码花费，单位为分。
func (s *Store) SMSSpent(accountID, batchID string) (accountCents, batchCents int64, err error) {
	err = s.db.QueryRow(`
SELECT IFNULL(SUM(CASE WHEN account_id = ? THEN units ELSE 0 END), 0),
       IFNULL(SUM(CASE WHEN batch_id = ? AND ? <> '' THEN units ELSE 0 END), 0)
FROM registration_sms_debits`, accountID, batchID, batchID).Scan(&accountCents, &batchCents)
	return
}
