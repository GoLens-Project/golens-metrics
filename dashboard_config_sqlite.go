package golens

import (
	"database/sql"
	"fmt"
)

// The dashboard layout document's sqlite store: a single-row table holding
// the same versioned JSON document the file backend writes, so an embedder
// can keep the layout in the database its metrics already live in.

const dashConfigSchema = `
CREATE TABLE IF NOT EXISTS dashboard_config (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    data TEXT NOT NULL,
    updated_at INTEGER NOT NULL DEFAULT (strftime('%s','now'))
);
`

func newDashConfigDB(path string) (*sql.DB, error) {
	// busy_timeout: the configured database may be the same file the metric
	// storage backend writes to; a colliding writer must wait out the other's
	// commit instead of failing the PUT with SQLITE_BUSY. The DSN pragma
	// applies to every pooled connection.
	// ponytail: the path is spliced raw into the DSN, so a plain path
	// containing '?' breaks the pragma suffix (as would pre-existing query
	// params on a file:-prefixed DSN); such paths are vanishingly rare.
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout%3d5000")
	if err != nil {
		return nil, fmt.Errorf("golens: open dashboard config sqlite: %w", err)
	}
	if _, err := db.Exec(dashConfigSchema); err != nil {
		db.Close()
		return nil, fmt.Errorf("golens: init dashboard config schema: %w", err)
	}
	return db, nil
}

// dashConfigLoad returns the stored document bytes. exists is false when no
// document has been saved (empty table) — the same state as a missing file.
func dashConfigLoad(db *sql.DB) (data []byte, exists bool, err error) {
	var s string
	err = db.QueryRow(`SELECT data FROM dashboard_config WHERE id = 1`).Scan(&s)
	if err == sql.ErrNoRows {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	return []byte(s), true, nil
}

// dashConfigPersist upserts the single row. One statement is one implicit
// transaction: the sqlite analogue of the file backend's temp-write + rename.
func dashConfigPersist(db *sql.DB, data []byte) error {
	_, err := db.Exec(
		`INSERT INTO dashboard_config (id, data, updated_at) VALUES (1, ?, strftime('%s','now'))
		 ON CONFLICT(id) DO UPDATE SET data = excluded.data, updated_at = excluded.updated_at`,
		string(data),
	)
	return err
}

// dashConfigDelete removes the saved document row, restoring the "no saved
// document" state (presets re-seed). Deleting a missing row is a no-op,
// matching the file backend's os.IsNotExist tolerance.
func dashConfigDelete(db *sql.DB) error {
	_, err := db.Exec(`DELETE FROM dashboard_config WHERE id = 1`)
	return err
}
