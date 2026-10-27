// Package store keeps orders in SQLite (pure Go, no cgo): one file, WAL journal, backups by copying it.
// Everything above it talks to order.Store, so a PostgreSQL store can replace it without touching the rest.
package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite" // database/sql driver "sqlite"

	"github.com/Dmitry-Fedotov-Dev/joblox/internal/money"
	"github.com/Dmitry-Fedotov-Dev/joblox/internal/order"
)

type SQLite struct {
	db *sql.DB
}

var _ order.Store = (*SQLite)(nil)

// Open opens (and creates) the database file and brings the schema up to date.
// path ":memory:" gives a private in-memory database for tests.
func Open(path string) (*SQLite, error) {
	dsn := "file:" + path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=foreign_keys(1)&_pragma=synchronous(NORMAL)"
	if path == ":memory:" {
		dsn = "file::memory:?_pragma=foreign_keys(1)"
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// SQLite has one writer; a single connection keeps writes ordered and avoids SQLITE_BUSY storms.
	// Reads are short, so this is not the bottleneck for one shop (see docs/ARCHITECTURE.md).
	db.SetMaxOpenConns(1)
	s := &SQLite{db: db}
	if err := s.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *SQLite) Close() error { return s.db.Close() }

var migrations = []string{
	`CREATE TABLE orders (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		token TEXT NOT NULL UNIQUE,
		state TEXT NOT NULL,
		lines TEXT NOT NULL,
		fees TEXT NOT NULL,
		fields TEXT NOT NULL,
		email TEXT NOT NULL,
		method TEXT NOT NULL,
		total INTEGER NOT NULL,
		priced INTEGER NOT NULL,
		paid INTEGER NOT NULL DEFAULT 0,
		paid_at INTEGER,
		paid_note TEXT NOT NULL DEFAULT '',
		verified_at INTEGER,
		code_hash TEXT NOT NULL DEFAULT '',
		code_expires INTEGER NOT NULL DEFAULT 0,
		code_sent_at INTEGER NOT NULL DEFAULT 0,
		code_sends INTEGER NOT NULL DEFAULT 0,
		code_attempts INTEGER NOT NULL DEFAULT 0,
		review INTEGER NOT NULL,
		approved INTEGER NOT NULL,
		risk INTEGER NOT NULL,
		risk_reasons TEXT NOT NULL,
		client_hash TEXT NOT NULL,
		created_at INTEGER NOT NULL,
		updated_at INTEGER NOT NULL,
		version INTEGER NOT NULL
	);
	CREATE INDEX orders_email ON orders(email, created_at);
	CREATE INDEX orders_client ON orders(client_hash, created_at);
	CREATE INDEX orders_state ON orders(state, updated_at);
	CREATE TABLE events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		order_id INTEGER NOT NULL REFERENCES orders(id),
		at INTEGER NOT NULL,
		actor TEXT NOT NULL,
		actor_id TEXT NOT NULL,
		kind TEXT NOT NULL,
		from_state TEXT NOT NULL,
		to_state TEXT NOT NULL,
		note TEXT NOT NULL
	);
	CREATE INDEX events_order ON events(order_id, id);
	-- the journal is append-only: nobody edits history, not even a bug
	CREATE TRIGGER events_no_update BEFORE UPDATE ON events BEGIN SELECT RAISE(ABORT, 'events are append-only'); END;
	CREATE TRIGGER events_no_delete BEFORE DELETE ON events BEGIN SELECT RAISE(ABORT, 'events are append-only'); END;
	CREATE TABLE blocklist (
		value TEXT PRIMARY KEY,
		note TEXT NOT NULL,
		created_at INTEGER NOT NULL
	);`,
}

func (s *SQLite) migrate(ctx context.Context) error {
	if _, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_version (v INTEGER NOT NULL)`); err != nil {
		return err
	}
	var v int
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(v), 0) FROM schema_version`).Scan(&v); err != nil {
		return err
	}
	for i := v; i < len(migrations); i++ {
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, migrations[i]); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("migration %d: %w", i+1, err)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO schema_version (v) VALUES (?)`, i+1); err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}

const columns = `id, token, state, lines, fees, fields, email, method, total, priced, paid, paid_at, paid_note,
	verified_at, code_hash, code_expires, code_sent_at, code_sends, code_attempts, review, approved, risk,
	risk_reasons, client_hash, created_at, updated_at, version`

func unix(t time.Time) int64 { return t.UnixMilli() }

func fromUnix(v int64) time.Time {
	if v == 0 {
		return time.Time{}
	}
	return time.UnixMilli(v).UTC()
}

func optUnix(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UnixMilli()
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func encode(o *order.Order) (lines, fees, fields, reasons string, err error) {
	parts := make([]string, 4)
	for i, v := range []any{o.Lines, o.Fees, o.Fields, o.RiskReasons} {
		raw, e := json.Marshal(v)
		if e != nil {
			return "", "", "", "", e
		}
		parts[i] = string(raw)
	}
	return parts[0], parts[1], parts[2], parts[3], nil
}

func (s *SQLite) Create(ctx context.Context, o *order.Order, ev order.Event) error {
	lines, fees, fields, reasons, err := encode(o)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	o.Version = 1
	res, err := tx.ExecContext(ctx, `INSERT INTO orders (token, state, lines, fees, fields, email, method, total, priced,
		paid, paid_at, paid_note, verified_at, code_hash, code_expires, code_sent_at, code_sends, code_attempts, review,
		approved, risk, risk_reasons, client_hash, created_at, updated_at, version)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		o.Token, o.State, lines, fees, fields, o.Email, o.Method, int64(o.Total), b2i(o.Priced), b2i(o.Paid),
		optUnix(o.PaidAt), o.PaidNote, optUnix(o.VerifiedAt), o.CodeHash, unix(o.CodeExpires), unix(o.CodeSentAt),
		o.CodeSends, o.CodeAttempts, b2i(o.Review), b2i(o.Approved), o.Risk, reasons, o.ClientHash,
		unix(o.CreatedAt), unix(o.UpdatedAt), o.Version)
	if err != nil {
		return err
	}
	if o.ID, err = res.LastInsertId(); err != nil {
		return err
	}
	ev.OrderID = o.ID
	if err := addEvent(ctx, tx, ev); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *SQLite) Save(ctx context.Context, o *order.Order, ev order.Event) error {
	lines, fees, fields, reasons, err := encode(o)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	res, err := tx.ExecContext(ctx, `UPDATE orders SET state=?, lines=?, fees=?, fields=?, email=?, method=?, total=?,
		priced=?, paid=?, paid_at=?, paid_note=?, verified_at=?, code_hash=?, code_expires=?, code_sent_at=?,
		code_sends=?, code_attempts=?, review=?, approved=?, risk=?, risk_reasons=?, updated_at=?, version=version+1
		WHERE id=? AND version=?`,
		o.State, lines, fees, fields, o.Email, o.Method, int64(o.Total), b2i(o.Priced), b2i(o.Paid), optUnix(o.PaidAt),
		o.PaidNote, optUnix(o.VerifiedAt), o.CodeHash, unix(o.CodeExpires), unix(o.CodeSentAt), o.CodeSends,
		o.CodeAttempts, b2i(o.Review), b2i(o.Approved), o.Risk, reasons, unix(o.UpdatedAt), o.ID, o.Version)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n != 1 {
		return order.ErrConflict
	}
	if ev.Kind != "" {
		ev.OrderID = o.ID
		if err := addEvent(ctx, tx, ev); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	o.Version++
	return nil
}

func addEvent(ctx context.Context, tx *sql.Tx, ev order.Event) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO events (order_id, at, actor, actor_id, kind, from_state, to_state, note)
		VALUES (?,?,?,?,?,?,?,?)`, ev.OrderID, unix(ev.At), ev.Actor, ev.ActorID, ev.Kind, ev.From, ev.To, ev.Note)
	return err
}

type scanner interface{ Scan(dest ...any) error }

func scan(r scanner) (*order.Order, error) {
	var (
		o                                       order.Order
		lines, fees, fields, reasons            string
		total                                   int64
		priced, paid, review, approved          int
		paidAt, verifiedAt                      sql.NullInt64
		codeExpires, codeSent, created, updated int64
	)
	err := r.Scan(&o.ID, &o.Token, &o.State, &lines, &fees, &fields, &o.Email, &o.Method, &total, &priced, &paid,
		&paidAt, &o.PaidNote, &verifiedAt, &o.CodeHash, &codeExpires, &codeSent, &o.CodeSends, &o.CodeAttempts,
		&review, &approved, &o.Risk, &reasons, &o.ClientHash, &created, &updated, &o.Version)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, order.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	for _, c := range []struct {
		raw string
		dst any
	}{{lines, &o.Lines}, {fees, &o.Fees}, {fields, &o.Fields}, {reasons, &o.RiskReasons}} {
		if err := json.Unmarshal([]byte(c.raw), c.dst); err != nil {
			return nil, err
		}
	}
	o.Total, o.Priced, o.Paid, o.Review, o.Approved = money.Amount(total), priced == 1, paid == 1, review == 1, approved == 1
	if paidAt.Valid {
		t := fromUnix(paidAt.Int64)
		o.PaidAt = &t
	}
	if verifiedAt.Valid {
		t := fromUnix(verifiedAt.Int64)
		o.VerifiedAt = &t
	}
	o.CodeExpires, o.CodeSentAt = fromUnix(codeExpires), fromUnix(codeSent)
	o.CreatedAt, o.UpdatedAt = fromUnix(created), fromUnix(updated)
	return &o, nil
}

func (s *SQLite) Get(ctx context.Context, id int64) (*order.Order, error) {
	return scan(s.db.QueryRowContext(ctx, `SELECT `+columns+` FROM orders WHERE id=?`, id))
}

func (s *SQLite) ByToken(ctx context.Context, token string) (*order.Order, error) {
	return scan(s.db.QueryRowContext(ctx, `SELECT `+columns+` FROM orders WHERE token=?`, token))
}

func (s *SQLite) Events(ctx context.Context, orderID int64) ([]order.Event, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, order_id, at, actor, actor_id, kind, from_state, to_state, note
		FROM events WHERE order_id=? ORDER BY id`, orderID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []order.Event
	for rows.Next() {
		var ev order.Event
		var at int64
		if err := rows.Scan(&ev.ID, &ev.OrderID, &at, &ev.Actor, &ev.ActorID, &ev.Kind, &ev.From, &ev.To, &ev.Note); err != nil {
			return nil, err
		}
		ev.At = fromUnix(at)
		out = append(out, ev)
	}
	return out, rows.Err()
}

