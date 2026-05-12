package main

import "strings"

// okxCompactFromInstID maps an OKX instrument id (e.g. ETH-USDT-SWAP, ETH-USDT)
// to the compact symbol key used in app state (ETHUSDT).
func okxCompactFromInstID(instID string) string {
	instID = strings.TrimSpace(instID)
	if instID == "" {
		return ""
	}
	s := instID
	s = strings.TrimSuffix(s, "-SWAP")
	s = strings.ReplaceAll(s, "-", "")
	return strings.ToUpper(s)
}

// okxNormalizeSymbol collapses Gate-style separators (e.g. BTC_USDT) so suffix parsing yields BTCUSDT, not BTC_.
func okxNormalizeSymbol(compact string) string {
	return strings.ToUpper(strings.TrimSpace(strings.ReplaceAll(compact, "_", "")))
}

func okxSwapInstID(compact string) string {
	compact = okxNormalizeSymbol(compact)
	if compact == "" {
		return ""
	}
	if strings.Contains(compact, "-") {
		if strings.HasSuffix(compact, "-SWAP") {
			return compact
		}
		return compact + "-SWAP"
	}
	if strings.HasSuffix(compact, "USDT") {
		base := strings.TrimSuffix(compact, "USDT")
		if base != "" {
			return base + "-USDT-SWAP"
		}
	}
	if strings.HasSuffix(compact, "USDC") {
		base := strings.TrimSuffix(compact, "USDC")
		if base != "" {
			return base + "-USDC-SWAP"
		}
	}
	return compact + "-SWAP"
}

// okxRubikCCYFromInstID is the base asset for Rubik statistics (e.g. BTC for BTC-USDT-SWAP).
func okxRubikCCYFromInstID(instID string) string {
	parts := strings.Split(strings.TrimSpace(instID), "-")
	if len(parts) >= 1 && parts[0] != "" {
		return parts[0]
	}
	return ""
}

func okxSpotInstID(compact string) string {
	compact = okxNormalizeSymbol(compact)
	if compact == "" {
		return ""
	}
	if strings.Contains(compact, "-") {
		return compact
	}
	if strings.HasSuffix(compact, "USDT") {
		base := strings.TrimSuffix(compact, "USDT")
		if base != "" {
			return base + "-USDT"
		}
	}
	if strings.HasSuffix(compact, "USDC") {
		base := strings.TrimSuffix(compact, "USDC")
		if base != "" {
			return base + "-USDC"
		}
	}
	return compact
}

// okxSpotBalanceFilterAssets maps compact spot pair keys (e.g. ETHUSDT from ETH-USDT)
// to base asset codes used in OKX balance payloads (ccy field).
func okxSpotBalanceFilterAssets(compactPairs []string) []string {
	out := make([]string, 0, len(compactPairs))
	for _, s := range compactPairs {
		s = strings.ToUpper(strings.TrimSpace(s))
		if s == "" {
			continue
		}
		base := okxSpotBalanceAssetFromCompact(s)
		if base != "" {
			out = append(out, base)
		}
	}
	return normalizeSymbolList(out)
}

func okxSpotBalanceAssetFromCompact(compact string) string {
	if compact == "" {
		return ""
	}
	if strings.HasSuffix(compact, "USDT") && len(compact) > len("USDT") {
		return strings.TrimSuffix(compact, "USDT")
	}
	if strings.HasSuffix(compact, "USDC") && len(compact) > len("USDC") {
		return strings.TrimSuffix(compact, "USDC")
	}
	return compact
}
