// Package server wires the HTTP routes for the session-lens API and the
// static dashboard UI.
package server

import (
	"database/sql"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/viharshah/session-lens/internal/alerter"
	"github.com/viharshah/session-lens/internal/db"
	"github.com/viharshah/session-lens/internal/mock"
	"github.com/viharshah/session-lens/internal/stats"
)

// flusher is satisfied by http.ResponseWriters that support streaming.
type flusher interface {
	Flush()
}

// Version is reported by /healthz.
const Version = "0.2.0"

// Config holds the runtime knobs for the server.
type Config struct {
	DB            *sql.DB
	StaticDir     string
	PlanBudgetUSD float64
	MockDefault   bool            // initial state of mock mode (driven by env var)
	Hub           *Hub            // SSE broadcast hub; if nil a no-op hub is used
	Notifier      alerter.Notifier // desktop notifications; if nil uses alerter.Default()
}

// modeFlag holds a thread-safe bool; UI toggles it via POST /v1/mode.
type modeFlag struct{ on atomic.Bool }

func (m *modeFlag) Set(v bool) { m.on.Store(v) }
func (m *modeFlag) Get() bool  { return m.on.Load() }

// New builds the http handler for the server.
func New(cfg Config) http.Handler {
	mux := http.NewServeMux()
	mode := &modeFlag{}
	mode.Set(cfg.MockDefault)
	// Cache the mock dataset once per process: it's deterministic.
	dataset := mock.Generate()

	// Use caller-supplied hub or a fresh one if none was provided.
	hub := cfg.Hub
	if hub == nil {
		hub = NewHub()
	}

	// Use caller-supplied notifier or the platform default.
	notifier := cfg.Notifier
	if notifier == nil {
		notifier = alerter.Default()
	}

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "version": Version, "mock": mode.Get()})
	})

	mux.HandleFunc("GET /v1/mode", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"mock": mode.Get()})
	})
	mux.HandleFunc("POST /v1/mode", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Mock bool `json:"mock"`
		}
		defer r.Body.Close()
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeError(w, http.StatusBadRequest, fmt.Errorf("invalid json: %w", err))
			return
		}
		mode.Set(body.Mock)
		writeJSON(w, http.StatusOK, map[string]any{"mock": mode.Get()})
	})

	// GET /v1/events — SSE live-tail of ingested sessions.
	mux.HandleFunc("GET /v1/events", func(w http.ResponseWriter, r *http.Request) {
		handleSSE(w, r, hub)
	})

	mux.HandleFunc("POST /v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		handleCreateSession(w, r, cfg, hub, notifier)
	})

	mux.HandleFunc("GET /v1/sessions", func(w http.ResponseWriter, r *http.Request) {
		limit := intParam(r, "limit", 20)
		if isMock(r, mode) {
			writeJSON(w, http.StatusOK, dataset.ListSessions(limit))
			return
		}
		rows, err := db.ListSessions(cfg.DB, limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, rows)
	})

	mux.HandleFunc("GET /v1/sessions/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if isMock(r, mode) {
			s, ok := dataset.GetSession(id)
			if !ok {
				writeError(w, http.StatusNotFound, fmt.Errorf("session %q not found", id))
				return
			}
			writeJSON(w, http.StatusOK, s.ToDBSession())
			return
		}
		s, err := db.GetSession(cfg.DB, id)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				writeError(w, http.StatusNotFound, fmt.Errorf("session %q not found", id))
				return
			}
			// GetSession wraps the error; check the underlying cause via string match.
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, s)
	})

	mux.HandleFunc("GET /v1/stats/summary", func(w http.ResponseWriter, r *http.Request) {
		if isMock(r, mode) {
			writeJSON(w, http.StatusOK, dataset.Summary(cfg.PlanBudgetUSD))
			return
		}
		s, err := stats.MonthSummary(cfg.DB, cfg.PlanBudgetUSD)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, s)
	})
	mux.HandleFunc("GET /v1/stats/daily", func(w http.ResponseWriter, r *http.Request) {
		days := intParam(r, "days", 30)
		if isMock(r, mode) {
			writeJSON(w, http.StatusOK, dataset.Daily(days))
			return
		}
		out, err := stats.Daily(cfg.DB, days)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("GET /v1/stats/weekly", func(w http.ResponseWriter, r *http.Request) {
		weeks := intParam(r, "weeks", 12)
		if isMock(r, mode) {
			writeJSON(w, http.StatusOK, dataset.Weekly(weeks))
			return
		}
		out, err := stats.Weekly(cfg.DB, weeks)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("GET /v1/stats/projects", func(w http.ResponseWriter, r *http.Request) {
		limit := intParam(r, "limit", 20)
		if isMock(r, mode) {
			writeJSON(w, http.StatusOK, dataset.Projects(limit))
			return
		}
		out, err := stats.Projects(cfg.DB, limit)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("GET /v1/stats/hourly", func(w http.ResponseWriter, r *http.Request) {
		days := intParam(r, "days", 7)
		if isMock(r, mode) {
			writeJSON(w, http.StatusOK, dataset.Hourly(days))
			return
		}
		out, err := stats.Hourly(cfg.DB, days)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("GET /v1/stats/by-model", func(w http.ResponseWriter, r *http.Request) {
		days := intParam(r, "days", 14)
		if isMock(r, mode) {
			writeJSON(w, http.StatusOK, dataset.ByModel(days))
			return
		}
		out, err := stats.ByModel(cfg.DB, days)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("GET /v1/stats/spikes", func(w http.ResponseWriter, r *http.Request) {
		spikeCfg := stats.DefaultSpikeConfig()
		if isMock(r, mode) {
			writeJSON(w, http.StatusOK, dataset.Spikes(spikeCfg))
			return
		}
		out, err := stats.Spikes(cfg.DB, spikeCfg)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	})

	mux.HandleFunc("GET /v1/forecast", func(w http.ResponseWriter, r *http.Request) {
		budgetUSD := monthlyBudget()
		if isMock(r, mode) {
			writeJSON(w, http.StatusOK, dataset.Forecast(budgetUSD))
			return
		}
		out, err := stats.MonthForecast(cfg.DB, budgetUSD)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, out)
	})

	// GET /v1/export/daily.csv — daily cost summary as CSV.
	mux.HandleFunc("GET /v1/export/daily.csv", func(w http.ResponseWriter, r *http.Request) {
		days := intParam(r, "days", 30)
		var buckets []stats.Bucket
		if isMock(r, mode) {
			buckets = dataset.Daily(days)
		} else {
			var err error
			buckets, err = stats.Daily(cfg.DB, days)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
		}
		filename := "daily-" + time.Now().UTC().Format("2006-01-02") + ".csv"
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{"date", "sessions", "total_cost_usd", "input_tokens", "output_tokens", "cache_creation_tokens", "cache_read_tokens"})
		for _, b := range buckets {
			_ = cw.Write([]string{
				b.Bucket,
				strconv.FormatInt(b.SessionCount, 10),
				strconv.FormatFloat(b.TotalCostUSD, 'f', 6, 64),
				strconv.FormatInt(b.InputTokens, 10),
				strconv.FormatInt(b.OutputTokens, 10),
				strconv.FormatInt(b.CacheWriteTokens, 10),
				strconv.FormatInt(b.CacheReadTokens, 10),
			})
		}
		cw.Flush()
	})

	// GET /v1/export/sessions.csv — full session list as CSV.
	mux.HandleFunc("GET /v1/export/sessions.csv", func(w http.ResponseWriter, r *http.Request) {
		limit := intParam(r, "limit", 1000)
		if limit > 10000 {
			limit = 10000
		}
		var sessions []db.Session
		if isMock(r, mode) {
			// dataset.ListSessions caps at 100; bypass to return more rows.
			all := dataset.Sessions
			count := limit
			if count > len(all) {
				count = len(all)
			}
			sessions = make([]db.Session, 0, count)
			for i := len(all) - 1; i >= len(all)-count; i-- {
				sessions = append(sessions, all[i].ToDBSession())
			}
		} else {
			var err error
			sessions, err = db.ListSessions(cfg.DB, limit)
			if err != nil {
				writeError(w, http.StatusInternalServerError, err)
				return
			}
		}
		filename := "sessions-" + time.Now().UTC().Format("2006-01-02") + ".csv"
		w.Header().Set("Content-Type", "text/csv; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
		cw := csv.NewWriter(w)
		_ = cw.Write([]string{"id", "started_at", "ended_at", "project_path", "model", "turns", "input_tokens", "output_tokens", "cache_creation_tokens", "cache_read_tokens", "total_cost_usd"})
		for _, s := range sessions {
			_ = cw.Write([]string{
				s.ID,
				s.StartedAt,
				s.EndedAt,
				s.ProjectPath,
				s.Model,
				strconv.Itoa(s.Turns),
				strconv.FormatInt(s.InputTokens, 10),
				strconv.FormatInt(s.OutputTokens, 10),
				strconv.FormatInt(s.CacheWriteTokens, 10),
				strconv.FormatInt(s.CacheReadTokens, 10),
				strconv.FormatFloat(s.TotalCostUSD, 'f', 6, 64),
			})
		}
		cw.Flush()
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

// isMock returns true if the request should be served mock data — either
// because the server-side flag is on or the caller passed `?mock=1`.
func isMock(r *http.Request, flag *modeFlag) bool {
	if flag.Get() {
		return true
	}
	q := r.URL.Query().Get("mock")
	return q == "1" || q == "true"
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

func handleCreateSession(w http.ResponseWriter, r *http.Request, cfg Config, hub *Hub, notifier alerter.Notifier) {
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
	// Notify SSE subscribers of the new/updated session.
	hub.Broadcast(out)

	// Fire a desktop notification if the session cost exceeds 2x the 7-day avg.
	if cfg.DB != nil && ev.TotalCostUSD > 0 {
		go func(costUSD float64, project string) {
			avg, err := stats.RollingAvgCostUSD(cfg.DB)
			if err != nil {
				log.Printf("spike check avg: %v", err)
				return
			}
			if avg > 0 && costUSD > 2*avg {
				msg := fmt.Sprintf("Session cost $%.4f is %.1fx the 7-day avg ($%.4f)", costUSD, costUSD/avg, avg)
				notifier.Notify("session-lens: cost spike", msg)
			}
		}(ev.TotalCostUSD, ev.ProjectPath)
	}

	status := http.StatusOK
	if inserted {
		status = http.StatusCreated
	}
	writeJSON(w, status, out)
}

// handleSSE streams newly-ingested sessions as Server-Sent Events.
// Each event looks like:
//
//	event: session
//	data: {"id":"...","project_path":"...",...}
//
// The connection stays open until the client disconnects.
func handleSSE(w http.ResponseWriter, r *http.Request, hub *Hub) {
	f, ok := w.(flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Disable any proxy buffering (nginx et al.).
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	// Send an initial comment so the browser's EventSource knows the stream is open.
	fmt.Fprint(w, ": connected\n\n")
	f.Flush()

	ch := hub.Subscribe()
	defer hub.Unsubscribe(ch)

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case s, ok := <-ch:
			if !ok {
				return
			}
			data, err := json.Marshal(s)
			if err != nil {
				log.Printf("sse marshal: %v", err)
				continue
			}
			fmt.Fprintf(w, "event: session\ndata: %s\n\n", data)
			f.Flush()
		}
	}
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

// monthlyBudget reads SESSIONLENS_MONTHLY_BUDGET_USD, defaulting to 100.
func monthlyBudget() float64 {
	raw := os.Getenv("SESSIONLENS_MONTHLY_BUDGET_USD")
	if raw == "" {
		return 100.0
	}
	v, err := strconv.ParseFloat(raw, 64)
	if err != nil || v < 0 {
		return 100.0
	}
	return v
}

func logMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start))
	})
}
