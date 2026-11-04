package tui

import (
	"math"
	"testing"
)

func TestComputeEnvSavings(t *testing.T) {
	tests := []struct {
		tokens      int64
		wantCO2     float64
		wantWater   float64
		description string
	}{
		{0, 0.0, 0.0, "zero tokens"},
		// 6700 tokens at 0.8 kg CO2 / 8 L water per million tokens (see eco.go).
		{6700, 5.36, 53.6, "6700 tokens (example case)"},
		{1_000_000, co2GramsPerMillionTokens, waterMlPerMillionTokens, "1 million tokens"},
	}

	const tolerance = 0.005 // allow 0.005 rounding tolerance for float math

	for _, tt := range tests {
		t.Run(tt.description, func(t *testing.T) {
			co2, water := computeEnvSavings(tt.tokens)
			if math.Abs(co2-tt.wantCO2) > tolerance {
				t.Errorf("CO2: got %.4f, want %.4f (±%.3f)", co2, tt.wantCO2, tolerance)
			}
			if math.Abs(water-tt.wantWater) > tolerance {
				t.Errorf("water: got %.4f, want %.4f (±%.3f)", water, tt.wantWater, tolerance)
			}
		})
	}
}

func TestEnvSavingsStatusSegmentUnitScaling(t *testing.T) {
	tests := []struct {
		tokens int64
		want   string
	}{
		{0, "🌿 0.000g CO2 | ❄️ 0.000ml water"},
		{500_000, "🌿 400.000g CO2 | ❄️ 4.000L water"},
		{1_000_000, "🌿 800.000g CO2 | ❄️ 8.000L water"},
		{2_000_000, "🌿 1.600kg CO2 | ❄️ 16.000L water"},
	}
	for _, tt := range tests {
		if got := envSavingsStatusSegment(tt.tokens); got != tt.want {
			t.Errorf("tokens=%d: got %q, want %q", tt.tokens, got, tt.want)
		}
	}
}

func TestSessionEcoTokensIncludesUnpriced(t *testing.T) {
	m := model{unpricedTokens: 12345}
	if got := m.sessionEcoTokens(); got != 12345 {
		t.Errorf("sessionEcoTokens: got %d, want 12345", got)
	}
}

func TestSessionEcoTokensStreamFallback(t *testing.T) {
	// When no usage events fire (unpricedTokens == 0), sessionStreamedRunes/4
	// is used as a monotonically-growing fallback so the eco counter doesn't
	// reset to zero at the start of every new message.
	m := model{sessionStreamedRunes: 4000} // 4000 runes / 4 = 1000 tokens
	if got := m.sessionEcoTokens(); got != 1000 {
		t.Errorf("stream fallback: got %d, want 1000", got)
	}

	// When real usage data is available, it wins over the stream estimate.
	m2 := model{unpricedTokens: 5000, sessionStreamedRunes: 4000}
	if got := m2.sessionEcoTokens(); got != 5000 {
		t.Errorf("real usage should win: got %d, want 5000", got)
	}
}
