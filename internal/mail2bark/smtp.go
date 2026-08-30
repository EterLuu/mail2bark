package mail2bark

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/mail"
	"os"
	"strings"

	"github.com/emersion/go-sasl"
	"github.com/emersion/go-smtp"
)

type SMTPBackend struct {
	Store   *Store
	MaxSize int64
	Log     *slog.Logger
}
type SMTPSession struct {
	backend       *SMTPBackend
	ip            net.IP
	username      string
	credential    credentialRow
	authenticated bool
	from          string
	rcpt          string
}

func (b *SMTPBackend) NewSession(c *smtp.Conn) (smtp.Session, error) {
	return &SMTPSession{backend: b, ip: parseClientIP(c.Conn().RemoteAddr().String())}, nil
}
func (s *SMTPSession) AuthMechanisms() []string { return []string{"PLAIN"} }
func (s *SMTPSession) Auth(mech string) (sasl.Server, error) {
	if !strings.EqualFold(mech, "PLAIN") {
		return nil, smtp.ErrAuthUnknownMechanism
	}
	return sasl.NewPlainServer(func(identity, username, password string) error {
		c, err := s.backend.Store.FindCredential(context.Background(), username, password, s.ip)
		if err != nil {
			return smtp.ErrAuthFailed
		}
		s.username, s.credential, s.authenticated = username, c, true
		return nil
	}), nil
}
func (s *SMTPSession) Mail(from string, _ *smtp.MailOptions) error {
	if !s.authenticated {
		return smtp.ErrAuthRequired
	}
	s.from = strings.TrimSpace(from)
	return nil
}
func (s *SMTPSession) Rcpt(to string, _ *smtp.RcptOptions) error {
	if !s.authenticated {
		return smtp.ErrAuthRequired
	}
	to = strings.Trim(strings.TrimSpace(to), "<>")
	if strings.Contains(to, "\"") || !strings.Contains(to, "@") {
		return fmt.Errorf("invalid recipient")
	}
	if !containsFold(s.credential.Recipients, to) || !s.backend.Store.IsRecipient(context.Background(), to) {
		return fmt.Errorf("recipient not authorized")
	}
	s.rcpt = strings.ToLower(to)
	return nil
}
func (s *SMTPSession) Data(r io.Reader) error {
	if s.rcpt == "" {
		return fmt.Errorf("no recipient")
	}
	limited := io.LimitReader(r, s.backend.MaxSize+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return err
	}
	if int64(len(raw)) > s.backend.MaxSize {
		return fmt.Errorf("message too large")
	}
	subject, sender := extractHeaders(raw)
	if _, err := s.backend.Store.AddMessage(context.Background(), s.from, s.rcpt, s.credential.ID, raw, subject, sender); err != nil {
		s.backend.Log.Error("queue message", "error", err)
		return fmt.Errorf("temporary queue failure")
	}
	return nil
}
func (s *SMTPSession) Reset()        { s.from = ""; s.rcpt = "" }
func (s *SMTPSession) Logout() error { return nil }
func extractHeaders(raw []byte) (subject, sender string) {
	m, err := mail.ReadMessage(strings.NewReader(string(raw)))
	if err != nil {
		return "", ""
	}
	subject = decodeHeaderValue(m.Header.Get("Subject"))
	sender = decodeHeaderValue(m.Header.Get("From"))
	return
}

type SMTPServerRunner struct {
	Addr        string
	Backend     *SMTPBackend
	Log         *slog.Logger
	ImplicitTLS bool
}

func NewSMTPServerRunner(addr string, b *SMTPBackend, l *slog.Logger, implicit ...bool) *SMTPServerRunner {
	r := &SMTPServerRunner{Addr: addr, Backend: b, Log: l}
	if len(implicit) > 0 {
		r.ImplicitTLS = implicit[0]
	}
	return r
}
func (r *SMTPServerRunner) ListenAndServe(ctx context.Context) error {
	l, err := net.Listen("tcp", r.Addr)
	if err != nil {
		return err
	}
	s := smtp.NewServer(r.Backend)
	s.Addr = r.Addr
	s.Domain = "mail2bark"
	s.MaxMessageBytes = r.Backend.MaxSize
	s.MaxRecipients = 8
	if cert, key := os.Getenv("MAIL2BARK_TLS_CERT"), os.Getenv("MAIL2BARK_TLS_KEY"); cert != "" && key != "" {
		if c, e := tlsConfig(cert, key); e == nil {
			s.TLSConfig = c
		} else {
			r.Log.Error("TLS configuration", "error", e)
		}
	}
	s.AllowInsecureAuth = s.TLSConfig == nil
	go func() { <-ctx.Done(); _ = l.Close(); _ = s.Close() }()
	r.Log.Info("smtp server listening", "addr", r.Addr)
	if r.ImplicitTLS {
		if s.TLSConfig == nil {
			return fmt.Errorf("implicit TLS requires MAIL2BARK_TLS_CERT and MAIL2BARK_TLS_KEY")
		}
		tlsListener := tls.NewListener(l, s.TLSConfig)
		return s.Serve(tlsListener)
	}
	return s.Serve(l)
}
