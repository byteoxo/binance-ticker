package main

import "math"

// Braille-based sparkline using 8 levels per character column.
// Each braille dot column has 4 rows (bits 0-3 left, 4-7 right in unicode layout).
// We use a simple 8-level block approach instead: ▁▂▃▄▅▆▇█
var sparkBlocks = []rune{'▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}

func buildSparkline(prices []float64) string {
	if len(prices) < 2 {
		return ""
	}

	lo, hi := prices[0], prices[0]
	for _, p := range prices {
		if p < lo {
			lo = p
		}
		if p > hi {
			hi = p
		}
	}

	span := hi - lo
	runes := make([]rune, len(prices))
	for i, p := range prices {
		var idx int
		if span < 1e-12 {
			idx = len(sparkBlocks) / 2
		} else {
			normalized := (p - lo) / span
			idx = int(math.Round(normalized * float64(len(sparkBlocks)-1)))
			if idx < 0 {
				idx = 0
			}
			if idx >= len(sparkBlocks) {
				idx = len(sparkBlocks) - 1
			}
		}
		runes[i] = sparkBlocks[idx]
	}
	return string(runes)
}
