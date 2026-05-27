// Package server wires the HTTP routes for the session-lens API and the
// static dashboard UI.
package server

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"path/filepath"
	"strconv"
	"time"

	"github.com/viharshah/session-lens/internal/db"
	"github.com/viharshah/session-lens/internal/stats"
)

// Version is reported by /healthz.
const Version = "0.1.0"

// Config holds the runtime knobs for the server.
type Config struct {
	DB            *sql.DB
	StaticDir     string
	PlanBudgetUSD float64
}

// New builds the http handler for the server.
func New(cfg Config) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": Version})
	})

	mux.HandleFunc("POST /v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		handleCreateSession(w, r, cfg)
	})
	mux.HandleFunc("GET /v1/stats/summary", func(w http.ResponseWriter, r *http.Request) {
		s, err := stats.MonthSummary(cfg.DB, cfg.PlanBudgetUSD)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, s)
	})
	mux.HandleFunc("GET /v1/stats/daily", func(w http.ResponseWriter, r *http.Request) {
		days := intParam(r, "days", 30)
		out, err := stats.Daily(cfg.DB, days)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("GET /v1/stats/weekly", func(w http.ResponseWriter, r *http.Request) {
		weeks := intParam(r, "weeks", 12)
		out, err := stats.Weekly(cfg.DB, weeks)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("GET /v1/stats/projects", func(w http.ResponseWriter, r *http.Request) {
		limit := intParam(r, "limit", 20)
		out, err := stats.Projects(cfg.DB, limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	})

	// Static UI.
	if cfg.StaticDir != "" {
		fs := http.FileServer(http.Dir(cfg.StaticDir))
		mux.Handle("GET /static/", http.StripPrefix("/static/", fs))
		mux.HandleFunc("GET /{$}", func(w http.ResponseWriter, r *http.Request) {
			http.ServeFile(w, r, filepath.Join(cfg.StaticDir, "index.html"))
		})
	}

	return logMiddleware(mux)
}

// SessionEvent is the POST body for /v1/sessions.
type SessionEvent struct {
	ID               string  `json:"id"`
	ProjectPath      string  `json:"project_path"`
	StartedAt        string  `json:"started_at"`
	EndedAt          string  `json:"ended_at"`
	InputTokens      int64   `json:"input_tokens"`
	OutputTokens     int64   `json:"output_tokens"`
	CacheReadTokens  int64   `json:"cache_read_tokens"`
	CacheWriteTokens int64   `json:"cache_write_tokens"`
	TotalCostUSD     float64 `json:"total_cost_usd"`
	Model            string  `json:"model"`
	Turns            int     `json:"turns"`
	RawPayload       string  `json:"raw_payload,omitempty"`
}

func handleCreateSession(w http.ResponseWriter, r *http.Request, cfg Config) {
	defer r.Body.Close()
	var ev SessionEvent
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&ev); err != nil {
		writeError(w, http.StatusBadRequest, fmt.Errorf("invalid json: %w", err))
		return
	}
	if ev.ID == "" {
		writeError(w, http.StatusBadRequest, errors.New("id required"))
		return
	}
	if ev.EndedAt == "" {
		ev.EndedAt = time.Now().UTC().Format(time.RFC3339)
	}
	row := db.Session{
		ID:               ev.ID,
		ProjectPath:      ev.ProjectPath,
		StartedAt:        ev.StartedAt,
		EndedAt:          ev.EndedAt,
		InputTokens:      ev.InputTokens,
		OutputTokens:     ev.OutputTokens,
		CacheReadTokens:  ev.CacheReadTokens,
		CacheWriteTokens: ev.CacheWriteTokens,
		TotalCostUSD:     ev.TotalCostUSD,
		Model:            ev.Model,
		Turns:            ev.Turns,
		RawPayload:       ev.RawPayload,
	}
	out, inserted, err := db.UpsertSession(cfg.DB, row)
	if err != nil {
		writeError(w, http.StatusInternalServerError, err)
		return
	}
	status := http.StatusOK
	if inserted {
		status = http.StatusCreated
	}
	writeJSON(w, status, out)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("write json: %v", err)
	}
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}

func intParam(r *http.Request, key string, def int) int {
	raw := r.URL.Query().Get(key)
	if raw == "" {
		return def
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n <= 0 {
		return def
	}
	return n
}

func logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start))
	})
}
