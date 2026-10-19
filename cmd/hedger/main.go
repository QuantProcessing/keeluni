// Command hedger serves the hedge-trading app's API (/v1). It trades only
// through the gateway and keeps its state in its own PostgreSQL database.
//
//	hedger          run the server
//	hedger initdb   create the hedger role and database (needs admin access)
//
// Environment:
//
//	HEDGER_ADDR               listen address                       (default 127.0.0.1:8082)
//	HEDGER_DATA_DIR           state directory                      (default ./data/hedger)
//	HEDGER_TOKEN_FILE         bearer token callers present, 0600   (default $DATA_DIR/token; created)
//	HEDGER_DATABASE_URL       postgres://... (or HEDGER_DATABASE_URL_FILE)
//	HEDGER_DATABASE_URL_FILE  file holding the URL, 0600           (default $DATA_DIR/database.url)
//	GATEWAY_URL               gateway base URL                     (default http://127.0.0.1:8081)
//	GATEWAY_TOKEN_FILE        gateway bearer token                 (default ./data/gateway/token)
//
// initdb connects as the Postgres superuser — POSTGRES_ADMIN_URL, or
// POSTGRES_HOST plus POSTGRES_ADMIN_PASSWORD_FILE — and writes the generated
// hedger URL to HEDGER_DATABASE_URL_FILE.
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/QuantProcessing/keeluni/gwclient"
	"github.com/QuantProcessing/keeluni/hedger"
	"github.com/jackc/pgx/v5"
)

func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	var err error
	if len(os.Args) > 1 && os.Args[1] == "initdb" {
		err = initdb()
	} else {
		err = run()
	}
	if err != nil {
		slog.Error("hedger stopped", "error", err)
		os.Exit(1)
	}
}

func dataDir() string { return env("HEDGER_DATA_DIR", "data/hedger") }

func databaseURLFile() string {
	return env("HEDGER_DATABASE_URL_FILE", filepath.Join(dataDir(), "database.url"))
}

