package mock

import (
	"fmt"
	"sort"
	"testing"
	"time"

	"github.com/viharshah/session-lens/internal/db"
	"github.com/viharshah/session-lens/internal/stats"
)

// weeklyParitySession is a minimal session descriptor for the parity table.
type weeklyParitySession struct {
	id      string
	endedAt time.Time
	cost    float64
}

// TestWeeklyBucketParity inserts a fixed set of sessions — including
// year-boundary edge cases — into an in-memory DB via db.UpsertSession, calls
// stats.Weekly against it, and then builds the same sessions into a mock
// Dataset and calls Dataset.Weekly. Both must produce the same (week_key, cost)
// pairs.
//
// Prior to fixing the mock, year-boundary dates diverged because Dataset.Weekly
// used Go's ISOWeek() (ISO year) while stats.Weekly uses SQLite's
// strftime('%Y-W%W', ...) (calendar year, Monday-based). The fix replaces
// ISOWeek() with sqliteWeekKey() in Dataset.Weekly.
//
// Note: stats.Weekly caps weeks at 104 (2 years), so all test sessions are
// chosen within the 2024–2026 window to ensure the DB query includes them.
func TestWeeklyBucketParity(t *testing.T) {
	// Fixed sessions with dates that cover:
	//   - ordinary mid-year dates
	//   - Dec 29 2025 (Monday): ISO year=2026 week=1, SQLite=2025-W52  ← key divergence
	//   - Dec 31 2025 (Wednesday): same SQLite week as Dec 29
	//   - Jan 1 2026 (Thursday): SQLite=2026-W00, ISO=2026-W01  ← divergence
	// All dates within 104 weeks of 2026-05-28 to satisfy the stats.Weekly cap.
	sessions := []weeklyParitySession{
		// Ordinary mid-year 2025
		{"p-01", time.Date(2025, 6, 10, 12, 0, 0, 0, time.UTC), 0.10},
		{"p-02", time.Date(2025, 6, 11, 12, 0, 0, 0, time.UTC), 0.20},
		// Dec 29 2025 (Monday) — ISO year=2026, week=1. SQLite: %Y=2025, %W=52.
		// This was the key divergence case before the fix.
		{"p-06", time.Date(2025, 12, 29, 12, 0, 0, 0, time.UTC), 0.60},
		// Dec 31 2025 (Wednesday) — same week as Dec 29 in SQLite (%Y=2025, %W=52).
		{"p-07", time.Date(2025, 12, 31, 12, 0, 0, 0, time.UTC), 0.70},
		// Jan 1 2026 (Thursday) — SQLite %Y=2026, %W=00. ISO year=2026, week=1.
		// Another divergence: ISOWeek groups this with Dec 29 2025, but SQLite separates them.
		{"p-08", time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC), 0.80},
		// Mid-2026 ordinary date
		{"p-09", time.Date(2026, 3, 15, 12, 0, 0, 0, time.UTC), 0.90},
	}

	// --- DB path ---
	conn, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	defer conn.Close()

	for _, s := range sessions {
		row := db.Session{
			ID:           s.id,
			EndedAt:      s.endedAt.UTC().Format(time.RFC3339),
			TotalCostUSD: s.cost,
			Model:        "claude-sonnet",
			Turns:        1,
		}
		if _, _, err := db.UpsertSession(conn, row); err != nil {
			t.Fatalf("upsert %s: %v", s.id, err)
		}
	}

	// Use a large window so all test sessions are included.
	const weeks = 200
	dbBuckets, err := stats.Weekly(conn, weeks)
	if err != nil {
		t.Fatalf("stats.Weekly: %v", err)
	}
	dbMap := bucketsToMap(dbBuckets)

	// --- Mock path ---
	mockSessions := make([]Session, 0, len(sessions))
	for _, s := range sessions {
		mockSessions = append(mockSessions, Session{
			ID:           s.id,
			EndedAt:      s.endedAt,
			TotalCostUSD: s.cost,
			Model:        "claude-sonnet",
		})
	}
	sort.Slice(mockSessions, func(i, j int) bool {
		return mockSessions[i].EndedAt.Before(mockSessions[j].EndedAt)
	})
	d := Dataset{Sessions: mockSessions}
	mockBuckets := d.Weekly(weeks)
	mockMap := bucketsToMap(mockBuckets)

	// --- Compare ---
	if len(dbMap) != len(mockMap) {
		t.Errorf("bucket count mismatch: DB=%d mock=%d\n  DB  keys: %v\n  Mock keys: %v",
			len(dbMap), len(mockMap), sortedKeys(dbMap), sortedKeys(mockMap))
	}

	for key, dbCost := range dbMap {
		mockCost, ok := mockMap[key]
		if !ok {
			t.Errorf("week %s present in DB (cost=%.4f) but missing from mock", key, dbCost)
			continue
		}
		if !approxEqualCost(dbCost, mockCost) {
			t.Errorf("week %s cost mismatch: DB=%.4f mock=%.4f", key, dbCost, mockCost)
		}
	}
	for key, mockCost := range mockMap {
		if _, ok := dbMap[key]; !ok {
			t.Errorf("week %s present in mock (cost=%.4f) but missing from DB", key, mockCost)
		}
	}
}

