package mail2bark

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
	"unicode"

	"golang.org/x/crypto/bcrypt"
)

type Store struct{ db *sql.DB }

func NewStore(db *sql.DB) *Store { return &Store{db: db} }

var (
	errNotFound          = errors.New("not found")
	errCredentialPending = errors.New("credential has pending messages")
	errDestinationInUse  = errors.New("destination is referenced by credentials")
)

func (s *Store) Migrate(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS smtp_credentials (
 id INTEGER PRIMARY KEY, name TEXT NOT NULL, username TEXT NOT NULL,
 password_hash TEXT NOT NULL, allowed_ips TEXT NOT NULL, recipients TEXT NOT NULL,
 destination_id INTEGER, enabled INTEGER NOT NULL DEFAULT 1, created_at DATETIME NOT NULL, last_used_at DATETIME, password TEXT
);
CREATE TABLE IF NOT EXISTS recipients (id INTEGER PRIMARY KEY, address TEXT NOT NULL UNIQUE, enabled INTEGER NOT NULL DEFAULT 1, created_at DATETIME NOT NULL);
CREATE TABLE IF NOT EXISTS destinations (id INTEGER PRIMARY KEY, name TEXT NOT NULL, server TEXT NOT NULL, device_key TEXT NOT NULL, group_name TEXT, sound TEXT, level TEXT, enabled INTEGER NOT NULL DEFAULT 1, created_at DATETIME NOT NULL);
CREATE TABLE IF NOT EXISTS messages (id INTEGER PRIMARY KEY, mail_from TEXT, rcpt_to TEXT NOT NULL, credential_id INTEGER, raw BLOB NOT NULL, subject TEXT, sender TEXT, status TEXT NOT NULL, attempts INTEGER NOT NULL DEFAULT 0, next_attempt_at DATETIME, last_error TEXT, created_at DATETIME NOT NULL, delivered_at DATETIME);
CREATE INDEX IF NOT EXISTS messages_due ON messages(status, next_attempt_at);
`)
	if err != nil {
		return err
	}
	// Older development databases used a UNIQUE username. Rebuild that table
	// once so every API key can use the shared SMTP username "mail2bark".
	var schema string
	_ = s.db.QueryRowContext(ctx, `SELECT sql FROM sqlite_master WHERE type='table' AND name='smtp_credentials'`).Scan(&schema)
	if strings.Contains(strings.ToUpper(schema), "USERNAME TEXT NOT NULL UNIQUE") {
		_, err = s.db.ExecContext(ctx, `ALTER TABLE smtp_credentials RENAME TO smtp_credentials_old;
CREATE TABLE smtp_credentials (id INTEGER PRIMARY KEY,name TEXT NOT NULL,username TEXT NOT NULL,password_hash TEXT NOT NULL,allowed_ips TEXT NOT NULL,recipients TEXT NOT NULL,destination_id INTEGER,enabled INTEGER NOT NULL DEFAULT 1,created_at DATETIME NOT NULL,last_used_at DATETIME);
INSERT INTO smtp_credentials(id,name,username,password_hash,allowed_ips,recipients,enabled,created_at,last_used_at) SELECT id,name,username,password_hash,allowed_ips,recipients,enabled,created_at,last_used_at FROM smtp_credentials_old;
DROP TABLE smtp_credentials_old;`)
		if err != nil {
			return err
		}
	}
	_, _ = s.db.ExecContext(ctx, `ALTER TABLE smtp_credentials ADD COLUMN destination_id INTEGER`)
	_, _ = s.db.ExecContext(ctx, `ALTER TABLE smtp_credentials ADD COLUMN password TEXT`)
	_, _ = s.db.ExecContext(ctx, `UPDATE smtp_credentials SET username='mail2bark'`)
	// A process killed during delivery leaves an in-flight row; make it eligible
	// again on startup so persisted alerts are never stranded.
	_, err = s.db.ExecContext(ctx, `UPDATE messages SET status='pending' WHERE status='processing'`)
	return err
}

type Credential struct {
	ID            int64     `json:"id"`
	Name          string    `json:"name"`
	Username      string    `json:"username"`
	AllowedIPs    []string  `json:"allowed_ips"`
	Recipients    []string  `json:"recipients"`
	DestinationID int64     `json:"destination_id,omitempty"`
	Enabled       bool      `json:"enabled"`
	CreatedAt     time.Time `json:"created_at"`
	Password      string    `json:"password,omitempty"`
}
type credentialRow struct {
	Credential
	PasswordHash string
}

func randomToken(prefix string) (string, error) {
	b := make([]byte, 24)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return prefix + hex.EncodeToString(b), nil
}
func hashSecret(v string) string {
	b, _ := bcrypt.GenerateFromPassword([]byte(v), bcrypt.DefaultCost)
	return string(b)
}
func checkSecret(hash, value string) bool {
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(value)) == nil
}
func encodeList(v []string) string { return strings.Join(v, "\n") }
func decodeList(v string) []string {
	if v == "" {
		return nil
	}
	return strings.Split(v, "\n")
}
func containsFold(xs []string, v string) bool {
	for _, x := range xs {
		if strings.EqualFold(strings.TrimSpace(x), strings.TrimSpace(v)) {
			return true
		}
	}
	return false
}

func recipientSlug(name string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(strings.TrimSpace(name)) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			dash = false
		} else if b.Len() > 0 && !dash {
			b.WriteByte('-')
			dash = true
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		slug = "alert"
	}
	return slug
}

func (s *Store) CreateCredential(ctx context.Context, name, domain string, ips []string, destinationID int64) (Credential, string, error) {
	user := "mail2bark"
	pass, err := randomToken("")
	if err != nil {
		return Credential{}, "", err
	}
	now := time.Now().UTC()
	addressDomain := strings.Trim(strings.ToLower(domain), " .")
	if addressDomain == "" {
		addressDomain = "notify.internal"
	}
	recipient := fmt.Sprintf("%s-%s@%s", recipientSlug(name), hex.EncodeToString(mustRandomBytes(2)), addressDomain)
	res, err := s.db.ExecContext(ctx, `INSERT INTO smtp_credentials(name,username,password_hash,allowed_ips,recipients,destination_id,created_at,password) VALUES(?,?,?,?,?,?,?,?)`, name, user, hashSecret(pass), encodeList(ips), recipient, destinationID, now, pass)
	if err != nil {
		return Credential{}, "", err
	}
	id, _ := res.LastInsertId()
	_, _ = s.db.ExecContext(ctx, `INSERT OR IGNORE INTO recipients(address,created_at) VALUES(?,?)`, recipient, now)
	return Credential{ID: id, Name: name, Username: user, AllowedIPs: ips, Recipients: []string{recipient}, DestinationID: destinationID, Enabled: true, CreatedAt: now, Password: pass}, pass, nil
}

func mustRandomBytes(n int) []byte { b := make([]byte, n); _, _ = rand.Read(b); return b }

func (s *Store) FindCredential(ctx context.Context, username, password string, ip net.IP) (credentialRow, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,username,password_hash,allowed_ips,recipients,COALESCE(destination_id,0),enabled,created_at FROM smtp_credentials WHERE username=?`, username)
	if err != nil {
		return credentialRow{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var c credentialRow
		var ips, rcpts string
		var enabled int
		if err := rows.Scan(&c.ID, &c.Name, &c.Username, &c.PasswordHash, &ips, &rcpts, &c.DestinationID, &enabled, &c.CreatedAt); err != nil {
			return credentialRow{}, err
		}
		c.AllowedIPs = decodeList(ips)
		c.Recipients = decodeList(rcpts)
		c.Enabled = enabled != 0
		allowed := false
		for _, cidr := range c.AllowedIPs {
			_, n, e := net.ParseCIDR(strings.TrimSpace(cidr))
			if e == nil && n.Contains(ip) {
				allowed = true
				break
			}
		}
		if c.Enabled && allowed && checkSecret(c.PasswordHash, password) {
			_, _ = s.db.ExecContext(ctx, `UPDATE smtp_credentials SET last_used_at=? WHERE id=?`, time.Now().UTC(), c.ID)
			return c, nil
		}
	}
	return credentialRow{}, errUnauthorized
}

