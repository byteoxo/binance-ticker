package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

type config struct {
	Exchange      string
	Symbols       []string
	SpotSymbols   []string
	ChartSymbol   string
	ChartLimit    int
	DefaultPanel  string
	Timeout       time.Duration
	TZ            string
	RESTBase      string
	WSBase        string
	NoColor       bool
	RetryDelay    time.Duration
	APIKey        string
	APISecret     string
	APIPassphrase string
	ConfigPath    string
}

type rawConfig struct {
	Exchange      string   `toml:"exchange"`
	Symbols       []string `toml:"symbols"`
	SpotSymbols   []string `toml:"spot_symbols"`
	ChartSymbol   string   `toml:"chart_symbol"`
	ChartLimit    int      `toml:"chart_limit"`
	DefaultPanel  string   `toml:"default_panel"`
	Timeout       string   `toml:"timeout"`
	TZ            string   `toml:"tz"`
	RESTBase      string   `toml:"rest_base"`
	WSBase        string   `toml:"ws_base"`
	NoColor       bool     `toml:"no_color"`
	RetryDelay    string   `toml:"retry_delay"`
	APIKey        string   `toml:"api_key"`
	APISecret     string   `toml:"api_secret"`
	APIPassphrase string   `toml:"api_passphrase"`
}

func (cfg config) hasAccountAuth() bool {
	if cfg.APIKey == "" || cfg.APISecret == "" {
		return false
	}
	if cfg.isOKX() && strings.TrimSpace(cfg.APIPassphrase) == "" {
		return false
	}
	return true
}

func (cfg config) hasSpot() bool {
	return len(cfg.SpotSymbols) > 0
}

func (cfg config) isGate() bool {
	return cfg.Exchange == "gate"
}

func (cfg config) isOKX() bool {
	return cfg.Exchange == "okx"
}

// chartsEnabled reports whether REST/WS should load candlesticks and the UI should show the chart panel.
func (cfg config) chartsEnabled() bool {
	return cfg.ChartLimit > 0
}

func envAPIKeyName(exchange string) string {
	switch exchange {
	case "gate":
		return "GATE_API_KEY"
	case "okx":
		return "OKX_API_KEY"
	default:
		return "BINANCE_API_KEY"
	}
}

func envAPISecretName(exchange string) string {
	switch exchange {
	case "gate":
		return "GATE_API_SECRET"
	case "okx":
		return "OKX_API_SECRET"
	default:
		return "BINANCE_API_SECRET"
	}
}

// normalizeLegacyBinanceFuturesMarketWSBase maps the pre-migration combined-stream host to the
// routed /market base so @ticker, @markPrice, @kline_*, etc. receive data.
func normalizeLegacyBinanceFuturesMarketWSBase(wsBase string) string {
	w := strings.TrimRight(strings.TrimSpace(wsBase), "/")
	switch strings.ToLower(w) {
	case "wss://fstream.binance.com", "ws://fstream.binance.com":
		return "wss://fstream.binance.com/market"
	default:
		return w
	}
}

// exchangeDefaults returns (restBase, wsBase) defaults for futures panel.
func exchangeDefaults(exchange string) (string, string) {
	switch exchange {
	case "gate":
		return defaultGateRESTBaseURL, defaultGateWSBaseURL
	case "okx":
		return defaultOKXRESTBaseURL, defaultOKXWSBaseURL
	default:
		return defaultRESTBaseURL, defaultWSBaseURL
	}
}

func mustLoadConfig() config {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("fatal: %v", err)
	}
	return cfg
}

