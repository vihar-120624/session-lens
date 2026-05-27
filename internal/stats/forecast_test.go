package stats

import (
	"math"
	"testing"
	"time"
)

// knownDay pins a date mid-month so daysRemaining is predictable.
var knownDay = time.Date(2026, 5, 10, 12, 0, 0, 0, time.UTC)

// May 2026 has 31 days; day=10 → 21 days remaining (11..31).
const knownDaysRemaining = 21

func approxEqual(a, b, tol float64) bool {
	return math.Abs(a-b) <= tol
}

func TestComputeForecast_Basic(t *testing.T) {
	// 7 days of uniform $2/day spend, $20 month-to-date.
	dailyCosts := []float64{2, 2, 2, 2, 2, 2, 2}
	mtd := 20.0
	budget := 100.0

	f := ComputeForecast(mtd, dailyCosts, budget, knownDay)

	if f.DaysRemaining != knownDaysRemaining {
		t.Errorf("DaysRemaining = %d, want %d", f.DaysRemaining, knownDaysRemaining)
	}
	if !approxEqual(f.DailyAvgLast7DUSD, 2.0, 0.001) {
		t.Errorf("DailyAvgLast7DUSD = %v, want 2.0", f.DailyAvgLast7DUSD)
	}
	// projected = 20 + 2*21 = 62
	wantProjected := 20.0 + 2.0*float64(knownDaysRemaining)
	if !approxEqual(f.ProjectedMonthTotalUSD, wantProjected, 0.001) {
		t.Errorf("ProjectedMonthTotalUSD = %v, want %v", f.ProjectedMonthTotalUSD, wantProjected)
	}
	if f.MonthToDateUSD != 20.0 {
		t.Errorf("MonthToDateUSD = %v, want 20", f.MonthToDateUSD)
	}
	if !f.OnTrack {
		t.Errorf("OnTrack = false, want true (projected %v <= budget %v)", f.ProjectedMonthTotalUSD, budget)
	}
	if f.OverageEstimateUSD != 0 {
		t.Errorf("OverageEstimateUSD = %v, want 0 (on track)", f.OverageEstimateUSD)
	}
}

func TestComputeForecast_OverBudget(t *testing.T) {
	// High daily spend: $10/day, $50 mtd, $100 budget → projected = 50 + 10*21 = 260
	dailyCosts := []float64{10, 10, 10, 10, 10, 10, 10}
	mtd := 50.0
	budget := 100.0

	f := ComputeForecast(mtd, dailyCosts, budget, knownDay)

	wantProjected := 50.0 + 10.0*float64(knownDaysRemaining)
	if !approxEqual(f.ProjectedMonthTotalUSD, wantProjected, 0.001) {
		t.Errorf("ProjectedMonthTotalUSD = %v, want %v", f.ProjectedMonthTotalUSD, wantProjected)
	}
	if f.OnTrack {
		t.Errorf("OnTrack = true, want false")
	}
	wantOverage := wantProjected - budget
	if !approxEqual(f.OverageEstimateUSD, wantOverage, 0.001) {
		t.Errorf("OverageEstimateUSD = %v, want %v", f.OverageEstimateUSD, wantOverage)
	}
}

func TestComputeForecast_NoBudget(t *testing.T) {
	// budgetUSD=0 → on_track always true, no overage.
	dailyCosts := []float64{50, 50, 50}
	f := ComputeForecast(200.0, dailyCosts, 0, knownDay)
	if !f.OnTrack {
		t.Errorf("OnTrack = false with zero budget, want true")
	}
	if f.OverageEstimateUSD != 0 {
		t.Errorf("OverageEstimateUSD = %v with zero budget, want 0", f.OverageEstimateUSD)
	}
}

func TestComputeForecast_NoDailyData(t *testing.T) {
	// No recent daily data → avg=0, projected equals mtd.
	f := ComputeForecast(5.0, nil, 100.0, knownDay)
	if !approxEqual(f.ProjectedMonthTotalUSD, 5.0, 0.001) {
		t.Errorf("ProjectedMonthTotalUSD = %v, want 5.0 (no daily data)", f.ProjectedMonthTotalUSD)
	}
	if f.DailyAvgLast7DUSD != 0 {
		t.Errorf("DailyAvgLast7DUSD = %v, want 0", f.DailyAvgLast7DUSD)
	}
}

func TestComputeForecast_FewerThan7Days(t *testing.T) {
	// Only 3 days of data; avg should use those 3 days.
	dailyCosts := []float64{3, 6, 9} // avg = 6
	f := ComputeForecast(18.0, dailyCosts, 200.0, knownDay)
	if !approxEqual(f.DailyAvgLast7DUSD, 6.0, 0.001) {
		t.Errorf("DailyAvgLast7DUSD = %v, want 6.0", f.DailyAvgLast7DUSD)
	}
	wantProjected := 18.0 + 6.0*float64(knownDaysRemaining)
	if !approxEqual(f.ProjectedMonthTotalUSD, wantProjected, 0.001) {
		t.Errorf("ProjectedMonthTotalUSD = %v, want %v", f.ProjectedMonthTotalUSD, wantProjected)
	}
}

func TestDaysIn(t *testing.T) {
	cases := []struct {
		year  int
		month time.Month
		want  int
	}{
		{2026, time.January, 31},
		{2026, time.February, 28},
		{2024, time.February, 29}, // leap year
		{2026, time.May, 31},
		{2026, time.April, 30},
	}
	for _, tc := range cases {
		got := daysIn(tc.year, tc.month)
		if got != tc.want {
			t.Errorf("daysIn(%d, %s) = %d, want %d", tc.year, tc.month, got, tc.want)
		}
	}
}