func placeholders(n int) string { return strings.TrimSuffix(strings.Repeat("?,", n), ",") }

func anySlice(v []string) []any {
	out := make([]any, len(v))
	for i, s := range v {
		out[i] = s
	}
	return out
}

func notFinal(finals []string) (string, []any) {
	if len(finals) == 0 {
		return "1=1", nil
	}
	return "state NOT IN (" + placeholders(len(finals)) + ")", anySlice(finals)
}

func (s *SQLite) List(ctx context.Context, f order.Filter, finals []string) ([]*order.Order, error) {
	where, args := []string{"1=1"}, []any{}
	if f.Open {
		w, a := notFinal(finals)
		where, args = append(where, w), append(args, a...)
	}
	if len(f.States) > 0 {
		where = append(where, "state IN ("+placeholders(len(f.States))+")")
		args = append(args, anySlice(f.States)...)
	}
	if f.Review {
		where = append(where, "review=1 AND approved=0")
	}
	limit := f.Limit
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, `SELECT `+columns+` FROM orders WHERE `+strings.Join(where, " AND ")+ // #nosec G202 -- only placeholders and fixed fragments are concatenated, values go as arguments
		` ORDER BY id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*order.Order
	for rows.Next() {
		o, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func (s *SQLite) Counts(ctx context.Context, email, clientHash string, total money.Amount, finals []string, now time.Time) (order.Counts, error) {
	var c order.Counts
	hour, day := unix(now.Add(-time.Hour)), unix(now.Add(-24*time.Hour))
	w, fa := notFinal(finals)
	q := `SELECT
		(SELECT COUNT(*) FROM orders WHERE client_hash=? AND created_at>=?),
		(SELECT COUNT(*) FROM orders WHERE email=? AND created_at>=?),
		(SELECT COUNT(*) FROM orders WHERE email=? AND paid=0 AND ` + w + `),
		(SELECT COUNT(*) FROM orders WHERE email=? AND paid=1),
		(SELECT COUNT(*) FROM orders WHERE email=? AND total=? AND created_at>=?)`
	args := append([]any{clientHash, hour, email, day, email}, fa...)
	args = append(args, email, email, int64(total), hour)
	err := s.db.QueryRowContext(ctx, q, args...).Scan(&c.ByClientHour, &c.ByEmailDay, &c.OpenUnpaid, &c.PaidByEmail, &c.DuplicateHour) // #nosec G202 -- only placeholders and fixed fragments are concatenated, values go as arguments
	return c, err
}

func (s *SQLite) Stale(ctx context.Context, states []string, before time.Time) ([]*order.Order, error) {
	if len(states) == 0 {
		return nil, nil
	}
	args := append(anySlice(states), unix(before))
	rows, err := s.db.QueryContext(ctx, `SELECT `+columns+` FROM orders WHERE state IN (`+placeholders(len(states))+ // #nosec G202 -- only placeholders and fixed fragments are concatenated, values go as arguments
		`) AND paid=0 AND updated_at<? ORDER BY id LIMIT 500`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*order.Order
	for rows.Next() {
		o, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func (s *SQLite) IsBlocked(ctx context.Context, values ...string) (bool, error) {
	var nonEmpty []string
	for _, v := range values {
		if v != "" {
			nonEmpty = append(nonEmpty, v)
		}
	}
	if len(nonEmpty) == 0 {
		return false, nil
	}
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM blocklist WHERE value IN (`+placeholders(len(nonEmpty))+`)`, // #nosec G202 -- only placeholders and fixed fragments are concatenated, values go as arguments
		anySlice(nonEmpty)...).Scan(&n)
	return n > 0, err
}

