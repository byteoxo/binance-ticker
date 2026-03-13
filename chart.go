package main

import (
	"fmt"
	"math"
	"strings"
)

func buildChartText(candles []klineCandle, noColor bool) string {
	if len(candles) == 0 {
		return "waiting for kline data..."
	}

	if len(candles) > defaultChartLimit {
		candles = candles[len(candles)-defaultChartLimit:]
	}

	high := candles[0].HighValue
	low := candles[0].LowValue
	for _, candle := range candles {
		if candle.HighValue > high {
			high = candle.HighValue
		}
		if candle.LowValue < low {
			low = candle.LowValue
		}
	}

	span := high - low
	if span == 0 {
		span = 1
	}

	// Use 1x terminal-row resolution with │ for wicks and █ for body.
	// Mixing half-block (▀▄) chars with box-drawing (│) always creates a
	// half-row visual gap at boundaries, so we avoid them entirely.
	// We use Ceil/Floor (instead of Round) for the wick endpoints so the
	// wick range mathematically contains the body range, preventing gaps.
	chartWidth := len(candles)*chartStride - chartCandleGap

	scaleWickHigh := func(v float64) int {
		// Ceil of normalized → smallest index (wick reaches as high as possible).
		idx := defaultChartHeight - 1 - int(math.Ceil((v-low)/span*float64(defaultChartHeight-1)))
		if idx < 0 {
			return 0
		}
		if idx >= defaultChartHeight {
			return defaultChartHeight - 1
		}
		return idx
	}
	scaleWickLow := func(v float64) int {
		// Floor of normalized → largest index (wick reaches as low as possible).
		idx := defaultChartHeight - 1 - int(math.Floor((v-low)/span*float64(defaultChartHeight-1)))
		if idx < 0 {
			return 0
		}
		if idx >= defaultChartHeight {
			return defaultChartHeight - 1
		}
		return idx
	}
	scaleBody := func(v float64) int {
		idx := defaultChartHeight - 1 - int(math.Round((v-low)/span*float64(defaultChartHeight-1)))
		if idx < 0 {
			return 0
		}
		if idx >= defaultChartHeight {
			return defaultChartHeight - 1
		}
		return idx
	}

	rows := make([][]string, defaultChartHeight)
	for y := range rows {
		rows[y] = make([]string, chartWidth)
		for x := range rows[y] {
			rows[y][x] = " "
		}
	}

	for i, candle := range candles {
		wickX := i * chartStride

		highY := scaleWickHigh(candle.HighValue)
		lowY := scaleWickLow(candle.LowValue)
		openY := scaleBody(candle.OpenValue)
		closeY := scaleBody(candle.CloseValue)

		color := bullColorTag
		if candle.CloseValue < candle.OpenValue {
			color = bearColorTag
		}

		upper := minInt(openY, closeY)
		lower := maxInt(openY, closeY)

		// Wick: │ spans full terminal-row height, so it connects flush to █
		// above and below with no gap.
		for y := highY; y <= lowY; y++ {
			rows[y][wickX] = uiGlyph("│", noColor, color)
		}
		// Body: █ overwrites wick in the open→close range.
		for y := upper; y <= lower; y++ {
			rows[y][wickX] = uiGlyph("█", noColor, color)
		}
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("high %.2f\n", high))
	for _, row := range rows {
		b.WriteString(strings.Join(row, ""))
		b.WriteString("\n")
	}
	b.WriteString(fmt.Sprintf("low  %.2f\n", low))
	b.WriteString(fmt.Sprintf("last close %.2f | candles %d", candles[len(candles)-1].CloseValue, len(candles)))
	return b.String()
}

func uiGlyph(glyph string, noColor bool, color string) string {
	if noColor {
		return glyph
	}
	return fmt.Sprintf("[%s]%s[-]", color, glyph)
}

func scaleValue(value, low, span float64) int {
	normalized := (value - low) / span
	index := defaultChartHeight - 1 - int(math.Round(normalized*float64(defaultChartHeight-1)))
	if index < 0 {
		return 0
	}
	if index >= defaultChartHeight {
		return defaultChartHeight - 1
	}
	return index
}

func stripTViewTags(text string) string {
	replacer := strings.NewReplacer("["+bullColorTag+"]", "", "["+bearColorTag+"]", "", "["+neutralColorTag+"]", "", "[-]", "")
	return replacer.Replace(text)
}

func chartSymbolOrDefault(symbol, fallback string) string {
	if symbol == "" {
		return fallback
	}
	return symbol
}