func loadConfig() (config, error) {
	path, err := resolveConfigPath()
	if err != nil {
		return config{}, err
	}

	var raw rawConfig
	meta, err := toml.DecodeFile(path, &raw)
	if err != nil {
		return config{}, fmt.Errorf("decode config %s: %w", path, err)
	}

	required := []string{"symbols", "chart_limit", "default_panel", "timeout", "tz", "no_color", "retry_delay"}
	for _, key := range required {
		if !meta.IsDefined(key) {
			return config{}, fmt.Errorf("config %s missing required field %q", path, key)
		}
	}

	exchange := strings.ToLower(strings.TrimSpace(raw.Exchange))
	if exchange == "" {
		exchange = "binance"
	}
	if exchange != "binance" && exchange != "gate" && exchange != "okx" {
		return config{}, fmt.Errorf("config %s field %q must be one of %q, %q, or %q", path, "exchange", "binance", "gate", "okx")
	}

	symbols := normalizeSymbols(strings.Join(raw.Symbols, ","))
	spotSymbols := normalizeSymbols(strings.Join(raw.SpotSymbols, ","))
	if exchange == "binance" {
		for i := range symbols {
			symbols[i] = strings.ReplaceAll(symbols[i], "_", "")
		}
		for i := range spotSymbols {
			spotSymbols[i] = strings.ReplaceAll(spotSymbols[i], "_", "")
		}
	}
	if exchange == "okx" {
		for i, rawSym := range symbols {
			c := okxCompactFromInstID(rawSym)
			if c == "" {
				return config{}, fmt.Errorf("config %s exchange okx: invalid futures instrument %q in symbols", path, rawSym)
			}
			symbols[i] = c
		}
		for i, rawSym := range spotSymbols {
			c := okxCompactFromInstID(rawSym)
			if c == "" {
				return config{}, fmt.Errorf("config %s exchange okx: invalid spot instrument %q in spot_symbols", path, rawSym)
			}
			spotSymbols[i] = c
		}
		symbols = normalizeSymbolList(symbols)
		spotSymbols = normalizeSymbolList(spotSymbols)
	}
	if len(symbols) == 0 && len(spotSymbols) == 0 {
		return config{}, fmt.Errorf("config %s must define at least one of %q or %q", path, "symbols", "spot_symbols")
	}

	chartSymbol := strings.ToUpper(strings.TrimSpace(raw.ChartSymbol))
	if chartSymbol != "" && exchange == "binance" {
		chartSymbol = strings.ReplaceAll(chartSymbol, "_", "")
	}
	if chartSymbol != "" && exchange == "okx" {
		c := okxCompactFromInstID(chartSymbol)
		if c == "" {
			return config{}, fmt.Errorf("config %s exchange okx: invalid chart_symbol %q (use OKX instrument ids, e.g. BTC-USDT-SWAP)", path, strings.TrimSpace(raw.ChartSymbol))
		}
		chartSymbol = c
	}
	chartLimit := raw.ChartLimit
	if chartLimit < 0 {
		return config{}, fmt.Errorf("config %s field %q must be >= 0", path, "chart_limit")
	}
	if chartSymbol == "" && len(symbols) > 0 && chartLimit > 0 {
		return config{}, fmt.Errorf("config %s field %q cannot be empty when futures symbols are configured and chart_limit > 0", path, "chart_symbol")
	}

	defaultPanel := panelMode(strings.ToLower(strings.TrimSpace(raw.DefaultPanel)))
	if defaultPanel != panelFutures && defaultPanel != panelSpot {
		return config{}, fmt.Errorf("config %s field %q must be one of %q or %q", path, "default_panel", panelFutures, panelSpot)
	}

	timeout, err := time.ParseDuration(strings.TrimSpace(raw.Timeout))
	if err != nil || timeout <= 0 {
		return config{}, fmt.Errorf("config %s field %q must be a valid positive duration", path, "timeout")
	}
	retryDelay, err := time.ParseDuration(strings.TrimSpace(raw.RetryDelay))
	if err != nil || retryDelay <= 0 {
		return config{}, fmt.Errorf("config %s field %q must be a valid positive duration", path, "retry_delay")
	}

	apiKey := strings.TrimSpace(raw.APIKey)
	apiSecret := strings.TrimSpace(raw.APISecret)
	apiPassphrase := strings.TrimSpace(raw.APIPassphrase)
	// Environment variables override config file values.
	// Binance: BINANCE_API_KEY / BINANCE_API_SECRET
	// Gate.io: GATE_API_KEY   / GATE_API_SECRET
	// OKX:     OKX_API_KEY    / OKX_API_SECRET / OKX_API_PASSPHRASE
	if v := os.Getenv(envAPIKeyName(exchange)); v != "" {
		apiKey = v
	}
	if v := os.Getenv(envAPISecretName(exchange)); v != "" {
		apiSecret = v
	}
	if exchange == "okx" && os.Getenv("OKX_API_PASSPHRASE") != "" {
		apiPassphrase = strings.TrimSpace(os.Getenv("OKX_API_PASSPHRASE"))
	}

	authIncompletePair := (apiKey == "") != (apiSecret == "")
	if exchange == "okx" {
		hasAnyAuth := apiKey != "" || apiSecret != "" || apiPassphrase != ""
		if hasAnyAuth && (apiKey == "" || apiSecret == "" || apiPassphrase == "") {
			return config{}, fmt.Errorf("config %s exchange okx requires %q, %q, and %q together when using account auth", path, "api_key", "api_secret", "api_passphrase")
		}
	} else if authIncompletePair {
		return config{}, fmt.Errorf("config %s requires both %q and %q when account auth is enabled", path, "api_key", "api_secret")
	}

	tz := strings.TrimSpace(raw.TZ)
	if tz == "" {
		return config{}, fmt.Errorf("config %s field %q cannot be empty", path, "tz")
	}
	restBase := strings.TrimRight(strings.TrimSpace(raw.RESTBase), "/")
	wsBase := strings.TrimRight(strings.TrimSpace(raw.WSBase), "/")
	// Apply exchange-specific defaults when not explicitly set.
	if restBase == "" || wsBase == "" {
		defREST, defWS := exchangeDefaults(exchange)
		if restBase == "" {
			restBase = defREST
		}
		if wsBase == "" {
			wsBase = defWS
		}
	}
	if restBase == "" {
		return config{}, fmt.Errorf("config %s field %q cannot be empty", path, "rest_base")
	}
	if wsBase == "" {
		return config{}, fmt.Errorf("config %s field %q cannot be empty", path, "ws_base")
	}

	if exchange == "binance" {
		wsBase = normalizeLegacyBinanceFuturesMarketWSBase(wsBase)
	}

	return config{
		Exchange:      exchange,
		Symbols:       symbols,
		SpotSymbols:   spotSymbols,
		ChartSymbol:   chartSymbol,
		ChartLimit:    chartLimit,
		DefaultPanel:  string(defaultPanel),
		Timeout:       timeout,
		TZ:            tz,
		RESTBase:      restBase,
		WSBase:        wsBase,
		NoColor:       raw.NoColor || os.Getenv("NO_COLOR") != "",
		RetryDelay:    retryDelay,
		APIKey:        apiKey,
		APISecret:     apiSecret,
		APIPassphrase: apiPassphrase,
		ConfigPath:    path,
	}, nil
}

func resolveConfigPath() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}

	candidates := []string{
		"./config.toml",
		filepath.Join(homeDir, ".config", "crypto-ticker", "config.toml"),
	}

	for _, candidate := range candidates {
		if info, err := os.Stat(candidate); err == nil && !info.IsDir() {
			abs, absErr := filepath.Abs(candidate)
			if absErr == nil {
				return abs, nil
			}
			return candidate, nil
		}
	}

	return "", fmt.Errorf("config file not found; expected one of %s or %s", candidates[0], candidates[1])
}

func isSpotTickerSymbolFunc(cfg config) func(string) bool {
	spotSet := make(map[string]struct{}, len(cfg.SpotSymbols))
	for _, symbol := range spotSymbolsToTickers(cfg.SpotSymbols) {
		spotSet[symbol] = struct{}{}
	}
	return func(symbol string) bool {
		_, ok := spotSet[strings.ToUpper(strings.TrimSpace(symbol))]
		return ok
	}
}

func getChartSymbolForActivePanel(state *appState) func() string {
	return func() string {
		_, _, _, _, _, chartSymbol, _, _, _, _, _, _, _, _, _, _, _, _, _ := state.snapshot()
		return chartSymbol
	}
}
