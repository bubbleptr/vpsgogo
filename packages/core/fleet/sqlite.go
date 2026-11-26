package fleet

import (
	"context"
	"fmt"
	"time"

	"github.com/egoist/mygo/plugins/sqlite"
)

// SQLite is the Repository the desktop app uses.
type SQLite struct {
	db *sqlite.DB
}

const schema = `CREATE TABLE IF NOT EXISTS servers (
	id          TEXT PRIMARY KEY,
	name        TEXT NOT NULL,
	host        TEXT NOT NULL,
	port        INTEGER NOT NULL,
	user        TEXT NOT NULL,
	host_key_fp TEXT NOT NULL,
	provider    TEXT NOT NULL,
	created_at  INTEGER NOT NULL
)`

// OpenSQLite opens (and creates if needed) the fleet database at path.
func OpenSQLite(ctx context.Context, path string) (*SQLite, error) {
	db, err := sqlite.Open(ctx, path, sqlite.OpenOptions{})
	if err != nil {
		return nil, fmt.Errorf("fleet: open %s: %w", path, err)
	}
	if _, err := db.Execute(ctx, schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("fleet: migrate: %w", err)
	}
	return &SQLite{db: db}, nil
}

func (r *SQLite) Close() error { return r.db.Close() }

func (r *SQLite) Insert(ctx context.Context, s Server) error {
	_, err := r.db.Execute(ctx,
		`INSERT INTO servers (id, name, host, port, user, host_key_fp, provider, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		s.ID, s.Name, s.Host, s.Port, s.User, s.HostKeyFP, s.Provider, s.CreatedAt.UnixMilli())
	return err
}

const columns = `id, name, host, port, user, host_key_fp, provider, created_at`

func (r *SQLite) Get(ctx context.Context, id string) (Server, error) {
	rows, err := r.db.Query(ctx, `SELECT `+columns+` FROM servers WHERE id = ?`, id)
	if err != nil {
		return Server{}, err
	}
	if len(rows.Values) == 0 {
		return Server{}, ErrNotFound
	}
	return scan(rows.Values[0])
}

func (r *SQLite) List(ctx context.Context) ([]Server, error) {
	rows, err := r.db.Query(ctx, `SELECT `+columns+` FROM servers ORDER BY rowid`)
	if err != nil {
		return nil, err
	}
	out := make([]Server, 0, len(rows.Values))
	for _, v := range rows.Values {
		s, err := scan(v)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

func (r *SQLite) Delete(ctx context.Context, id string) error {
	_, err := r.db.Execute(ctx, `DELETE FROM servers WHERE id = ?`, id)
	return err
}

func (r *SQLite) UpdateUser(ctx context.Context, id, user string) error {
	res, err := r.db.Execute(ctx, `UPDATE servers SET user = ? WHERE id = ?`, user, id)
	if err != nil {
		return err
	}
	if res.Changes == 0 {
		return ErrNotFound
	}
	return nil
}

func scan(v []any) (Server, error) {
	if len(v) != 8 {
		return Server{}, fmt.Errorf("fleet: row has %d columns", len(v))
	}
	str := func(i int) string { s, _ := v[i].(string); return s }
	num := func(i int) int64 { n, _ := v[i].(int64); return n }
	return Server{
		ID:        str(0),
		Name:      str(1),
		Host:      str(2),
		Port:      int(num(3)),
		User:      str(4),
		HostKeyFP: str(5),
		Provider:  str(6),
		CreatedAt: time.UnixMilli(num(7)).UTC(),
	}, nil
}
