package main

import "strings"

func spotSymbolToTicker(asset string) string {
	asset = strings.ToUpper(strings.TrimSpace(asset))
	if asset == "" || asset == "USDT" {
		return ""
	}
	return asset + "USDT"
}

func spotTickerToAsset(symbol string) string {
	symbol = strings.ToUpper(strings.TrimSpace(symbol))
	if strings.HasSuffix(symbol, "USDT") {
		return strings.TrimSuffix(symbol, "USDT")
	}
	return symbol
}

func spotSymbolsToTickers(assets []string) []string {
	result := make([]string, 0, len(assets))
	for _, asset := range assets {
		ticker := spotSymbolToTicker(asset)
		if ticker != "" {
			result = append(result, ticker)
		}
	}
	return normalizeSymbolList(result)
}

func allowedSpotAssets(symbols []string) map[string]struct{} {
	allowed := make(map[string]struct{}, len(symbols))
	for _, asset := range symbols {
		asset = strings.ToUpper(strings.TrimSpace(asset))
		if asset == "" {
			continue
		}
		allowed[asset] = struct{}{}
	}
	return allowed
}

func normalizeSymbolList(symbols []string) []string {
	seen := make(map[string]struct{}, len(symbols))
	result := make([]string, 0, len(symbols))
	for _, raw := range symbols {
		symbol := strings.ToUpper(strings.TrimSpace(raw))
		if symbol == "" {
			continue
		}
		if _, ok := seen[symbol]; ok {
			continue
		}
		seen[symbol] = struct{}{}
		result = append(result, symbol)
	}
	return result
}

func normalizeSymbols(raw string) []string {
	parts := strings.Split(raw, ",")
	seen := make(map[string]struct{}, len(parts))
	result := make([]string, 0, len(parts))
	for _, part := range parts {
		symbol := strings.ToUpper(strings.TrimSpace(part))
		if symbol == "" {
			continue
		}
		if _, ok := seen[symbol]; ok {
			continue
		}
		seen[symbol] = struct{}{}
		result = append(result, symbol)
	}
	return result
}

func indexOfSymbol(symbols []string, target string) int {
	for i, symbol := range symbols {
		if symbol == target {
			return i
		}
	}
	return -1
}
