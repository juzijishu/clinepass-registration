package store

import (
	"database/sql"
	"errors"
	"fmt"
	"net/mail"
	"strings"
	"unicode/utf8"

	"github.com/juzijishu/clinepass-registration/internal/model"
)

const maxBatchImportBytes = 1 << 20
const maxBatchImportAccounts = 1000

func validDeliveryEmail(value string) bool {
	address, err := mail.ParseAddress(value)
	return err == nil && address.Name == "" && address.Address == value && strings.Contains(value, "@")
}

type batchImportRow struct {
	line    int
	account model.CreateAccountInput
	err     error
}

// Preview and persistence share the parser; only persistence may mutate accounts.
func parseBatchImport(in model.CreateBatchInput) ([]batchImportRow, error) {
	if len(in.Text) > maxBatchImportBytes || !utf8.ValidString(in.Text) {
		return nil, fmt.Errorf("账号文本须为 UTF-8，且不能超过 1 MiB")
	}
	switch strings.ToLower(strings.TrimSpace(in.LoginProvider)) {
	case "", "google", "microsoft", "ms", "outlook", "hotmail", "live":
	default:
		return nil, fmt.Errorf("登录方式须为 Google 或微软")
	}
	var rows []batchImportRow
	for i, line := range strings.Split(strings.TrimPrefix(in.Text, "\ufeff"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		account, err := parseDelivery(line)
		account.Email = strings.ToLower(account.Email)
		if account.LoginProvider == "" {
			account.LoginProvider = model.NormalizeLoginProvider(in.LoginProvider)
		}
		rows = append(rows, batchImportRow{line: i + 1, account: account, err: err})
		if len(rows) > maxBatchImportAccounts {
			return nil, fmt.Errorf("每批最多导入 1000 个账号")
		}
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("没有账号内容，请选择 TXT 文件或粘贴账号")
	}
	return rows, nil
}

func (s *Store) PreviewBatch(in model.CreateBatchInput) (model.BatchPreview, error) {
	rows, err := parseBatchImport(in)
	if err != nil {
		return model.BatchPreview{}, err
	}
	out := model.BatchPreview{Total: len(rows), Items: make([]model.BatchPreviewItem, 0, len(rows))}
	seen := make(map[string]bool)
	stmt, err := s.db.Prepare(`SELECT 1 FROM accounts WHERE email = ?`)
	if err != nil {
		return out, fmt.Errorf("无法检查重复账号，请稍后重试")
	}
	defer stmt.Close()
	for _, row := range rows {
		item := model.BatchPreviewItem{Line: row.line, Status: "invalid"}
		if row.err != nil {
			item.Message = row.err.Error()
			out.Invalid++
		} else {
			email := row.account.Email
			local, domain, _ := strings.Cut(email, "@")
			item.Email = string([]rune(local)[:1]) + "***@" + domain
			item.LoginProvider = row.account.LoginProvider
			var exists int
			err := stmt.QueryRow(email).Scan(&exists)
			if err != nil && !errors.Is(err, sql.ErrNoRows) {
				return model.BatchPreview{}, fmt.Errorf("无法检查重复账号，请稍后重试")
			}
			switch {
			case seen[email]:
				item.Status, item.Message = "duplicate", "文件内重复，将跳过"
				out.Duplicates++
			case err == nil:
				item.Status, item.Message = "duplicate", "账号已存在，将跳过并保留原凭据"
				out.Duplicates++
			default:
				item.Status, item.Message = "valid", "可导入"
				out.Importable++
			}
			seen[email] = true
		}
		out.Items = append(out.Items, item)
	}
	return out, nil
}
