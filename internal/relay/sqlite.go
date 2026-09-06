package relay

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite" // pure-Go SQLite driver

	"github.com/0x3639/nomctl/internal/alertproto"
)

const schema = `
CREATE TABLE IF NOT EXISTS codes (
  code TEXT PRIMARY KEY,
  chat_id INTEGER NOT NULL,
  expires INTEGER NOT NULL,
  used INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS nodes (
  id TEXT PRIMARY KEY,
  chat_id INTEGER NOT NULL,
  name TEXT NOT NULL,
  host TEXT NOT NULL,
  version TEXT NOT NULL,
  secret BLOB NOT NULL,
  created INTEGER NOT NULL,
  last_seen INTEGER NOT NULL,
  silent INTEGER NOT NULL DEFAULT 0,
  blocked INTEGER NOT NULL DEFAULT 0,
  summary TEXT NOT NULL DEFAULT '{}',
  UNIQUE(chat_id, name)
);
CREATE INDEX IF NOT EXISTS nodes_chat ON nodes(chat_id);
CREATE TABLE IF NOT EXISTS mutes (
  node_id TEXT NOT NULL,
  alert TEXT NOT NULL,
  until INTEGER NOT NULL,
  PRIMARY KEY(node_id, alert)
);
CREATE TABLE IF NOT EXISTS last_sent (
  node_id TEXT NOT NULL,
  alert TEXT NOT NULL,
  state TEXT NOT NULL,
  at INTEGER NOT NULL,
  PRIMARY KEY(node_id, alert)
);
`

type sqliteStore struct {
	db *sql.DB
}

// OpenSQLite opens (creating if needed) the relay database at path.
func OpenSQLite(path string) (Store, error) {
	db, err := sql.Open("sqlite", path+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec(schema); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("init schema: %w", err)
	}
	return &sqliteStore{db: db}, nil
}

func (s *sqliteStore) Close() error { return s.db.Close() }

func (s *sqliteStore) CreateCode(ctx context.Context, chatID int64, code string, expires time.Time) error {
	var unused int
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM codes WHERE chat_id=? AND used=0 AND expires>?`, chatID, time.Now().Unix()).Scan(&unused); err != nil {
		return err
	}
	if unused >= MaxCodesPerChat {
		return ErrTooManyCodes
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO codes(code, chat_id, expires) VALUES(?,?,?)`, code, chatID, expires.Unix())
	return err
}

func (s *sqliteStore) ConsumeCode(ctx context.Context, code string, now time.Time) (int64, error) {
	var chatID int64
	err := s.db.QueryRowContext(ctx, `SELECT chat_id FROM codes WHERE code=? AND used=0 AND expires>?`, code, now.Unix()).Scan(&chatID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrCodeInvalid
	}
	if err != nil {
		return 0, err
	}
	res, err := s.db.ExecContext(ctx, `UPDATE codes SET used=1 WHERE code=? AND used=0`, code)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return 0, ErrCodeInvalid
	}
	// Opportunistic cleanup of expired codes.
	_, _ = s.db.ExecContext(ctx, `DELETE FROM codes WHERE expires<?`, now.Add(-24*time.Hour).Unix())
	return chatID, nil
}

func (s *sqliteStore) CreateNode(ctx context.Context, n Node) error {
	summary, _ := json.Marshal(n.Summary)
	_, err := s.db.ExecContext(ctx, `INSERT INTO nodes(id, chat_id, name, host, version, secret, created, last_seen, silent, blocked, summary) VALUES(?,?,?,?,?,?,?,?,?,?,?)`,
		n.ID, n.ChatID, n.Name, n.Host, n.Version, n.Secret, n.Created.Unix(), n.LastSeen.Unix(), n.Silent, n.Blocked, string(summary))
	if err != nil && strings.Contains(err.Error(), "UNIQUE") {
		return ErrNameTaken
	}
	return err
}

const nodeColumns = `id, chat_id, name, host, version, secret, created, last_seen, silent, blocked, summary`