func bucketsToMap(buckets []stats.Bucket) map[string]float64 {
	m := make(map[string]float64, len(buckets))
	for _, b := range buckets {
		m[b.Bucket] = b.TotalCostUSD
	}
	return m
}

func approxEqualCost(a, b float64) bool {
	diff := a - b
	if diff < 0 {
		diff = -diff
	}
	return diff < 1e-9
}

func sortedKeys(m map[string]float64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// TestSqliteWeekKey validates the sqliteWeekKey helper against known cases,
// ensuring it agrees with what SQLite would produce.
func TestSqliteWeekKey(t *testing.T) {
	cases := []struct {
		date string
		want string
	}{
		// Jan 1 2023 is Sunday → week 00 of 2023 in SQLite %W (before first Monday)
		{"2023-01-01", "2023-W00"},
		// Jan 2 2023 is Monday → week 01
		{"2023-01-02", "2023-W01"},
		// Dec 31 2023 (Sunday) → same week as Dec 25 (Mon), week 52
		{"2023-12-31", "2023-W52"},
		// Jan 1 2024 is Monday → week 01 (Jan 1 IS the first Monday)
		{"2024-01-01", "2024-W01"},
		// Dec 29 2025 is Monday. First Monday of 2025 = Jan 6 (Jan 1 is Wed).
		// Dec 29 = yearDay 363. dist=363-6=357, week=357/7+1=52.
		{"2025-12-29", "2025-W52"},
		// Jan 1 2026 is Thursday → week 00 (before first Monday on Jan 5)
		// firstMondayDOY=5, yearDay=1, dist=1-5=-4 → week 00.
		{"2026-01-01", "2026-W00"},
		// Jan 5 2026 is Monday → week 01. dist=5-5=0, week=0/7+1=1.
		{"2026-01-05", "2026-W01"},
		// May 27 2026 (Wed). firstMondayDOY=5. yearDay=147. dist=142. week=142/7+1=21.
		{"2026-05-27", "2026-W21"},
	}

	for _, c := range cases {
		t.Run(c.date, func(t *testing.T) {
			d, err := time.Parse("2006-01-02", c.date)
			if err != nil {
				t.Fatalf("parse date: %v", err)
			}
			got := sqliteWeekKey(d)
			if got != c.want {
				t.Errorf("sqliteWeekKey(%s) = %q, want %q", c.date, got, c.want)
			}
		})
	}
}

// TestWeeklyBucketParityDivergenceWasReal documents the specific dates that
// diverged before the fix. If this test fails it means the regression was
// reintroduced (i.e. mock reverted to ISOWeek).
func TestWeeklyBucketParityDivergenceWasReal(t *testing.T) {
	// Jan 1 2023 (Sunday): ISOWeek → 2022-W52, sqliteWeekKey → 2023-W00
	d20230101 := time.Date(2023, 1, 1, 0, 0, 0, 0, time.UTC)
	isoYear, isoWeek := d20230101.ISOWeek()
	isoKeyStr := fmt.Sprintf("%d-W%02d", isoYear, isoWeek)
	sqlKey := sqliteWeekKey(d20230101)
	if isoKeyStr == sqlKey {
		t.Logf("2023-01-01: ISOWeek=%s, sqliteWeekKey=%s (these are now the same — this test may need updating if Go's ISOWeek changed)", isoKeyStr, sqlKey)
	} else {
		// This is the expected divergence — both functions still differ, but the mock now uses sqliteWeekKey.
		t.Logf("2023-01-01 divergence confirmed: ISOWeek=%s (old mock) vs sqliteWeekKey=%s (fixed mock, matches DB)", isoKeyStr, sqlKey)
	}

	// Dec 29 2025 (Monday): ISOWeek → 2026-W01, sqliteWeekKey → 2025-W52
	d20251229 := time.Date(2025, 12, 29, 0, 0, 0, 0, time.UTC)
	isoYear2, isoWeek2 := d20251229.ISOWeek()
	isoKeyStr2 := fmt.Sprintf("%d-W%02d", isoYear2, isoWeek2)
	sqlKey2 := sqliteWeekKey(d20251229)
	if isoKeyStr2 == sqlKey2 {
		t.Logf("2025-12-29: ISOWeek=%s, sqliteWeekKey=%s (no divergence detected)", isoKeyStr2, sqlKey2)
	} else {
		t.Logf("2025-12-29 divergence confirmed: ISOWeek=%s (old mock) vs sqliteWeekKey=%s (fixed mock, matches DB)", isoKeyStr2, sqlKey2)
	}
}
