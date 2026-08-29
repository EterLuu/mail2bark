package mail2bark

import (
	"context"
	"crypto/tls"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

type Config struct {
	HTTPAddr        string
	SMTPAddr        string
	SMTPTLSAddr     string
	SMTPStartTLS    string
	DBPath          string
	AdminKey        string
	RecipientDomain string
	MaxMessageSize  int64
	BarkTimeout     time.Duration
	BasePath        string
}

func normalizeBasePath(value string) string {
	cleaned := path.Clean("/" + strings.TrimSpace(value))
	if cleaned == "/" || cleaned == "." {
		return ""
	}
	return cleaned
}

func loadConfig() Config {
	get := func(k, d string) string {
		if v := os.Getenv(k); v != "" {
			return v
		}
		return d
	}
	max := int64(10 << 20)
	if v := os.Getenv("MAIL2BARK_MAX_MESSAGE_BYTES"); v != "" {
		fmt.Sscanf(v, "%d", &max)
	}
	timeout := 15 * time.Second
	if v := os.Getenv("MAIL2BARK_BARK_TIMEOUT"); v != "" {
		if d, e := time.ParseDuration(v); e == nil {
			timeout = d
		}
	}
	return Config{
		HTTPAddr: get("MAIL2BARK_HTTP_ADDR", ":8080"), SMTPAddr: get("MAIL2BARK_SMTP_ADDR", ":2525"),
		SMTPTLSAddr: get("MAIL2BARK_SMTP_TLS_ADDR", ""), SMTPStartTLS: get("MAIL2BARK_SMTP_STARTTLS_ADDR", ""),
		DBPath: get("MAIL2BARK_DB_PATH", "/data/mail2bark.db"), AdminKey: os.Getenv("MAIL2BARK_ADMIN_KEY"),
		RecipientDomain: get("MAIL2BARK_RECIPIENT_DOMAIN", "notify.internal"),
		MaxMessageSize:  max, BarkTimeout: timeout,
		BasePath: normalizeBasePath(os.Getenv("MAIL2BARK_BASE_PATH")),
	}
}

// Run starts the SMTP receivers, management API, and delivery worker. It blocks
// until SIGINT or SIGTERM is received.
func Run() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	cfg := loadConfig()
	if cfg.AdminKey == "" {
		log.Warn("MAIL2BARK_ADMIN_KEY is empty; management API is running without authentication")
	}
	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0750); err != nil {
		log.Error("create data directory", "error", err)
		os.Exit(1)
	}
	db, err := sql.Open("sqlite3", cfg.DBPath+"?_journal_mode=WAL&_synchronous=FULL&_busy_timeout=5000")
	if err != nil {
		log.Error("open database", "error", err)
		os.Exit(1)
	}
	defer db.Close()
	store := NewStore(db)
	if err := store.Migrate(context.Background()); err != nil {
		log.Error("database migration", "error", err)
		os.Exit(1)
	}
	bark := NewBarkClient(cfg.BarkTimeout, log)
	worker := NewWorker(store, bark, log)
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go worker.Run(ctx)

	backend := &SMTPBackend{Store: store, MaxSize: cfg.MaxMessageSize, Log: log}
	servers := []*SMTPServerRunner{NewSMTPServerRunner(cfg.SMTPAddr, backend, log)}
	if cfg.SMTPStartTLS != "" {
		servers = append(servers, NewSMTPServerRunner(cfg.SMTPStartTLS, backend, log, false))
	}
	if cfg.SMTPTLSAddr != "" {
		servers = append(servers, NewSMTPServerRunner(cfg.SMTPTLSAddr, backend, log, true))
	}
	for _, s := range servers {
		go func(s *SMTPServerRunner) {
			if err := s.ListenAndServe(ctx); err != nil && !errors.Is(err, context.Canceled) {
				log.Error("smtp server", "error", err)
			}
		}(s)
	}

	api := &API{Store: store, AdminKey: cfg.AdminKey, RecipientDomain: cfg.RecipientDomain, BasePath: cfg.BasePath, Log: log}
	httpServer := &http.Server{Addr: cfg.HTTPAddr, Handler: api.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		log.Info("http server listening", "addr", cfg.HTTPAddr)
		if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Error("http server", "error", err)
		}
	}()
	<-ctx.Done()
	shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = httpServer.Shutdown(shutdown)
}

func parseClientIP(addr string) net.IP {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		host = strings.TrimSpace(addr)
	}
	ip := net.ParseIP(host)
	if v4 := ip.To4(); v4 != nil {
		return v4
	}
	return ip
}

func jsonResponse(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

var errUnauthorized = errors.New("unauthorized")

// tlsConfig loads certificates for implicit TLS SMTP listeners when configured.
func tlsConfig(certFile, keyFile string) (*tls.Config, error) {
	c, err := tls.LoadX509KeyPair(certFile, keyFile)
	if err != nil {
		return nil, err
	}
	return &tls.Config{Certificates: []tls.Certificate{c}, MinVersion: tls.VersionTLS12}, nil
}
