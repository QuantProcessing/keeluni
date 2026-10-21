// Command optioner serves the options app's API (/v1): portfolio risk over
// every options account and multi-leg execution. It trades only through the
// gateway and keeps no database: positions are the venues', read live.
//
// Environment:
//
//	OPTIONER_ADDR        listen address                       (default 127.0.0.1:8083)
//	OPTIONER_DATA_DIR    state directory                      (default ./data/optioner)
//	OPTIONER_TOKEN_FILE  bearer token callers present, 0600   (default $DATA_DIR/token; created)
//	GATEWAY_URL          gateway base URL                     (default http://127.0.0.1:8081)
//	GATEWAY_TOKEN_FILE   gateway bearer token                 (default ./data/gateway/token)
package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/QuantProcessing/keeluni/gwclient"
	"github.com/QuantProcessing/keeluni/optioner"
)

func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))
	if err := run(); err != nil {
		slog.Error("optioner stopped", "error", err)
		os.Exit(1)
	}
}

func run() error {
	dataDir := env("OPTIONER_DATA_DIR", "data/optioner")
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return err
	}
	token, err := ensureSecretFile(env("OPTIONER_TOKEN_FILE", filepath.Join(dataDir, "token")))
	if err != nil {
		return fmt.Errorf("optioner token: %w", err)
	}
	gatewayToken, err := waitForSecret(env("GATEWAY_TOKEN_FILE", "data/gateway/token"), 30*time.Second)
	if err != nil {
		return fmt.Errorf("gateway token: %w", err)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	client, err := gwclient.New(env("GATEWAY_URL", "http://127.0.0.1:8081"), gatewayToken, &http.Client{Timeout: 25 * time.Second})
	if err != nil {
		return err
	}
	service := optioner.NewService(client)
	errs := make(chan error, 2)
	go func() { errs <- service.Run(ctx) }()
	server := &http.Server{
		Addr:              env("OPTIONER_ADDR", "127.0.0.1:8083"),
		Handler:           optioner.NewAPI(service, token),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	go func() {
		slog.Info("optioner listening", "addr", server.Addr)
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