func (s *SQLite) Block(ctx context.Context, value, note string) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO blocklist (value, note, created_at) VALUES (?,?,?)
		ON CONFLICT(value) DO UPDATE SET note=excluded.note`, value, note, unix(time.Now()))
	return err
}

func (s *SQLite) Unblock(ctx context.Context, value string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM blocklist WHERE value=?`, value)
	return err
}

func (s *SQLite) Stats(ctx context.Context, since time.Time, finals []string) (order.Stats, error) {
	var st order.Stats
	var revenue int64
	w, fa := notFinal(finals)
	from := unix(since)
	q := `SELECT
		(SELECT COUNT(*) FROM orders WHERE created_at>=?),
		(SELECT COUNT(*) FROM orders WHERE ` + w + `),
		(SELECT COUNT(*) FROM orders WHERE review=1 AND approved=0 AND ` + w + `),
		(SELECT COUNT(*) FROM orders WHERE paid=1 AND paid_at>=?),
		(SELECT COALESCE(SUM(total),0) FROM orders WHERE paid=1 AND paid_at>=?)`
	args := append([]any{from}, fa...)
	args = append(args, fa...)
	args = append(args, from, from)
	err := s.db.QueryRowContext(ctx, q, args...).Scan(&st.Today, &st.Open, &st.Review, &st.PaidToday, &revenue) // #nosec G202 -- only placeholders and fixed fragments are concatenated, values go as arguments
	st.RevenueDay = money.Amount(revenue)
	return st, err
}
