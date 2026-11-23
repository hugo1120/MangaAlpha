package database

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"strings"
	"time"
)

const maxArchiveFolderRetryCandidates = 8

type ArchivePasswordCandidate struct {
	PasswordID string
	Password   string
	Source     string // "direct", "series", "folder"
}

type ArchivePasswordBindingInfo struct {
	BookID      string    `json:"bookId"`
	BookName    string    `json:"bookName"`
	SeriesID    string    `json:"seriesId"`
	SeriesTitle string    `json:"seriesTitle"`
	LibraryID   string    `json:"libraryId"`
	LibraryName string    `json:"libraryName"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type ArchivePasswordGroup struct {
	ID        string                       `json:"id"`
	Password  string                       `json:"password"`
	CreatedAt time.Time                    `json:"createdAt"`
	UpdatedAt time.Time                    `json:"updatedAt"`
	Bindings  []ArchivePasswordBindingInfo `json:"bindings"`
}

func newArchivePasswordID() string {
	var raw [8]byte
	_, _ = rand.Read(raw[:])
	return "apw_" + hex.EncodeToString(raw[:])
}

// SaveArchivePasswordBinding stores password (deduplicated by exact value) and binds
// it to book. When a book belongs to a union library, callers may pass the physical
// source book as extraFolderBooks so same-folder auto-retry also sees physical siblings.
func (s *Store) SaveArchivePasswordBinding(ctx context.Context, book Book, password string, extraFolderBooks ...Book) error {
	if book.ID == "" || password == "" {
		return errors.New("book ID and password are required")
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var passwordID string
	err = tx.QueryRowContext(ctx, `SELECT id FROM archive_passwords WHERE password=?`, password).Scan(&passwordID)
	if errors.Is(err, sql.ErrNoRows) {
		passwordID = newArchivePasswordID()
		if _, err := tx.ExecContext(ctx, `
INSERT INTO archive_passwords(id,password,created_at,updated_at)
VALUES(?,?,?,?)`, passwordID, password, now, now); err != nil {
			return err
		}
	} else if err != nil {
		return err
	} else {
		if _, err := tx.ExecContext(ctx, `UPDATE archive_passwords SET updated_at=? WHERE id=?`, now, passwordID); err != nil {
			return err
		}
	}

	bindOne := func(b Book) error {
		if b.ID == "" {
			return nil
		}
		_, err := tx.ExecContext(ctx, `
INSERT INTO archive_password_bindings(book_id,password_id,series_id,library_id,parent_cid,created_at,updated_at)
VALUES(?,?,?,?,?,?,?)
ON CONFLICT(book_id) DO UPDATE SET
 password_id=excluded.password_id,
 series_id=excluded.series_id,
 library_id=excluded.library_id,
 parent_cid=excluded.parent_cid,
 updated_at=excluded.updated_at`,
			b.ID, passwordID, b.SeriesID, b.LibraryID, b.ParentCID, now, now)
		return err
	}

	if err := bindOne(book); err != nil {
		return err
	}
	for _, extra := range extraFolderBooks {
		if extra.ID != "" && extra.ID != book.ID {
			if err := bindOne(extra); err != nil {
				return err
			}
		}
	}

	// Prune any password row that no longer has active bindings (e.g. book changed password).
	if _, err := tx.ExecContext(ctx, `
DELETE FROM archive_passwords
WHERE NOT EXISTS (
  SELECT 1 FROM archive_password_bindings b WHERE b.password_id=archive_passwords.id
)`); err != nil {
		return err
	}
	return tx.Commit()
}

// ArchivePasswordCandidatesForBook returns deduplicated remembered passwords ordered by:
//  1. direct binding on book_id
//  2. sibling bindings in the same series_id
//  3. sibling bindings in the same folder (library_id, parent_cid) — covers One-Shot directories too
func (s *Store) ArchivePasswordCandidatesForBook(ctx context.Context, books ...Book) ([]ArchivePasswordCandidate, error) {
	if len(books) == 0 {
		return nil, nil
	}
	seenPass := make(map[string]bool)
	out := make([]ArchivePasswordCandidate, 0, 4)

	add := func(id, pw, source string) {
		if pw == "" || seenPass[pw] {
			return
		}
		seenPass[pw] = true
		out = append(out, ArchivePasswordCandidate{
			PasswordID: id,
			Password:   pw,
			Source:     source,
		})
	}

	// 1. Direct bindings
	bookIDs := make([]string, 0, len(books))
	for _, b := range books {
		if b.ID != "" {
			bookIDs = append(bookIDs, b.ID)
		}
	}
	bookIDs = cleanStringSliceCaseSensitive(bookIDs)
	if len(bookIDs) > 0 {
		args := make([]any, len(bookIDs))
		for i, id := range bookIDs {
			args[i] = id
		}
		rows, err := s.db.QueryContext(ctx, `
SELECT p.id, p.password
FROM archive_password_bindings b
JOIN archive_passwords p ON p.id=b.password_id
WHERE b.book_id IN `+Placeholders(len(bookIDs))+`
ORDER BY b.updated_at DESC`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id, pw string
			if err := rows.Scan(&id, &pw); err != nil {
				rows.Close()
				return nil, err
			}
			add(id, pw, "direct")
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}

	nonDirectCount := 0
	// 2. Same series bindings
	seriesIDs := make([]string, 0, len(books))
	for _, b := range books {
		if b.SeriesID != "" {
			seriesIDs = append(seriesIDs, b.SeriesID)
		}
	}
	seriesIDs = cleanStringSliceCaseSensitive(seriesIDs)
	if len(seriesIDs) > 0 && nonDirectCount < maxArchiveFolderRetryCandidates {
		args := make([]any, len(seriesIDs))
		for i, id := range seriesIDs {
			args[i] = id
		}
		rows, err := s.db.QueryContext(ctx, `
SELECT p.id, p.password
FROM archive_password_bindings b
JOIN archive_passwords p ON p.id=b.password_id
WHERE b.series_id IN `+Placeholders(len(seriesIDs))+`
ORDER BY b.updated_at DESC`, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() && nonDirectCount < maxArchiveFolderRetryCandidates {
			var id, pw string
			if err := rows.Scan(&id, &pw); err != nil {
				rows.Close()
				return nil, err
			}
			if !seenPass[pw] {
				add(id, pw, "series")
				nonDirectCount++
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}

	// 3. Same folder (library_id, parent_cid) bindings — covers One-Shot directories and physical folders
	type folderKey struct{ libID, parentCID string }
	seenFolders := make(map[folderKey]bool)
	for _, b := range books {
		if b.LibraryID == "" || b.ParentCID == "" || nonDirectCount >= maxArchiveFolderRetryCandidates {
			continue
		}
		fk := folderKey{libID: b.LibraryID, parentCID: b.ParentCID}
		if seenFolders[fk] {
			continue
		}
		seenFolders[fk] = true
		rows, err := s.db.QueryContext(ctx, `
SELECT p.id, p.password
FROM archive_password_bindings b
JOIN archive_passwords p ON p.id=b.password_id
WHERE b.library_id=? AND b.parent_cid=?
ORDER BY b.updated_at DESC`, fk.libID, fk.parentCID)
		if err != nil {
			return nil, err
		}
		for rows.Next() && nonDirectCount < maxArchiveFolderRetryCandidates {
			var id, pw string
			if err := rows.Scan(&id, &pw); err != nil {
				rows.Close()
				return nil, err
			}
			if !seenPass[pw] {
				add(id, pw, "folder")
				nonDirectCount++
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}

	return out, nil
}

// ListArchivePasswordGroups lists all remembered archive passwords grouped with their bound books.
func (s *Store) ListArchivePasswordGroups(ctx context.Context) ([]ArchivePasswordGroup, error) {
	rows, err := s.db.QueryContext(ctx, `
SELECT id, password, created_at, updated_at
FROM archive_passwords
ORDER BY updated_at DESC, id ASC`)
	if err != nil {
		return nil, err
	}
	groups := make([]ArchivePasswordGroup, 0)
	byID := make(map[string]int)
	for rows.Next() {
		var g ArchivePasswordGroup
		var created, updated string
		if err := rows.Scan(&g.ID, &g.Password, &created, &updated); err != nil {
			rows.Close()
			return nil, err
		}
		g.CreatedAt = parseTime(created)
		g.UpdatedAt = parseTime(updated)
		g.Bindings = make([]ArchivePasswordBindingInfo, 0)
		byID[g.ID] = len(groups)
		groups = append(groups, g)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	if len(groups) == 0 {
		return groups, nil
	}

	bRows, err := s.db.QueryContext(ctx, `
SELECT apb.password_id, apb.book_id, coalesce(bk.name,''), coalesce(bk.series_id, apb.series_id),
       coalesce(nullif(trim(sm.title),''), sr.name, ''),
       coalesce(bk.library_id, apb.library_id), coalesce(l.name,''), apb.updated_at
FROM archive_password_bindings apb
LEFT JOIN books bk ON bk.id=apb.book_id
LEFT JOIN series sr ON sr.id=coalesce(bk.series_id, apb.series_id)
LEFT JOIN series_metadata sm ON sm.series_id=sr.id
LEFT JOIN libraries l ON l.id=coalesce(bk.library_id, apb.library_id)
WHERE NOT EXISTS (SELECT 1 FROM union_layers ul WHERE ul.member_library_id=coalesce(bk.library_id, apb.library_id))
ORDER BY apb.updated_at DESC, apb.book_id ASC`)
	if err != nil {
		return nil, err
	}
	defer bRows.Close()
	for bRows.Next() {
		var pid string
		var info ArchivePasswordBindingInfo
		var updated string
		if err := bRows.Scan(
			&pid, &info.BookID, &info.BookName, &info.SeriesID,
			&info.SeriesTitle, &info.LibraryID, &info.LibraryName, &updated,
		); err != nil {
			return nil, err
		}
		info.UpdatedAt = parseTime(updated)
		if strings.TrimSpace(info.BookName) == "" {
			info.BookName = info.BookID
		}
		if idx, ok := byID[pid]; ok {
			groups[idx].Bindings = append(groups[idx].Bindings, info)
		}
	}
	if err := bRows.Err(); err != nil {
		return nil, err
	}
	return groups, nil
}

func (s *Store) expandUnionRelatedBookIDsTx(ctx context.Context, tx *sql.Tx, bookIDs []string) ([]string, error) {
	seen := make(map[string]bool, len(bookIDs))
	out := make([]string, 0, len(bookIDs))
	for _, id := range bookIDs {
		if id != "" && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	if len(out) == 0 {
		return out, nil
	}
	for _, chunk := range chunkStrings(out, sqlInChunkSize) {
		args := make([]any, len(chunk)*2)
		for i, id := range chunk {
			args[i] = id
			args[len(chunk)+i] = id
		}
		ph := Placeholders(len(chunk))
		rows, err := tx.QueryContext(ctx, `
SELECT union_book_id, source_book_id
FROM union_book_sources
WHERE union_book_id IN `+ph+` OR source_book_id IN `+ph, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var uID, sID string
			if err := rows.Scan(&uID, &sID); err != nil {
				rows.Close()
				return nil, err
			}
			for _, id := range []string{uID, sID} {
				if id != "" && !seen[id] {
					seen[id] = true
					out = append(out, id)
				}
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			return nil, err
		}
		rows.Close()
	}
	return out, nil
}

func deleteZipIndexesTx(ctx context.Context, tx *sql.Tx, bookIDs []string) error {
	for _, chunk := range chunkStrings(bookIDs, sqlInChunkSize) {
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM zip_indexes WHERE book_id IN `+Placeholders(len(chunk)), args...); err != nil {
			return err
		}
	}
	return nil
}