func run() error {
	if err := os.MkdirAll(dataDir(), 0o700); err != nil {
		return err
	}
	token, err := ensureSecretFile(env("HEDGER_TOKEN_FILE", filepath.Join(dataDir(), "token")))
	if err != nil {
		return fmt.Errorf("hedger token: %w", err)
	}
	databaseURL := env("HEDGER_DATABASE_URL", "")
	if databaseURL == "" {
		if databaseURL, err = waitForSecret(databaseURLFile(), 60*time.Second); err != nil {
			return fmt.Errorf("database URL: %w (run `hedger initdb` or set HEDGER_DATABASE_URL)", err)
		}
	}
	gatewayToken, err := waitForSecret(env("GATEWAY_TOKEN_FILE", "data/gateway/token"), 30*time.Second)
	if err != nil {
		return fmt.Errorf("gateway token: %w", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	var store *hedger.Store
	for attempt := 0; ; attempt++ {
		openCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		store, err = hedger.OpenStore(openCtx, databaseURL)
		cancel()
		if err == nil {
			break
		}
		if attempt >= 30 || ctx.Err() != nil {
			return fmt.Errorf("database: %w", err)
		}
		slog.Warn("database not ready; retrying", "error", err)
		time.Sleep(2 * time.Second)
	}
	defer store.Close()

	client, err := gwclient.New(env("GATEWAY_URL", "http://127.0.0.1:8081"), gatewayToken, &http.Client{Timeout: 25 * time.Second})
	if err != nil {
		return err
	}
	service := hedger.NewService(store, client)
	errs := make(chan error, 2)
	go func() { errs <- service.Run(ctx) }()

	server := &http.Server{
		Addr:              env("HEDGER_ADDR", "127.0.0.1:8082"),
		Handler:           hedger.NewAPI(service, token),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	go func() {
		slog.Info("hedger listening", "addr", server.Addr)
		errs <- server.ListenAndServe()
	}()
	select {
	case <-ctx.Done():
	case err := <-errs:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return server.Shutdown(shutdownCtx)
}

// initdb creates the hedger role (random password) and database, owned by
// that role, and writes the hedger's connection URL. It is idempotent: an
// existing role keeps working with the URL already written.
func initdb() error {
	target := databaseURLFile()
	if existing, err := os.ReadFile(target); err == nil && strings.TrimSpace(string(existing)) != "" {
		fmt.Fprintln(os.Stderr, "hedger database URL already exists:", target)
		return nil
	}
	adminURL := env("POSTGRES_ADMIN_URL", "")
	if adminURL == "" {
		host := env("POSTGRES_HOST", "")
		// The admin password file is group-readable by design (shared with
		// Postgres), so it is read without the owner-only check.
		data, err := os.ReadFile(env("POSTGRES_ADMIN_PASSWORD_FILE", ""))
		password := strings.TrimSpace(string(data))
		if host == "" || err != nil || password == "" {
			return fmt.Errorf("set POSTGRES_ADMIN_URL, or POSTGRES_HOST and a readable POSTGRES_ADMIN_PASSWORD_FILE")
		}
		admin := url.URL{Scheme: "postgres", User: url.UserPassword(env("POSTGRES_ADMIN_USER", "postgres"), password), Host: host, Path: "/postgres", RawQuery: "sslmode=disable"}
		adminURL = admin.String()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	var conn *pgx.Conn
	var err error
	for attempt := 0; ; attempt++ {
		conn, err = pgx.Connect(ctx, adminURL)
		if err == nil {
			break
		}
		if attempt >= 30 {
			return fmt.Errorf("connect as admin: %w", err)
		}
		time.Sleep(2 * time.Second)
	}
	defer conn.Close(context.Background())
	raw := make([]byte, 24)
	if _, err := rand.Read(raw); err != nil {
		return err
	}
	password := hex.EncodeToString(raw)
	role, database := "hedger", "hedger"
	var exists bool
	if err := conn.QueryRow(ctx, "select exists(select 1 from pg_roles where rolname=$1)", role).Scan(&exists); err != nil {
		return err
	}
	quotedPassword := "'" + strings.ReplaceAll(password, "'", "''") + "'"
	if exists {
		// The URL file was lost: rotate the password so a new URL works.
		if _, err := conn.Exec(ctx, "alter role "+role+" with login password "+quotedPassword); err != nil {
			return err
		}
	} else if _, err := conn.Exec(ctx, "create role "+role+" with login password "+quotedPassword); err != nil {
		return err
	}
	if err := conn.QueryRow(ctx, "select exists(select 1 from pg_database where datname=$1)", database).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		if _, err := conn.Exec(ctx, "create database "+database+" owner "+role); err != nil {
			return err
		}
	}
	// Other roles may not connect to the hedger's database.
	if _, err := conn.Exec(ctx, "revoke all on database "+database+" from public"); err != nil {
		return err
	}
	admin, err := url.Parse(adminURL)
	if err != nil {
		return err
	}
	hedgerURL := url.URL{Scheme: "postgres", User: url.UserPassword(role, password), Host: admin.Host, Path: "/" + database, RawQuery: admin.RawQuery}
	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(target, []byte(hedgerURL.String()+"\n"), 0o600); err != nil {
		return err
	}
	fmt.Fprintln(os.Stderr, "hedger database ready; URL written to", target)
	return nil
}

// ensureSecretFile returns the trimmed contents of path, creating it with a
// random 32-byte hex secret (mode 0600) when missing.
func ensureSecretFile(path string) (string, error) {
	if value, err := readSecret(path); err == nil {
		return value, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	value := hex.EncodeToString(raw)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(path, []byte(value+"\n"), 0o600); err != nil {
		return "", err
	}
	return value, nil
}

func readSecret(path string) (string, error) {
	if path == "" {
		return "", os.ErrNotExist
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	if info, err := os.Stat(path); err == nil && info.Mode().Perm()&0o077 != 0 {
		return "", fmt.Errorf("%s must not be readable by group or others (chmod 600)", path)
	}
	value := strings.TrimSpace(string(data))
	if value == "" {
		return "", fmt.Errorf("%s is empty", path)
	}
	return value, nil
}

// waitForSecret tolerates another container creating the file on first start.
func waitForSecret(path string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for {
		value, err := readSecret(path)
		if err == nil {
			return value, nil
		}
		if time.Now().After(deadline) {
			return "", err
		}
		time.Sleep(500 * time.Millisecond)
	}
}
