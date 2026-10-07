// Package store owns the controller's SQLite database (D5).
//
// Writes go through a single connection so they are serialized; reads use a
// separate pool. Both run in WAL mode.
package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"

	"github.com/pressly/goose/v3"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrations embed.FS

type Store struct {
	// W is the single writer connection. Use it for every INSERT/UPDATE/DELETE.
	W *sql.DB
	// R is the read pool.
	R *sql.DB
}

func Open(ctx context.Context, path string) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create data dir: %w", err)
	}
	dsn := "file:" + path + "?" + url.Values{"_pragma": {
		"journal_mode(WAL)",
		"busy_timeout(5000)",
		"foreign_keys(ON)",
		"synchronous(NORMAL)",
	}}.Encode()

	w, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	w.SetMaxOpenConns(1)

	r, err := sql.Open("sqlite", dsn+"&mode=ro")
	if err != nil {
		w.Close()
		return nil, err
	}
	r.SetMaxOpenConns(8)

	s := &Store{W: w, R: r}
	if err := s.migrate(ctx); err != nil {
		s.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate(ctx context.Context) error {
	p, err := goose.NewProvider(goose.DialectSQLite3, s.W, mustSub(migrations, "migrations"))
	if err != nil {
		return fmt.Errorf("migrations: %w", err)
	}
	if _, err := p.Up(ctx); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

func (s *Store) Close() error {
	s.R.Close()
	return s.W.Close()
}

// idCursor parses a page cursor that names an integer ID (0 = none).
func idCursor(before string) int64 {
	n, _ := strconv.ParseInt(before, 10, 64)
	return max(n, 0)
}