func (s *Store) AddMessage(ctx context.Context, from string, rcpt string, credID int64, raw []byte, subject, sender string) (int64, error) {
	res, err := s.db.ExecContext(ctx, `INSERT INTO messages(mail_from,rcpt_to,credential_id,raw,subject,sender,status,next_attempt_at,created_at) VALUES(?,?,?,?,?,?,?, ?,?)`, from, rcpt, credID, raw, subject, sender, "pending", time.Now().UTC(), time.Now().UTC())
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

type Message struct {
	ID                                 int64
	From, To                           string
	CredentialID                       int64
	Raw                                []byte
	Subject, Sender, Status, LastError string
	Attempts                           int
	NextAttempt, CreatedAt             time.Time
}

func (s *Store) ClaimDue(ctx context.Context) (*Message, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var m Message
	var next sql.NullTime
	var lastErr sql.NullString
	err = tx.QueryRowContext(ctx, `SELECT id,mail_from,rcpt_to,credential_id,raw,subject,sender,status,attempts,next_attempt_at,created_at,last_error FROM messages WHERE status IN ('pending','retrying') AND (next_attempt_at IS NULL OR next_attempt_at<=?) ORDER BY id LIMIT 1`, time.Now().UTC()).Scan(&m.ID, &m.From, &m.To, &m.CredentialID, &m.Raw, &m.Subject, &m.Sender, &m.Status, &m.Attempts, &next, &m.CreatedAt, &lastErr)
	if err != nil {
		return nil, err
	}
	m.NextAttempt = next.Time
	m.LastError = lastErr.String
	if _, err = tx.ExecContext(ctx, `UPDATE messages SET status='processing', attempts=attempts+1 WHERE id=?`, m.ID); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	m.Attempts++
	return &m, nil
}
func (s *Store) MarkDelivered(ctx context.Context, id int64) {
	_, _ = s.db.ExecContext(ctx, `UPDATE messages SET status='delivered',delivered_at=?,last_error=NULL WHERE id=?`, time.Now().UTC(), id)
}
func (s *Store) MarkRetry(ctx context.Context, id int64, e error, attempts int) {
	delay := time.Duration(1<<min(attempts, 8)) * time.Second
	_, _ = s.db.ExecContext(ctx, `UPDATE messages SET status='retrying',next_attempt_at=?,last_error=? WHERE id=?`, time.Now().UTC().Add(delay), e.Error(), id)
}
func (s *Store) MarkDead(ctx context.Context, id int64, e error) {
	_, _ = s.db.ExecContext(ctx, `UPDATE messages SET status='dead_letter',last_error=? WHERE id=?`, e.Error(), id)
}
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
func (s *Store) ListCredentials(ctx context.Context) ([]Credential, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,username,allowed_ips,recipients,COALESCE(destination_id,0),enabled,created_at FROM smtp_credentials ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Credential
	for rows.Next() {
		var c Credential
		var ips, rs string
		var en int
		if err := rows.Scan(&c.ID, &c.Name, &c.Username, &ips, &rs, &c.DestinationID, &en, &c.CreatedAt); err != nil {
			return nil, err
		}
		c.AllowedIPs = decodeList(ips)
		c.Recipients = decodeList(rs)
		c.Enabled = en != 0
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) getCredentialRow(ctx context.Context, id int64) (credentialRow, error) {
	var c credentialRow
	var ips, recipients, password sql.NullString
	var enabled int
	err := s.db.QueryRowContext(ctx, `SELECT id,name,username,password_hash,allowed_ips,recipients,COALESCE(destination_id,0),enabled,created_at,password FROM smtp_credentials WHERE id=?`, id).Scan(
		&c.ID, &c.Name, &c.Username, &c.PasswordHash, &ips, &recipients, &c.DestinationID, &enabled, &c.CreatedAt, &password,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return credentialRow{}, errNotFound
	}
	if err != nil {
		return credentialRow{}, err
	}
	c.AllowedIPs = decodeList(ips.String)
	c.Recipients = decodeList(recipients.String)
	c.Enabled = enabled != 0
	c.Password = password.String
	return c, nil
}

func (s *Store) UpdateCredential(ctx context.Context, id int64, name string, ips []string, destinationID int64, enabled bool) (Credential, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE smtp_credentials SET name=?,allowed_ips=?,destination_id=?,enabled=? WHERE id=?`, name, encodeList(ips), destinationID, boolInt(enabled), id)
	if err != nil {
		return Credential{}, err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return Credential{}, errNotFound
	}
	c, err := s.getCredentialRow(ctx, id)
	return c.Credential, err
}

func (s *Store) RotateCredentialSecret(ctx context.Context, id int64) (Credential, string, error) {
	secret, err := randomToken("")
	if err != nil {
		return Credential{}, "", err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE smtp_credentials SET password_hash=?,password=? WHERE id=?`, hashSecret(secret), secret, id)
	if err != nil {
		return Credential{}, "", err
	}
	if affected, _ := result.RowsAffected(); affected == 0 {
		return Credential{}, "", errNotFound
	}
	c, err := s.getCredentialRow(ctx, id)
	return c.Credential, secret, err
}

func (s *Store) GetCredential(ctx context.Context, id int64) (Credential, error) {
	c, err := s.getCredentialRow(ctx, id)
	return c.Credential, err
}

func (s *Store) AuthenticateCredentialSecret(ctx context.Context, id int64, secret string) (Credential, error) {
	c, err := s.getCredentialRow(ctx, id)
	if err != nil {
		return Credential{}, err
	}
	if !c.Enabled || !checkSecret(c.PasswordHash, secret) {
		return Credential{}, errUnauthorized
	}
	return c.Credential, nil
}

func (s *Store) DeleteCredential(ctx context.Context, id int64) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	var recipients string
	if err = tx.QueryRowContext(ctx, `SELECT recipients FROM smtp_credentials WHERE id=?`, id).Scan(&recipients); errors.Is(err, sql.ErrNoRows) {
		return errNotFound
	} else if err != nil {
		return err
	}

	var pending int
	if err = tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM messages WHERE credential_id=? AND status IN ('pending','processing','retrying')`, id).Scan(&pending); err != nil {
		return err
	}
	if pending > 0 {
		return errCredentialPending
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM smtp_credentials WHERE id=?`, id); err != nil {
		return err
	}
	for _, recipient := range decodeList(recipients) {
		if _, err = tx.ExecContext(ctx, `DELETE FROM recipients WHERE lower(address)=lower(?)`, recipient); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
func (s *Store) IsRecipient(ctx context.Context, address string) bool {
	var enabled int
	err := s.db.QueryRowContext(ctx, `SELECT enabled FROM recipients WHERE lower(address)=lower(?)`, address).Scan(&enabled)
	return err == nil && enabled != 0
}
func (s *Store) ListMessages(ctx context.Context) ([]map[string]any, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,mail_from,rcpt_to,subject,status,attempts,last_error,created_at,delivered_at FROM messages ORDER BY id DESC LIMIT 100`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id, attempts int
		var from, to, subject, status string
		var le sql.NullString
		var created, delivered sql.NullTime
		if err := rows.Scan(&id, &from, &to, &subject, &status, &attempts, &le, &created, &delivered); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{"id": id, "from": from, "to": to, "subject": subject, "status": status, "attempts": attempts, "last_error": le.String, "created_at": created.Time, "delivered_at": delivered.Time})
	}
	return out, rows.Err()
}

func (s *Store) GetMessageDetail(ctx context.Context, id int64) (map[string]any, error) {
	var (
		raw                       []byte
		from, to, subject, sender sql.NullString
		status, lastError         sql.NullString
		attempts                  int
		createdAt, deliveredAt    sql.NullTime
	)
	err := s.db.QueryRowContext(ctx, `SELECT raw,mail_from,rcpt_to,subject,sender,status,attempts,last_error,created_at,delivered_at FROM messages WHERE id=?`, id).Scan(
		&raw, &from, &to, &subject, &sender, &status, &attempts, &lastError, &createdAt, &deliveredAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	alert := ParseAlert(raw, subject.String, sender.String)
	return map[string]any{
		"id": id, "from": from.String, "to": to.String, "subject": subject.String, "sender": sender.String,
		"status": status.String, "attempts": attempts, "last_error": lastError.String,
		"created_at": createdAt.Time, "delivered_at": deliveredAt.Time,
		"alert": alert, "raw": string(raw),
	}, nil
}

func (s *Store) RetryMessage(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `UPDATE messages SET status='retrying',next_attempt_at=?,last_error=NULL WHERE id=? AND status IN ('dead_letter','ignored')`, time.Now().UTC(), id)
	return err
}
