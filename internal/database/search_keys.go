package database

import (
	"database/sql"
	"encoding/json"
	"strings"

	"github.com/xJogger/fake-komga-115/internal/cjk"
)

// searchKeysVersion marks the deployed shape of the folded search columns.
// Bump this when internal/cjk folding changes so existing databases rewrite
// every key once instead of serving stale Traditional/Simplified spellings.
const (
	searchKeysVersion    = "1"
	searchKeysVersionKey = "search_keys_version"
)

// ensureSearchKeys backfills the folded keys for databases created before the
// search_key columns existed. SQLite cannot evaluate the Go folding table
// inside an UPDATE, so the rewrite happens row by row in one transaction.
func ensureSearchKeys(db *sql.DB) error {
	var current string
	err := db.QueryRow(`SELECT value FROM settings WHERE key=?`, searchKeysVersionKey).Scan(&current)
	if err == nil && current == searchKeysVersion {
		return nil
	}
	if err != nil && err != sql.ErrNoRows {
		return err
	}

	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := backfillSeriesSearchKeys(tx); err != nil {
		return err
	}
	if err := backfillBookSearchKeys(tx); err != nil {
		return err
	}
	if err := backfillMetadataSearchKeys(tx); err != nil {
		return err
	}
	if err := backfillTaxonomySearchKeys(tx); err != nil {
		return err
	}
	if _, err := tx.Exec(`
INSERT INTO settings(key,value,updated_at) VALUES(?,?,?)
ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_at=excluded.updated_at`,
		searchKeysVersionKey, searchKeysVersion, nowText()); err != nil {
		return err
	}
	return tx.Commit()
}

func backfillSeriesSearchKeys(tx *sql.Tx) error {
	rows, err := tx.Query(`SELECT id,name FROM series`)
	if err != nil {
		return err
	}
	var ids []string
	var keys []string
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
		keys = append(keys, cjk.SearchKey(name))
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	stmt, err := tx.Prepare(`UPDATE series SET search_key=? WHERE id=?`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for i, id := range ids {
		if _, err := stmt.Exec(keys[i], id); err != nil {
			return err
		}
	}
	return nil
}

func backfillBookSearchKeys(tx *sql.Tx) error {
	rows, err := tx.Query(`SELECT id,name FROM books`)
	if err != nil {
		return err
	}
	var ids []string
	var keys []string
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
		keys = append(keys, cjk.SearchKey(name))
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	stmt, err := tx.Prepare(`UPDATE books SET search_key=? WHERE id=?`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for i, id := range ids {
		if _, err := stmt.Exec(keys[i], id); err != nil {
			return err
		}
	}
	return nil
}

func backfillMetadataSearchKeys(tx *sql.Tx) error {
	rows, err := tx.Query(`SELECT series_id,title,publisher,alternate_titles_json FROM series_metadata`)
	if err != nil {
		return err
	}
	type row struct {
		id, titleKey, publisherKey, alternateKey string
	}
	var rowsToUpdate []row
	for rows.Next() {
		var id, title, publisher, alternateJSON string
		if err := rows.Scan(&id, &title, &publisher, &alternateJSON); err != nil {
			rows.Close()
			return err
		}
		rowsToUpdate = append(rowsToUpdate, row{
			id:           id,
			titleKey:     cjk.SearchKey(title),
			publisherKey: cjk.SearchKey(publisher),
			alternateKey: alternateTitlesSearchKey(alternateJSON),
		})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	stmt, err := tx.Prepare(`UPDATE series_metadata SET title_key=?,publisher_key=?,alternate_key=? WHERE series_id=?`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, r := range rowsToUpdate {
		if _, err := stmt.Exec(r.titleKey, r.publisherKey, r.alternateKey, r.id); err != nil {
			return err
		}
	}
	return nil
}

func alternateTitlesSearchKey(raw string) string {
	if strings.TrimSpace(raw) == "" || raw == "[]" {
		return ""
	}
	var titles []AlternateTitle
	if err := json.Unmarshal([]byte(raw), &titles); err != nil {
		return ""
	}
	var b strings.Builder
	for _, title := range titles {
		key := cjk.SearchKey(title.Title)
		if key == "" {
			continue
		}
		if b.Len() > 0 {
			b.WriteByte(' ')
		}
		b.WriteString(key)
	}
	return b.String()
}

func backfillTaxonomySearchKeys(tx *sql.Tx) error {
	if err := backfillSimpleKeyTable(tx,
		`SELECT series_id,tag FROM series_tags`,
		`UPDATE series_tags SET search_key=? WHERE series_id=? AND tag=?`); err != nil {
		return err
	}
	if err := backfillSimpleKeyTable(tx,
		`SELECT series_id,genre FROM series_genres`,
		`UPDATE series_genres SET search_key=? WHERE series_id=? AND genre=?`); err != nil {
		return err
	}

	rows, err := tx.Query(`SELECT series_id,name,role FROM series_authors`)
	if err != nil {
		return err
	}
	type authorRow struct {
		seriesID, role, key string
		name                string
	}
	var authors []authorRow
	for rows.Next() {
		var seriesID, name, role string
		if err := rows.Scan(&seriesID, &name, &role); err != nil {
			rows.Close()
			return err
		}
		authors = append(authors, authorRow{
			seriesID: seriesID, name: name, role: role, key: cjk.SearchKey(name),
		})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	stmt, err := tx.Prepare(`UPDATE series_authors SET search_key=? WHERE series_id=? AND name=? AND role=?`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, a := range authors {
		if _, err := stmt.Exec(a.key, a.seriesID, a.name, a.role); err != nil {
			return err
		}
	}
	return nil
}

func backfillSimpleKeyTable(tx *sql.Tx, selectSQL, updateSQL string) error {
	rows, err := tx.Query(selectSQL)
	if err != nil {
		return err
	}
	type keyedRow struct {
		seriesID, value, key string
	}
	var items []keyedRow
	for rows.Next() {
		var seriesID, value string
		if err := rows.Scan(&seriesID, &value); err != nil {
			rows.Close()
			return err
		}
		items = append(items, keyedRow{seriesID: seriesID, value: value, key: cjk.SearchKey(value)})
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	stmt, err := tx.Prepare(updateSQL)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, item := range items {
		if _, err := stmt.Exec(item.key, item.seriesID, item.value); err != nil {
			return err
		}
	}
	return nil
}
