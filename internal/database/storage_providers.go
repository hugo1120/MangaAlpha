package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ErrWebDAVAccountInUse indicates that one or more libraries still reference a WebDAV account.
var ErrWebDAVAccountInUse = errors.New("webdav account is in use by one or more libraries")

// ErrWebDAVAccountNotFound indicates that the referenced WebDAV account does not exist.
var ErrWebDAVAccountNotFound = errors.New("webdav account not found")

// WebDAVAccountInUseError carries the human-readable list of libraries referencing a WebDAV account.
type WebDAVAccountInUseError struct {
	Libraries []string
}

func (e *WebDAVAccountInUseError) Error() string {
	if e == nil || len(e.Libraries) == 0 {
		return ErrWebDAVAccountInUse.Error()
	}
	return fmt.Sprintf("cannot delete WebDAV account while libraries still use it: %s", strings.Join(e.Libraries, ", "))
}

func (e *WebDAVAccountInUseError) Unwrap() error {
	return ErrWebDAVAccountInUse
}

func (s *Store) WebDAVAccounts(ctx context.Context) ([]WebDAVAccount, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id,name,base_url,username,password,insecure_tls,rate_per_second,created_at,updated_at
FROM webdav_accounts ORDER BY name COLLATE NOCASE,id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]WebDAVAccount, 0)
	for rows.Next() {
		var (
			item        WebDAVAccount
			password    string
			insecureInt int
		)
		if err := rows.Scan(
			&item.ID, &item.Name, &item.BaseURL, &item.Username, &password,
			&insecureInt, &item.RatePerSecond, &item.CreatedAt, &item.UpdatedAt,
		); err != nil {
			return nil, err
		}
		item.HasPassword = password != ""
		item.InsecureTLS = insecureInt != 0
		out = append(out, item)
	}
	return out, rows.Err()
}

func (s *Store) WebDAVAccount(ctx context.Context, id string) (WebDAVAccount, error) {
	sec, err := s.WebDAVAccountSecret(ctx, id)
	if err != nil {
		return WebDAVAccount{}, err
	}
	return sec.WebDAVAccount, nil
}

func (s *Store) WebDAVAccountSecret(ctx context.Context, id string) (WebDAVAccountSecret, error) {
	var (
		item        WebDAVAccountSecret
		insecureInt int
	)
	err := s.db.QueryRowContext(ctx, `
SELECT id,name,base_url,username,password,insecure_tls,rate_per_second,created_at,updated_at
FROM webdav_accounts WHERE id=?`, strings.TrimSpace(id)).Scan(
		&item.ID, &item.Name, &item.BaseURL, &item.Username, &item.Password,
		&insecureInt, &item.RatePerSecond, &item.CreatedAt, &item.UpdatedAt,
	)
	if err != nil {
		return WebDAVAccountSecret{}, err
	}
	item.HasPassword = item.Password != ""
	item.InsecureTLS = insecureInt != 0
	if item.RatePerSecond <= 0 {
		item.RatePerSecond = 8
	}
	return item, nil
}

func (s *Store) UpsertWebDAVAccount(
	ctx context.Context,
	item WebDAVAccountSecret,
	keepExistingPassword bool,
) (WebDAVAccount, error) {
	id := strings.TrimSpace(item.ID)
	if id == "" {
		generated, err := randomHexID(12)
		if err != nil {
			return WebDAVAccount{}, err
		}
		id = "wd_" + generated
	}
	password := item.Password
	if keepExistingPassword && password == "" {
		if existing, err := s.WebDAVAccountSecret(ctx, id); err == nil {
			password = existing.Password
		} else if !errors.Is(err, sql.ErrNoRows) {
			return WebDAVAccount{}, err
		}
	}
	rate := item.RatePerSecond
	if rate <= 0 {
		rate = 8
	}
	insecureInt := 0
	if item.InsecureTLS {
		insecureInt = 1
	}
	now := nowText()
	_, err := s.db.ExecContext(ctx, `
INSERT INTO webdav_accounts(
 id,name,base_url,username,password,insecure_tls,rate_per_second,created_at,updated_at
) VALUES(?,?,?,?,?,?,?,?,?)
ON CONFLICT(id) DO UPDATE SET
 name=excluded.name,
 base_url=excluded.base_url,
 username=excluded.username,
 password=excluded.password,
 insecure_tls=excluded.insecure_tls,
 rate_per_second=excluded.rate_per_second,
 updated_at=excluded.updated_at`,
		id, strings.TrimSpace(item.Name), strings.TrimRight(strings.TrimSpace(item.BaseURL), "/"),
		strings.TrimSpace(item.Username), password, insecureInt, rate, now, now)
	if err != nil {
		return WebDAVAccount{}, err
	}
	return s.WebDAVAccount(ctx, id)
}

func (s *Store) DeleteWebDAVAccount(ctx context.Context, id string) error {
	id = strings.TrimSpace(id)
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, `BEGIN IMMEDIATE`); err != nil {
		return err
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = conn.ExecContext(context.Background(), `ROLLBACK`)
		}
	}()

	rows, err := conn.QueryContext(ctx, `
SELECT l.name, l.enabled,
       coalesce((SELECT group_concat(ul2.name, ' / ')
                 FROM union_layers ul
                 JOIN libraries ul2 ON ul2.id = ul.union_library_id
                 WHERE ul.member_library_id = l.id), '')
FROM libraries l
WHERE l.provider_type='webdav' AND l.webdav_account_id=?
ORDER BY l.name COLLATE NOCASE, l.id`, id)
	if err != nil {
		return err
	}
	var using []string
	for rows.Next() {
		var (
			name       string
			enabled    int
			unionNames string
		)
		if err := rows.Scan(&name, &enabled, &unionNames); err != nil {
			rows.Close()
			return err
		}
		label := strings.TrimSpace(name)
		if label == "" {
			label = "(未命名书库)"
		}
		var notes []string
		if enabled == 0 {
			notes = append(notes, "已隐藏")
		}
		if strings.TrimSpace(unionNames) != "" {
			notes = append(notes, "联合库「"+strings.TrimSpace(unionNames)+"」子库")
		}
		if len(notes) > 0 {
			label += "（" + strings.Join(notes, "，") + "）"
		}
		using = append(using, label)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	if len(using) > 0 {
		return &WebDAVAccountInUseError{Libraries: using}
	}

	res, err := conn.ExecContext(ctx, `DELETE FROM webdav_accounts WHERE id=?`, id)
	if err != nil {
		return err
	}
	if affected, _ := res.RowsAffected(); affected == 0 {
		return sql.ErrNoRows
	}
	if _, err := conn.ExecContext(ctx, `COMMIT`); err != nil {
		return err
	}
	committed = true
	return nil
}
