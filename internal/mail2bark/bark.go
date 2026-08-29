package mail2bark

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

type Destination struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Server    string    `json:"server"`
	DeviceKey string    `json:"device_key"`
	Group     string    `json:"group"`
	Sound     string    `json:"sound"`
	Level     string    `json:"level"`
	Enabled   bool      `json:"enabled"`
	CreatedAt time.Time `json:"created_at"`
}
type BarkClient struct {
	HTTP *http.Client
	Log  *slog.Logger
}

func NewBarkClient(timeout time.Duration, log *slog.Logger) *BarkClient {
	return &BarkClient{HTTP: &http.Client{Timeout: timeout}, Log: log}
}
func (b *BarkClient) Send(ctx context.Context, d Destination, a Alert) error {
	payload := map[string]string{"title": a.Title, "body": a.Body}
	if d.Group != "" {
		payload["group"] = d.Group
	}
	if d.Sound != "" {
		payload["sound"] = d.Sound
	}
	if d.Level != "" {
		payload["level"] = d.Level
	}
	data, _ := json.Marshal(payload)
	endpoint := strings.TrimRight(d.Server, "/") + "/" + d.DeviceKey
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := b.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)
	if resp.StatusCode >= 500 || resp.StatusCode == 429 {
		return fmt.Errorf("temporary Bark response: %s", resp.Status)
	}
	if resp.StatusCode >= 400 {
		return fmt.Errorf("permanent Bark response: %s", resp.Status)
	}
	return nil
}

func (s *Store) ListDestinations(ctx context.Context) ([]Destination, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,server,device_key,group_name,sound,level,enabled,created_at FROM destinations WHERE enabled=1 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Destination
	for rows.Next() {
		var d Destination
		var en int
		if err := rows.Scan(&d.ID, &d.Name, &d.Server, &d.DeviceKey, &d.Group, &d.Sound, &d.Level, &en, &d.CreatedAt); err != nil {
			return nil, err
		}
		d.Enabled = en != 0
		out = append(out, d)
	}
	return out, rows.Err()
}
func (s *Store) ListDestinationsForCredential(ctx context.Context, credentialID int64) ([]Destination, error) {
	var destinationID int64
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(destination_id,0) FROM smtp_credentials WHERE id=?`, credentialID).Scan(&destinationID); err != nil { return nil, err }
	if destinationID == 0 { return s.ListDestinations(ctx) }
	rows, err := s.db.QueryContext(ctx, `SELECT id,name,server,device_key,group_name,sound,level,enabled,created_at FROM destinations WHERE id=? AND enabled=1`, destinationID)
	if err != nil { return nil, err }
	defer rows.Close(); var out []Destination
	for rows.Next() { var d Destination; var en int; if err:=rows.Scan(&d.ID,&d.Name,&d.Server,&d.DeviceKey,&d.Group,&d.Sound,&d.Level,&en,&d.CreatedAt);err!=nil{return nil,err};d.Enabled=en!=0;out=append(out,d) }
	return out, rows.Err()
}
func (s *Store) CreateDestination(ctx context.Context, d Destination) (Destination, error) {
	now := time.Now().UTC()
	res, err := s.db.ExecContext(ctx, `INSERT INTO destinations(name,server,device_key,group_name,sound,level,created_at) VALUES(?,?,?,?,?,?,?)`, d.Name, strings.TrimRight(d.Server, "/"), d.DeviceKey, d.Group, d.Sound, d.Level, now)
	if err != nil {
		return Destination{}, err
	}
	id, _ := res.LastInsertId()
	d.ID, d.Enabled, d.CreatedAt = id, true, now
	return d, nil
}

type Worker struct {
	Store *Store
	Bark  *BarkClient
	Log   *slog.Logger
}

func NewWorker(s *Store, b *BarkClient, l *slog.Logger) *Worker {
	return &Worker{Store: s, Bark: b, Log: l}
}
func (w *Worker) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		m, err := w.Store.ClaimDue(ctx)
		if err != nil {
			if err == sql.ErrNoRows {
				select {
				case <-ctx.Done():
					return
				case <-time.After(time.Second):
					continue
				}
			}
			w.Log.Error("claim message", "error", err)
			time.Sleep(time.Second)
			continue
		}
		a := ParseAlert(m.Raw, m.Subject, m.Sender)
		ds, err := w.Store.ListDestinationsForCredential(ctx, m.CredentialID)
		if err == nil {
			for _, d := range ds {
				if e := w.Bark.Send(ctx, d, a); e != nil {
					err = e
					break
				}
			}
		}
		if err == nil && len(ds) == 0 {
			err = fmt.Errorf("no enabled Bark destination")
		}
		if err == nil {
			w.Store.MarkDelivered(ctx, m.ID)
			w.Log.Info("message delivered", "id", m.ID)
		} else if m.Attempts >= 8 {
			w.Store.MarkDead(ctx, m.ID, err)
			w.Log.Error("message dead-lettered", "id", m.ID, "error", err)
		} else {
			w.Store.MarkRetry(ctx, m.ID, err, m.Attempts)
			w.Log.Warn("message retry scheduled", "id", m.ID, "error", err)
		}
	}
}