// DeleteArchivePassword removes a remembered password group, all of its bindings,
// and invalidates cached zip_indexes for affected books (returning their IDs so
// callers can also purge decrypted page caches).
func (s *Store) DeleteArchivePassword(ctx context.Context, passwordID string) ([]string, error) {
	if strings.TrimSpace(passwordID) == "" {
		return nil, sql.ErrNoRows
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `SELECT book_id FROM archive_password_bindings WHERE password_id=?`, passwordID)
	if err != nil {
		return nil, err
	}
	var bookIDs []string
	for rows.Next() {
		var bid string
		if err := rows.Scan(&bid); err != nil {
			rows.Close()
			return nil, err
		}
		bookIDs = append(bookIDs, bid)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	res, err := tx.ExecContext(ctx, `DELETE FROM archive_passwords WHERE id=?`, passwordID)
	if err != nil {
		return nil, err
	}
	affected, _ := res.RowsAffected()
	if affected == 0 {
		return nil, sql.ErrNoRows
	}

	allBookIDs, err := s.expandUnionRelatedBookIDsTx(ctx, tx, bookIDs)
	if err != nil {
		return nil, err
	}
	if err := deleteZipIndexesTx(ctx, tx, allBookIDs); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return allBookIDs, nil
}

// DeleteArchivePasswordBinding unbinds a single book (plus its union alias/sources),
// prunes orphaned passwords, and invalidates zip_indexes for the affected books.
func (s *Store) DeleteArchivePasswordBinding(ctx context.Context, bookID string) ([]string, error) {
	if strings.TrimSpace(bookID) == "" {
		return nil, sql.ErrNoRows
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	allBookIDs, err := s.expandUnionRelatedBookIDsTx(ctx, tx, []string{bookID})
	if err != nil {
		return nil, err
	}
	var deleted int64
	for _, chunk := range chunkStrings(allBookIDs, sqlInChunkSize) {
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		res, err := tx.ExecContext(ctx, `DELETE FROM archive_password_bindings WHERE book_id IN `+Placeholders(len(chunk)), args...)
		if err != nil {
			return nil, err
		}
		n, _ := res.RowsAffected()
		deleted += n
	}
	if deleted == 0 {
		return nil, sql.ErrNoRows
	}
	if _, err := tx.ExecContext(ctx, `
DELETE FROM archive_passwords
WHERE NOT EXISTS (
  SELECT 1 FROM archive_password_bindings b WHERE b.password_id=archive_passwords.id
)`); err != nil {
		return nil, err
	}
	if err := deleteZipIndexesTx(ctx, tx, allBookIDs); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return allBookIDs, nil
}

// ClearAllArchivePasswords clears all remembered passwords and bindings and invalidates
// zip_indexes for all previously bound books.
func (s *Store) ClearAllArchivePasswords(ctx context.Context) ([]string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	rows, err := tx.QueryContext(ctx, `SELECT book_id FROM archive_password_bindings`)
	if err != nil {
		return nil, err
	}
	var bookIDs []string
	for rows.Next() {
		var bid string
		if err := rows.Scan(&bid); err != nil {
			rows.Close()
			return nil, err
		}
		bookIDs = append(bookIDs, bid)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()

	if _, err := tx.ExecContext(ctx, `DELETE FROM archive_password_bindings`); err != nil {
		return nil, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM archive_passwords`); err != nil {
		return nil, err
	}
	allBookIDs, err := s.expandUnionRelatedBookIDsTx(ctx, tx, bookIDs)
	if err != nil {
		return nil, err
	}
	if err := deleteZipIndexesTx(ctx, tx, allBookIDs); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return allBookIDs, nil
}
