// sessionlens-server is the HTTP server exposing the dashboard UI and the
// JSON API used by the Stop-hook to record token usage.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/viharshah/session-lens/internal/db"
	"github.com/viharshah/session-lens/internal/server"
)

func main() {
	port := getenv("PORT", "7821")
	dbPath := getenv("DB_PATH", "./data/sessions.db")
	planBudget := parseFloat(getenv("PLAN_BUDGET_USD", "20"), 20.0)

	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		log.Fatalf("create db dir: %v", err)
	}

	conn, err := db.Open(dbPath)
	if err != nil {
		log.Fatalf("open db: %v", err)
	}
	defer conn.Close()

	staticDir := getenv("STATIC_DIR", "./web")
	mockDefault := os.Getenv("MOCK_MODE") == "1"
	bufferDir := getenv("SESSIONLENS_BUFFER_DIR", defaultBufferDir())
	handler := server.New(server.Config{
		DB:            conn,
		DBPath:        dbPath,
		BufferDir:     bufferDir,
		StaticDir:     staticDir,
		PlanBudgetUSD: planBudget,
		MockDefault:   mockDefault,
	})

	srv := &http.Server{
		Addr:              ":" + port,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	go func() {
		log.Printf("session-lens server listening on http://127.0.0.1:%s (db=%s, budget=$%.2f, mock=%v)", port, dbPath, planBudget, mockDefault)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server: %v", err)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	log.Println("shutdown requested")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Printf("graceful shutdown: %v", err)
	}
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func parseFloat(raw string, def float64) float64 {
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return def
	}
	return v
}

// defaultBufferDir mirrors the path the Stop-hook uses for its disk buffer.
// Kept in sync with sessionlens-hook's bufferDir.
func defaultBufferDir() string {
	base := os.Getenv("XDG_STATE_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = "/tmp"
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "sessionlens", "buffer")
}
