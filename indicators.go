package main

import "math"

// calcEMA returns the EMA of closes with the given period, or NaN if insufficient data.
func calcEMA(closes []float64, period int) float64 {
	if len(closes) < period {
		return math.NaN()
	}
	k := 2.0 / float64(period+1)
	ema := closes[0]
	for _, v := range closes[1:] {
		ema = v*k + ema*(1-k)
	}
	return ema
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

func buildIndicatorLine(candles []klineCandle) string {
	if len(candles) < 2 {
		return ""
	}
	closes := make([]float64, len(candles))
	for i, c := range candles {
		closes[i] = c.CloseValue
	}

	ema20 := calcEMA(closes, 20)
	ema50 := calcEMA(closes, 50)
	rsi14 := calcRSI(closes, 14)

	line := ""
	if !math.IsNaN(ema20) {
		line += "EMA20 " + formatCompactFloat(ema20)
	}
	if !math.IsNaN(ema50) {
		if line != "" {
			line += "  "
		}
		line += "EMA50 " + formatCompactFloat(ema50)
	}
	if !math.IsNaN(rsi14) {
		if line != "" {
			line += "  "
		}
		rsiStr := formatCompactFloat(rsi14)
		line += "RSI14 " + rsiStr
	}
	return line
}
