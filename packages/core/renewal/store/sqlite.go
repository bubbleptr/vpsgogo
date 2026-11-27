// Package store keeps the desktop app's renewal plans in SQLite. It is apart
// from package renewal so gogo-agent, which uses renewal, links no SQLite.
package store

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/bubbleptr/vpsgogo/packages/core/renewal"
	"github.com/egoist/mygo/plugins/sqlite"
)

// ErrNotFound is returned for a server with no plan recorded.
var ErrNotFound = errors.New("renewal: no plan for this server")

// SQLite stores one plan per server. It can share a file with fleet.SQLite.
type SQLite struct {
	db *sqlite.DB
}

// A plan is stored as its JSON, the same form the agent receives.
const schema = `CREATE TABLE IF NOT EXISTS subscriptions (
	server_id TEXT PRIMARY KEY,
	data      TEXT NOT NULL
)`

// OpenSQLite opens (and creates if needed) the plans table in the database
// at path.
func OpenSQLite(ctx context.Context, path string) (*SQLite, error) {
	db, err := sqlite.Open(ctx, path, sqlite.OpenOptions{})
	if err != nil {
		return nil, fmt.Errorf("renewal: open %s: %w", path, err)
	}
	if _, err := db.Execute(ctx, schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("renewal: migrate: %w", err)
	}
	return &SQLite{db: db}, nil
}

func (r *SQLite) Close() error { return r.db.Close() }

// Put records s, replacing the server's plan.
func (r *SQLite) Put(ctx context.Context, s renewal.Subscription) error {
	if err := s.Validate(); err != nil {
		return err
	}
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	_, err = r.db.Execute(ctx,
		`INSERT INTO subscriptions (server_id, data) VALUES (?, ?)
		 ON CONFLICT(server_id) DO UPDATE SET data = excluded.data`, s.ServerID, string(data))
	return err
}

func (r *SQLite) Get(ctx context.Context, serverID string) (renewal.Subscription, error) {
	rows, err := r.db.Query(ctx, `SELECT data FROM subscriptions WHERE server_id = ?`, serverID)
	if err != nil {
		return renewal.Subscription{}, err
	}
	if len(rows.Values) == 0 {
		return renewal.Subscription{}, ErrNotFound
	}
	return decode(rows.Values[0])
}

func (r *SQLite) List(ctx context.Context) ([]renewal.Subscription, error) {
	rows, err := r.db.Query(ctx, `SELECT data FROM subscriptions ORDER BY rowid`)
	if err != nil {
		return nil, err
	}
	out := make([]renewal.Subscription, 0, len(rows.Values))
	for _, v := range rows.Values {
		s, err := decode(v)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, nil
}

func (r *SQLite) Delete(ctx context.Context, serverID string) error {
	_, err := r.db.Execute(ctx, `DELETE FROM subscriptions WHERE server_id = ?`, serverID)
	return err
}

// Renew records that the user paid for one more cycle and returns the plan
// with its new expiry, which the caller then syncs to the server's agent.
func (r *SQLite) Renew(ctx context.Context, serverID string) (renewal.Subscription, error) {
	s, err := r.Get(ctx, serverID)
	if err != nil {
		return s, err
	}
	if s, err = s.Renewed(); err != nil {
		return s, err
	}
	return s, r.Put(ctx, s)
}

func decode(v []any) (renewal.Subscription, error) {
	var s renewal.Subscription
	data, _ := v[0].(string)
	if err := json.Unmarshal([]byte(data), &s); err != nil {
		return s, fmt.Errorf("renewal: stored plan: %w", err)
	}
	return s, nil
}
