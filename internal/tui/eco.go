package tui

import (
	"fmt"

	"github.com/aistatine/sonut/internal/usage"
)

// Reference: 0.8 kg CO2 per million tokens, 8 L water per million tokens.
const (
	co2GramsPerMillionTokens = 800.0  // g CO2 per million tokens
	waterMlPerMillionTokens  = 8000.0 // ml water per million tokens
)

// computeEnvSavings returns the estimated CO2 (grams) and water (millilitres)
// saved by running locally instead of on a cloud model, based on cumulative
// session token count.
func computeEnvSavings(tokens int64) (co2Grams float64, waterMl float64) {
	millions := float64(tokens) / 1_000_000
	return millions * co2GramsPerMillionTokens, millions * waterMlPerMillionTokens
}

// envSavingsStatusSegment formats the live environmental savings counter for
// the status bar: "🌿 0.00g CO2  | ❄️ 0.00ml water"
func envSavingsStatusSegment(tokens int64) string {
	co2, water := computeEnvSavings(tokens)
	return "🌿 " + formatMassCO2(co2) + " | ❄️ " + formatWaterVolume(water)
}

// formatMassCO2 renders grams, switching to kilograms once the number gets big.
func formatMassCO2(grams float64) string {
	if grams >= 1000 {
		return fmt.Sprintf("%.3fkg CO2", grams/1000)
	}
	return fmt.Sprintf("%.3fg CO2", grams)
}

// formatWaterVolume renders millilitres, switching to litres once big.
func formatWaterVolume(ml float64) string {
	if ml >= 1000 {
		return fmt.Sprintf("%.3fL water", ml/1000)
	}
	return fmt.Sprintf("%.3fml water", ml)
}

// sessionEcoTokens returns the cumulative token count for the eco counter.
// For priced models the usage tracker gives exact counts. For local models like
// atcode whose llama-server may strip stream_options (no include_usage), usage
// events never fire so unpricedTokens stays 0; sessionStreamedRunes/4 is a
// monotonically-growing output-token estimate accumulated from completed turns.
func (m model) sessionEcoTokens() int64 {
	total := int64(m.unpricedTokens)
	if m.usageTracker != nil {
		total += int64(m.usageTracker.Summary().TotalTokens)
	}
	// Fallback: if no usage events fired, use cumulative streamed-rune estimate.
	if est := int64(m.sessionStreamedRunes) / 4; est > total {
		total = est
	}
	return total
}

// Fable 5 water usage: 0.005 mL per token (inference + cooling).
const fableWaterPerTokenML = 0.005

// ecoSavingsLine formats the per-turn eco savings footnote (without 🌿 prefix).
func ecoSavingsLine(u usage.Normalized) string {
	return formatEcoLine(u.InputTokens, u.OutputTokens)
}

// ecoSessionSavingsLine formats cumulative session savings (without 🌿 prefix).
func ecoSessionSavingsLine(inputTokens, outputTokens int) string {
	return formatEcoLine(inputTokens, outputTokens)
}

func formatEcoLine(inputTokens, outputTokens int) string {
	total := inputTokens + outputTokens
	if total == 0 {
		return ""
	}
	ml := float64(total) * fableWaterPerTokenML
	var result string
	if ml >= 1000 {
		result = fmt.Sprintf("~%.1f L water saved", ml/1000)
	} else if ml >= 0.1 {
		result = fmt.Sprintf("~%.1f mL water saved", ml)
	} else {
		result = fmt.Sprintf("~%.2f mL water saved", ml)
	}
	return fmt.Sprintf("%s (%d tok)", result, total)
}
