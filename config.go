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
	Exchange     string
	Symbols      []string
	SpotSymbols  []string
	ChartSymbol  string
	ChartLimit   int
	DefaultPanel string
	Timeout      time.Duration
	TZ           string
	RESTBase     string
	WSBase       string
	NoColor      bool
	RetryDelay   time.Duration
	APIKey       string
	APISecret    string
	ConfigPath   string
}

type rawConfig struct {
	Exchange     string   `toml:"exchange"`
	Symbols      []string `toml:"symbols"`
	SpotSymbols  []string `toml:"spot_symbols"`
	ChartSymbol  string   `toml:"chart_symbol"`
	ChartLimit   int      `toml:"chart_limit"`
	DefaultPanel string   `toml:"default_panel"`
	Timeout      string   `toml:"timeout"`
	TZ           string   `toml:"tz"`
	RESTBase     string   `toml:"rest_base"`
	WSBase       string   `toml:"ws_base"`
	NoColor      bool     `toml:"no_color"`
	RetryDelay   string   `toml:"retry_delay"`
	APIKey       string   `toml:"api_key"`
	APISecret    string   `toml:"api_secret"`
}

func (cfg config) hasAccountAuth() bool {
	return cfg.APIKey != "" && cfg.APISecret != ""
}

func (cfg config) hasSpot() bool {
	return len(cfg.SpotSymbols) > 0
}

func (cfg config) isGate() bool {
	return cfg.Exchange == "gate"
}

func envAPIKeyName(exchange string) string {
	if exchange == "gate" {
		return "GATE_API_KEY"
	}
	return "BINANCE_API_KEY"
}

func envAPISecretName(exchange string) string {
	if exchange == "gate" {
		return "GATE_API_SECRET"
	}
	return "BINANCE_API_SECRET"
}

// exchangeDefaults returns (restBase, wsBase) defaults for futures panel.
func exchangeDefaults(exchange string) (string, string) {
	switch exchange {
	case "gate":
		return defaultGateRESTBaseURL, defaultGateWSBaseURL
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

	required := []string{"symbols", "chart_symbol", "chart_limit", "default_panel", "timeout", "tz", "no_color", "retry_delay"}
	for _, key := range required {
		if !meta.IsDefined(key) {
			return config{}, fmt.Errorf("config %s missing required field %q", path, key)
		}
	}

	exchange := strings.ToLower(strings.TrimSpace(raw.Exchange))
	if exchange == "" {
		exchange = "binance"
	}
	if exchange != "binance" && exchange != "gate" {
		return config{}, fmt.Errorf("config %s field %q must be one of %q or %q", path, "exchange", "binance", "gate")
	}

	symbols := normalizeSymbols(strings.Join(raw.Symbols, ","))
	spotSymbols := normalizeSymbols(strings.Join(raw.SpotSymbols, ","))
	if len(symbols) == 0 && len(spotSymbols) == 0 {
		return config{}, fmt.Errorf("config %s must define at least one of %q or %q", path, "symbols", "spot_symbols")
	}

	chartSymbol := strings.ToUpper(strings.TrimSpace(raw.ChartSymbol))
	if chartSymbol == "" && len(symbols) > 0 {
		return config{}, fmt.Errorf("config %s field %q cannot be empty when futures symbols are configured", path, "chart_symbol")
	}
	chartLimit := raw.ChartLimit
	if len(symbols) > 0 && chartLimit <= 0 {
		return config{}, fmt.Errorf("config %s field %q must be greater than 0 when futures symbols are configured", path, "chart_limit")
	}
	if len(symbols) == 0 && chartLimit <= 0 {
		chartLimit = defaultChartLimit
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
	// Environment variables override config file values.
	// Binance: BINANCE_API_KEY / BINANCE_API_SECRET
	// Gate.io: GATE_API_KEY   / GATE_API_SECRET
	if v := os.Getenv(envAPIKeyName(exchange)); v != "" {
		apiKey = v
	}
	if v := os.Getenv(envAPISecretName(exchange)); v != "" {
		apiSecret = v
	}
	if (apiKey == "") != (apiSecret == "") {
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

	return config{
		Exchange:     exchange,
		Symbols:      symbols,
		SpotSymbols:  spotSymbols,
		ChartSymbol:  chartSymbol,
		ChartLimit:   chartLimit,
		DefaultPanel: string(defaultPanel),
		Timeout:      timeout,
		TZ:           tz,
		RESTBase:     restBase,
		WSBase:       wsBase,
		NoColor:      raw.NoColor || os.Getenv("NO_COLOR") != "",
		RetryDelay:   retryDelay,
		APIKey:       apiKey,
		APISecret:    apiSecret,
		ConfigPath:   path,
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
