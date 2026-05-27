package transcript

import "strings"

// Pricing represents per-million-token USD pricing for a model family.
type Pricing struct {
	InputPerM      float64
	OutputPerM     float64
	CacheReadPerM  float64
	CacheWritePerM float64
}

// Per-million-token pricing for Claude model families.
var (
	pricingOpus = Pricing{
		InputPerM:      15.00,
		OutputPerM:     75.00,
		CacheReadPerM:  1.50,
		CacheWritePerM: 18.75,
	}
	pricingSonnet = Pricing{
		InputPerM:      3.00,
		OutputPerM:     15.00,
		CacheReadPerM:  0.30,
		CacheWritePerM: 3.75,
	}
	pricingHaiku = Pricing{
		InputPerM:      1.00,
		OutputPerM:     5.00,
		CacheReadPerM:  0.10,
		CacheWritePerM: 1.25,
	}
)

// PricingFor returns the pricing table for a model name. Detection is by
// substring (case-insensitive): "opus" -> opus, "haiku" -> haiku, else sonnet.
func PricingFor(model string) Pricing {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "opus"):
		return pricingOpus
	case strings.Contains(m, "haiku"):
		return pricingHaiku
	default:
		return pricingSonnet
	}
}

// ComputeCost returns the USD cost for a token-usage tuple under the given model.
// Negative token counts are clamped to 0 to prevent corrupting cost aggregations.
func ComputeCost(model string, inputTokens, outputTokens, cacheReadTokens, cacheWriteTokens int64) float64 {
	p := PricingFor(model)
	const perM = 1_000_000.0
	return (float64(clampTokens(inputTokens))*p.InputPerM +
		float64(clampTokens(outputTokens))*p.OutputPerM +
		float64(clampTokens(cacheReadTokens))*p.CacheReadPerM +
		float64(clampTokens(cacheWriteTokens))*p.CacheWritePerM) / perM
}

// clampTokens returns v if v >= 0, otherwise 0. Guards against negative token
// values that would corrupt cost calculations.
func clampTokens(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}