func scanNode(row interface{ Scan(dest ...any) error }) (Node, error) {
	var n Node
	var created, lastSeen int64
	var summary string
	if err := row.Scan(&n.ID, &n.ChatID, &n.Name, &n.Host, &n.Version, &n.Secret, &created, &lastSeen, &n.Silent, &n.Blocked, &summary); err != nil {
		return n, err
	}
	n.Created = time.Unix(created, 0).UTC()
	n.LastSeen = time.Unix(lastSeen, 0).UTC()
	_ = json.Unmarshal([]byte(summary), &n.Summary)
	return n, nil
}

func (s *sqliteStore) GetNode(ctx context.Context, id string) (Node, error) {
	n, err := scanNode(s.db.QueryRowContext(ctx, `SELECT `+nodeColumns+` FROM nodes WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Node{}, ErrNotFound
	}
	return n, err
}

func (s *sqliteStore) ListNodes(ctx context.Context, chatID int64) ([]Node, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+nodeColumns+` FROM nodes WHERE chat_id=? ORDER BY name`, chatID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Node
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *sqliteStore) UpdateNode(ctx context.Context, n Node) error {
	summary, _ := json.Marshal(n.Summary)
	res, err := s.db.ExecContext(ctx, `UPDATE nodes SET name=?, host=?, version=?, last_seen=?, silent=?, blocked=?, summary=? WHERE id=?`,
		n.Name, n.Host, n.Version, n.LastSeen.Unix(), n.Silent, n.Blocked, string(summary), n.ID)
	if err != nil {
		return err
	}
	if c, _ := res.RowsAffected(); c == 0 {
		return ErrNotFound
	}
	return nil
}

func (s *sqliteStore) DeleteNode(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM nodes WHERE id=?`, id)
	if err != nil {
		return err
	}
	if c, _ := res.RowsAffected(); c == 0 {
		return ErrNotFound
	}
	_, _ = s.db.ExecContext(ctx, `DELETE FROM mutes WHERE node_id=?`, id)
	_, _ = s.db.ExecContext(ctx, `DELETE FROM last_sent WHERE node_id=?`, id)
	return nil
}

func (s *sqliteStore) SilentCandidates(ctx context.Context, before time.Time) ([]Node, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+nodeColumns+` FROM nodes WHERE silent=0 AND last_seen<? ORDER BY id`, before.Unix())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []Node
	for rows.Next() {
		n, err := scanNode(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func (s *sqliteStore) SetMute(ctx context.Context, m Mute) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO mutes(node_id, alert, until) VALUES(?,?,?) ON CONFLICT(node_id, alert) DO UPDATE SET until=excluded.until`, m.NodeID, m.Alert, m.Until.Unix())
	return err
}

func (s *sqliteStore) ClearMute(ctx context.Context, nodeID, alert string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM mutes WHERE node_id=? AND alert=?`, nodeID, alert)
	return err
}

func (s *sqliteStore) Muted(ctx context.Context, nodeID, alert string, now time.Time) (bool, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM mutes WHERE node_id=? AND alert IN (?, 'all') AND until>?`, nodeID, alert, now.Unix()).Scan(&n)
	return n > 0, err
}

func (s *sqliteStore) LastSent(ctx context.Context, nodeID, alert string) (alertproto.State, time.Time, error) {
	var state string
	var at int64
	err := s.db.QueryRowContext(ctx, `SELECT state, at FROM last_sent WHERE node_id=? AND alert=?`, nodeID, alert).Scan(&state, &at)
	if errors.Is(err, sql.ErrNoRows) {
		return "", time.Time{}, nil
	}
	if err != nil {
		return "", time.Time{}, err
	}
	return alertproto.State(state), time.Unix(at, 0).UTC(), nil
}

func (s *sqliteStore) SetLastSent(ctx context.Context, nodeID, alert string, state alertproto.State, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO last_sent(node_id, alert, state, at) VALUES(?,?,?,?) ON CONFLICT(node_id, alert) DO UPDATE SET state=excluded.state, at=excluded.at`, nodeID, alert, string(state), at.Unix())
	return err
}
