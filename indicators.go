package main

import (
	"fmt"
	"math"
)

// calcEMA returns the EMA of closes with the given period, or NaN if insufficient data.
// Seed with a simple average of the first `period` values.
func calcEMA(closes []float64, period int) float64 {
	if len(closes) < period {
		return math.NaN()
	}
	k := 2.0 / float64(period+1)
	// seed with SMA of first `period` values
	var seed float64
	for _, v := range closes[:period] {
		seed += v
	}
	ema := seed / float64(period)
	for _, v := range closes[period:] {
		ema = v*k + ema*(1-k)
	}
	return ema
}

// calcEMASeries computes EMA for every point and returns the full series.
// Returns nil if insufficient data.
func calcEMASeries(closes []float64, period int) []float64 {
	if len(closes) < period {
		return nil
	}
	k := 2.0 / float64(period+1)
	var seed float64
	for _, v := range closes[:period] {
		seed += v
	}
	first := seed / float64(period)
	out := make([]float64, len(closes)-period+1)
	out[0] = first
	for i, v := range closes[period:] {
		out[i+1] = v*k + out[i]*(1-k)
	}
	return out
}

// calcRSI returns the RSI(period) of closes, or NaN if insufficient data.
func calcRSI(closes []float64, period int) float64 {
	if len(closes) < period+1 {
		return math.NaN()
	}
	data := closes[len(closes)-(period+1):]
	var gains, losses float64
	for i := 1; i < len(data); i++ {
		diff := data[i] - data[i-1]
		if diff > 0 {
			gains += diff
		} else {
			losses -= diff
		}
	}
	avgGain := gains / float64(period)
	avgLoss := losses / float64(period)
	if avgLoss < 1e-12 {
		return 100
	}
	rs := avgGain / avgLoss
	return 100 - 100/(1+rs)
}

// calcBB returns (upper, middle, lower) Bollinger Bands for the given period and stddev multiplier.
// Returns NaN values if insufficient data.
func calcBB(closes []float64, period int, mult float64) (upper, middle, lower float64) {
	nan := math.NaN()
	if len(closes) < period {
		return nan, nan, nan
	}
	data := closes[len(closes)-period:]
	var sum float64
	for _, v := range data {
		sum += v
	}
	ma := sum / float64(period)
	var variance float64
	for _, v := range data {
		d := v - ma
		variance += d * d
	}
	stddev := math.Sqrt(variance / float64(period))
	return ma + mult*stddev, ma, ma - mult*stddev
}

// calcMACD returns (macdLine, signalLine, histogram).
// Uses EMA12, EMA26 for MACD line, EMA9 of MACD for signal.
// Returns NaN if insufficient data (need at least 26+9=35 candles).
func calcMACD(closes []float64) (macdLine, signalLine, histogram float64) {
	nan := math.NaN()
	ema12s := calcEMASeries(closes, 12)
	ema26s := calcEMASeries(closes, 26)
	if ema12s == nil || ema26s == nil {
		return nan, nan, nan
	}
	// align: ema26s starts at index 25, ema12s starts at index 11
	// macd series aligned to ema26s length
	offset := len(ema12s) - len(ema26s)
	if offset < 0 {
		return nan, nan, nan
	}
	macdSeries := make([]float64, len(ema26s))
	for i := range ema26s {
		macdSeries[i] = ema12s[i+offset] - ema26s[i]
	}
	signalSeries := calcEMASeries(macdSeries, 9)
	if signalSeries == nil {
		return nan, nan, nan
	}
	ml := macdSeries[len(macdSeries)-1]
	sl := signalSeries[len(signalSeries)-1]
	return ml, sl, ml - sl
}

func buildIndicatorLine(candles []klineCandle) string {
	if len(candles) < 2 {
		return ""
	}
	closes := make([]float64, len(candles))
	for i, c := range candles {
		closes[i] = c.CloseValue
	}

	var parts []string

	ema20 := calcEMA(closes, 20)
	if !math.IsNaN(ema20) {
		parts = append(parts, "EMA20 "+formatCompactFloat(ema20))
	}
	ema50 := calcEMA(closes, 50)
	if !math.IsNaN(ema50) {
		parts = append(parts, "EMA50 "+formatCompactFloat(ema50))
	}
	rsi14 := calcRSI(closes, 14)
	if !math.IsNaN(rsi14) {
		parts = append(parts, fmt.Sprintf("RSI14 %.1f", rsi14))
	}

	upper, _, lower := calcBB(closes, 20, 2)
	if !math.IsNaN(upper) {
		parts = append(parts, fmt.Sprintf("BB↑ %s BB↓ %s", formatCompactFloat(upper), formatCompactFloat(lower)))
	}

	ml, sl, hist := calcMACD(closes)
	if !math.IsNaN(ml) {
		arrow := "▲"
		if hist < 0 {
			arrow = "▼"
		}
		parts = append(parts, fmt.Sprintf("MACD %s%+.2f Sig %+.2f", arrow, ml, sl))
	}

	line := ""
	for i, p := range parts {
		if i > 0 {
			line += "  "
		}
		line += p
	}
	return line
}
