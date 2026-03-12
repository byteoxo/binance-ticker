package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/gdamore/tcell/v2"
	"github.com/gorilla/websocket"
	"github.com/rivo/tview"
)

const (
	defaultWSBaseURL          = "wss://fstream.binance.com"
	defaultRESTBaseURL        = "https://fapi.binance.com"
	defaultSpotWSBaseURL      = "wss://stream.binance.com:9443"
	defaultSpotRESTBaseURL    = "https://api.binance.com"
	futuresKlinePath          = "/fapi/v1/klines"
	spotKlinePath             = "/api/v3/klines"
	positionRiskPath          = "/fapi/v3/positionRisk"
	listenKeyPath             = "/fapi/v1/listenKey"
	spotAccountPath           = "/api/v3/account"
	defaultSpotWSAPIBaseURL   = "wss://ws-api.binance.com:443/ws-api/v3"
	defaultTimeout            = 8 * time.Second
	userDataKeepaliveInterval = 50 * time.Minute
	uiRefreshInterval         = time.Second
	defaultChartLimit         = 48
	defaultChartHeight        = 12
	chartCandleWidth          = 1
	chartCandleGap            = 1
	chartStride               = chartCandleWidth + chartCandleGap
	bullColorTag              = "#00c853"
	bearColorTag              = "#e53935"
	neutralColorTag           = "#9aa0a6"
)

type config struct {
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

type wsEnvelope struct {
	Stream string          `json:"stream"`
	Data   json.RawMessage `json:"data"`
}

type wsKlineEnvelope struct {
	Symbol string         `json:"s"`
	Kline  wsKlinePayload `json:"k"`
}

type wsKlinePayload struct {
	StartTime jsonFlexibleInt64  `json:"t"`
	CloseTime jsonFlexibleInt64  `json:"T"`
	Open      jsonFlexibleString `json:"o"`
	High      jsonFlexibleString `json:"h"`
	Low       jsonFlexibleString `json:"l"`
	Close     jsonFlexibleString `json:"c"`
	IsClosed  bool               `json:"x"`
}

type userDataListenKeyResponse struct {
	ListenKey string `json:"listenKey"`
}

type userDataEvent struct {
	EventType string `json:"e"`
	ListenKey string `json:"listenKey"`
}

type userDataAccountUpdateEvent struct {
	EventType string                `json:"e"`
	EventTime jsonFlexibleInt64     `json:"E"`
	Data      userDataAccountUpdate `json:"a"`
}

type userDataAccountUpdate struct {
	Reason    string                    `json:"m"`
	Positions []userDataPositionPayload `json:"P"`
}

type userDataPositionPayload struct {
	Symbol           string             `json:"s"`
	PositionAmt      jsonFlexibleString `json:"pa"`
	EntryPrice       jsonFlexibleString `json:"ep"`
	UnrealizedProfit jsonFlexibleString `json:"up"`
	MarginType       string             `json:"mt"`
	PositionSide     string             `json:"ps"`
}

type spotUserDataEvent struct {
	EventType string `json:"e"`
}

type spotOutboundAccountPositionEvent struct {
	EventType  string                     `json:"e"`
	EventTime  jsonFlexibleInt64          `json:"E"`
	UpdateTime jsonFlexibleInt64          `json:"u"`
	Balances   []spotBalanceUpdatePayload `json:"B"`
}

type spotBalanceUpdatePayload struct {
	Asset  string             `json:"a"`
	Free   jsonFlexibleString `json:"f"`
	Locked jsonFlexibleString `json:"l"`
}

type spotAssetDeltaEvent struct {
	EventType string             `json:"e"`
	EventTime jsonFlexibleInt64  `json:"E"`
	Asset     string             `json:"a"`
	Delta     jsonFlexibleString `json:"d"`
	ClearTime jsonFlexibleInt64  `json:"T"`
}

type spotBalanceUpdate struct {
	Asset      string
	Free       float64
	Locked     float64
	UpdateTime int64
	Remove     bool
}

type jsonFlexibleInt64 int64

type jsonFlexibleString string

func (v *jsonFlexibleInt64) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		*v = 0
		return nil
	}

	var num int64
	if err := json.Unmarshal(data, &num); err == nil {
		*v = jsonFlexibleInt64(num)
		return nil
	}

	var text string
	if err := json.Unmarshal(data, &text); err != nil {
		return err
	}

	parsed, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return err
	}

	*v = jsonFlexibleInt64(parsed)
	return nil
}

func (v *jsonFlexibleString) UnmarshalJSON(data []byte) error {
	if len(data) == 0 || string(data) == "null" {
		*v = ""
		return nil
	}

	var text string
	if err := json.Unmarshal(data, &text); err == nil {
		*v = jsonFlexibleString(text)
		return nil
	}

	var number json.Number
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	decoder.UseNumber()
	if err := decoder.Decode(&number); err != nil {
		return err
	}

	*v = jsonFlexibleString(number.String())
	return nil
}

type rowState struct {
	Symbol       string
	Price        string
	PriceValue   float64
	ExchangeTime int64
	LocalTime    time.Time
	PrevValue    float64
	HasPrev      bool
	Change       int
	Delta        float64
	DeltaPct     float64
	Updates      int
	Status       string
}

type klineCandle struct {
	Symbol     string
	OpenTime   int64
	CloseTime  int64
	Open       string
	High       string
	Low        string
	Close      string
	OpenValue  float64
	HighValue  float64
	LowValue   float64
	CloseValue float64
	Closed     bool
}

type priceTicker struct {
	Symbol string `json:"symbol"`
	Price  string `json:"price"`
	Time   int64  `json:"time"`
}

type positionRiskResponse struct {
	Symbol           string `json:"symbol"`
	PositionAmt      string `json:"positionAmt"`
	EntryPrice       string `json:"entryPrice"`
	MarkPrice        string `json:"markPrice"`
	UnrealizedProfit string `json:"unRealizedProfit"`
	LiquidationPrice string `json:"liquidationPrice"`
	MarginType       string `json:"marginType"`
	PositionSide     string `json:"positionSide"`
	Leverage         string `json:"leverage"`
	UpdateTime       int64  `json:"updateTime"`
}

type positionState struct {
	Symbol           string
	Side             string
	Size             float64
	EntryPrice       float64
	MarkPrice        float64
	UnrealizedPnL    float64
	LiquidationPrice float64
	MarginType       string
	Leverage         string
	UpdateTime       int64
}

type positionUpdate struct {
	Symbol        string
	Side          string
	Size          float64
	EntryPrice    float64
	UnrealizedPnL float64
	MarginType    string
	UpdateTime    int64
	Remove        bool
}

type spotBalance struct {
	Asset          string
	Free           float64
	Locked         float64
	Total          float64
	QuoteValue     float64
	QuoteValueText string
	PriceSymbol    string
	PriceValue     float64
	LocalUpdate    time.Time
}

type spotAccountResponse struct {
	Balances []spotBalancePayload `json:"balances"`
}

type spotBalancePayload struct {
	Asset  string `json:"asset"`
	Free   string `json:"free"`
	Locked string `json:"locked"`
}

type panelMode string

const (
	panelFutures panelMode = "futures"
	panelSpot    panelMode = "spot"
)

type appState struct {
	mu                    sync.RWMutex
	rows                  map[string]rowState
	spotRows              map[string]rowState
	futuresChart          []klineCandle
	spotChart             []klineCandle
	positions             []positionState
	spotBalances          []spotBalance
	futuresChartSymbol    string
	spotChartSymbol       string
	panel                 panelMode
	modalMessage          string
	startedAt             time.Time
	lastError             string
	spotError             string
	accountError          string
	spotAccountError      string
	lastUpdate            time.Time
	spotLastUpdate        time.Time
	accountLastUpdate     time.Time
	spotAccountLastUpdate time.Time
	accountEnabled        bool
}

type uiModel struct {
	app         *tview.Application
	pages       *tview.Pages
	header      *tview.TextView
	status      *tview.TextView
	table       *tview.Table
	positions   *tview.Table
	chart       *tview.TextView
	footer      *tview.TextView
	help        tview.Primitive
	cfg         config
	loc         *time.Location
	state       *appState
	changeChart func(int)
	helpOpen    bool
}

func main() {
	log.SetFlags(0)
	log.SetPrefix("")

	cfg := mustLoadConfig()
	loc := mustLoadLocation(cfg.TZ)
	client := &http.Client{Timeout: cfg.Timeout}
	state := newAppState(cfg)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, client, cfg, loc, state); err != nil {
		log.Fatalf("fatal: %v", err)
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

	required := []string{"symbols", "chart_symbol", "chart_limit", "default_panel", "timeout", "tz", "rest_base", "ws_base", "no_color", "retry_delay"}
	for _, key := range required {
		if !meta.IsDefined(key) {
			return config{}, fmt.Errorf("config %s missing required field %q", path, key)
		}
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
	if (apiKey == "") != (apiSecret == "") {
		return config{}, fmt.Errorf("config %s requires both %q and %q when account auth is enabled", path, "api_key", "api_secret")
	}

	tz := strings.TrimSpace(raw.TZ)
	if tz == "" {
		return config{}, fmt.Errorf("config %s field %q cannot be empty", path, "tz")
	}
	restBase := strings.TrimRight(strings.TrimSpace(raw.RESTBase), "/")
	if restBase == "" {
		return config{}, fmt.Errorf("config %s field %q cannot be empty", path, "rest_base")
	}
	wsBase := strings.TrimRight(strings.TrimSpace(raw.WSBase), "/")
	if wsBase == "" {
		return config{}, fmt.Errorf("config %s field %q cannot be empty", path, "ws_base")
	}

	return config{
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
		filepath.Join(homeDir, ".config", "binance-ticker", "config.toml"),
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
		_, _, _, _, _, chartSymbol, _, _, _, _, _, _, _, _, _, _, _, _ := state.snapshot()
		return chartSymbol
	}
}

func run(ctx context.Context, client *http.Client, cfg config, loc *time.Location, state *appState) error {
	if len(cfg.Symbols) > 0 {
		if err := loadChartHistory(ctx, client, cfg, state); err != nil {
			state.setError(fmt.Sprintf("chart init failed: %v", err))
		}
	}
	if cfg.hasAccountAuth() && len(cfg.Symbols) > 0 {
		if err := loadInitialPositions(ctx, client, cfg, state); err != nil {
			state.setAccountError(fmt.Sprintf("positions init failed: %v", err))
		}
	}
	if cfg.hasSpot() {
		state.setSpotRows(spotSymbolsToTickers(cfg.SpotSymbols))
		if err := loadInitialSpotBalances(ctx, client, cfg, state); err != nil {
			state.setSpotAccountError(fmt.Sprintf("spot balances init failed: %v", err))
		}
		if len(cfg.SpotSymbols) > 0 {
			spotTickers := spotSymbolsToTickers(cfg.SpotSymbols)
			if len(spotTickers) > 0 && (cfg.DefaultPanel == string(panelSpot) || len(cfg.Symbols) == 0) {
				if err := loadChartHistoryForSymbol(ctx, client, defaultSpotRESTBaseURL, panelSpot, spotTickers[0], cfg.ChartLimit, state); err != nil {
					state.setSpotError(fmt.Sprintf("spot chart init failed: %v", err))
				}
			}
		}
	}

	getChartSymbol := func() string {
		_, _, _, _, _, chartSymbol, _, _, _, _, _, _, _, _, _, _, _, _ := state.snapshot()
		return chartSymbol
	}
	setChartSymbol := func(panel panelMode, symbol string) {
		state.mu.Lock()
		defer state.mu.Unlock()
		if panel == panelSpot {
			state.spotChartSymbol = symbol
		} else {
			state.futuresChartSymbol = symbol
		}
	}
	getTickerSymbols := func() []string {
		_, _, _, positions, _, _, _, _, _, _, _, _, _, _, _, _, _, _ := state.snapshot()
		combined := make([]string, 0, len(cfg.Symbols)+len(positions))
		combined = append(combined, cfg.Symbols...)
		for _, position := range positions {
			combined = append(combined, position.Symbol)
		}
		return normalizeSymbolList(combined)
	}
	getSpotTickerSymbols := func() []string {
		return spotSymbolsToTickers(cfg.SpotSymbols)
	}

	changeChart := func(offset int) {
		_, _, _, _, _, _, _, _, _, _, _, _, _, _, _, _, panel, _ := state.snapshot()

		var symbols []string
		var baseURL string
		var waitingMessage string
		var switchMessage string
		var clearErr func()
		var setErr func(string)

		switch panel {
		case panelSpot:
			symbols = spotSymbolsToTickers(cfg.SpotSymbols)
			baseURL = defaultSpotRESTBaseURL
			waitingMessage = "Spot panel is not configured"
			switchMessage = "switching spot chart to %s..."
			clearErr = state.clearSpotError
			setErr = state.setSpotError
		default:
			symbols = cfg.Symbols
			baseURL = cfg.RESTBase
			waitingMessage = "Futures is not configured"
			switchMessage = "switching chart to %s..."
			clearErr = state.clearError
			setErr = state.setError
		}

		if len(symbols) == 0 {
			state.setModal(waitingMessage)
			return
		}

		current := getChartSymbol()
		idx := indexOfSymbol(symbols, current)
		if idx < 0 {
			idx = 0
		}
		next := symbols[(idx+offset+len(symbols))%len(symbols)]
		if next == current {
			return
		}

		setChartSymbol(panel, next)
		setErr(fmt.Sprintf(switchMessage, next))
		if err := loadChartHistoryForSymbol(ctx, client, baseURL, panel, next, cfg.ChartLimit, state); err != nil {
			setErr(fmt.Sprintf("chart switch failed: %v", err))
			return
		}
		clearErr()
	}

	ui := newUI(cfg, loc, state, changeChart)
	errCh := make(chan error, 1)

	go func() {
		<-ctx.Done()
		ui.app.QueueUpdateDraw(func() {
			ui.app.Stop()
		})
	}()

	go func() {
		err := runWSLoop(ctx, cfg, state, ui.requestDraw, getChartSymbol, getTickerSymbols, isSpotTickerSymbolFunc(cfg))
		if err != nil {
			select {
			case errCh <- err:
			default:
			}
		}
	}()

	if cfg.hasAccountAuth() && len(cfg.Symbols) > 0 {
		go runUserDataLoop(ctx, client, cfg, state, ui.requestDraw)
	}
	if cfg.hasAccountAuth() && cfg.hasSpot() {
		go runSpotUserDataLoop(ctx, client, cfg, state, ui.requestDraw)
	}
	if cfg.hasSpot() {
		go func() {
			err := runSpotWSLoop(ctx, cfg, state, ui.requestDraw, getSpotTickerSymbols)
			if err != nil {
				select {
				case errCh <- err:
				default:
				}
			}
		}()
	}

	go ui.runClock(ctx)

	if err := ui.app.SetRoot(ui.root(), true).Run(); err != nil {
		return err
	}

	select {
	case err := <-errCh:
		return err
	default:
		return nil
	}
}

func runWSLoop(ctx context.Context, cfg config, state *appState, notify func(), getChartSymbol func() string, getTickerSymbols func() []string, isSpotChartSymbol func(string) bool) error {
	for {
		if ctx.Err() != nil {
			return nil
		}

		state.setError("connecting websocket...")
		notify()

		err := consumeWS(ctx, cfg, state, notify, getChartSymbol, getTickerSymbols, isSpotChartSymbol)
		if err == nil || ctx.Err() != nil {
			return nil
		}
		if errors.Is(err, errResubscribe) {
			continue
		}

		state.setError(fmt.Sprintf("websocket disconnected: %v | retry in %s", err, cfg.RetryDelay))
		notify()

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(cfg.RetryDelay):
		}
	}
}

func consumeWS(ctx context.Context, cfg config, state *appState, notify func(), getChartSymbol func() string, getTickerSymbols func() []string, isSpotChartSymbol func(string) bool) error {
	chartSymbol := getChartSymbol()
	if isSpotChartSymbol(chartSymbol) {
		chartSymbol = ""
	}
	endpoint := buildWSURL(cfg.WSBase, getTickerSymbols(), chartSymbol)
	dialer := websocket.Dialer{HandshakeTimeout: cfg.Timeout}
	conn, _, err := dialer.DialContext(ctx, endpoint, nil)
	if err != nil {
		return fmt.Errorf("dial websocket: %w", err)
	}
	defer conn.Close()

	state.clearError()
	notify()

	conn.SetReadLimit(1 << 20)
	_ = conn.SetReadDeadline(time.Now().Add(2 * cfg.Timeout))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(2 * cfg.Timeout))
	})

	pingTicker := time.NewTicker(cfg.Timeout)
	defer pingTicker.Stop()
	resubscribeTicker := time.NewTicker(time.Second)
	defer resubscribeTicker.Stop()
	baselineSymbols := strings.Join(getTickerSymbols(), ",") + "|" + chartSymbol

	readErrCh := make(chan error, 1)
	go func() {
		defer close(readErrCh)
		for {
			var envelope wsEnvelope
			if err := conn.ReadJSON(&envelope); err != nil {
				readErrCh <- err
				return
			}

			switch {
			case strings.HasSuffix(envelope.Stream, "@ticker"):
				ticker, err := parseWSTicker(envelope.Data)
				if err != nil {
					readErrCh <- fmt.Errorf("decode websocket ticker payload: %w", err)
					return
				}
				state.applyTicker(ticker)
				notify()
			case strings.HasSuffix(envelope.Stream, "@kline_1h"):
				candle, err := parseWSKline(envelope.Data)
				if err != nil {
					readErrCh <- fmt.Errorf("decode websocket kline payload: %w", err)
					return
				}
				if candle.Symbol == getChartSymbol() {
					state.applyChartCandle(panelFutures, candle, cfg.ChartLimit)
				}
				notify()
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "shutdown"), time.Now().Add(time.Second))
			return nil
		case err := <-readErrCh:
			if err == nil {
				return nil
			}
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) || errors.Is(err, netErrClosed) {
				return err
			}
			return err
		case <-pingTicker.C:
			if err := conn.WriteControl(websocket.PingMessage, []byte("ping"), time.Now().Add(time.Second)); err != nil {
				return fmt.Errorf("ping websocket: %w", err)
			}
		case <-resubscribeTicker.C:
			currentChartSymbol := getChartSymbol()
			if isSpotChartSymbol(currentChartSymbol) {
				currentChartSymbol = ""
			}
			currentSymbols := strings.Join(getTickerSymbols(), ",") + "|" + currentChartSymbol
			if currentSymbols != baselineSymbols {
				state.setError("updating market subscriptions...")
				notify()
				return errResubscribe
			}
		}
	}
}

func runSpotWSLoop(ctx context.Context, cfg config, state *appState, notify func(), getSpotTickerSymbols func() []string) error {
	for {
		if ctx.Err() != nil {
			return nil
		}

		state.setSpotError("connecting spot websocket...")
		notify()

		err := consumeSpotWS(ctx, cfg, state, notify, getSpotTickerSymbols, getChartSymbolForActivePanel(state), isSpotTickerSymbolFunc(cfg))
		if err == nil || ctx.Err() != nil {
			return nil
		}
		if errors.Is(err, errResubscribe) {
			continue
		}

		state.setSpotError(fmt.Sprintf("spot websocket disconnected: %v | retry in %s", err, cfg.RetryDelay))
		notify()

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(cfg.RetryDelay):
		}
	}
}

func consumeSpotWS(ctx context.Context, cfg config, state *appState, notify func(), getSpotTickerSymbols func() []string, getChartSymbol func() string, isSpotChartSymbol func(string) bool) error {
	chartSymbol := getChartSymbol()
	if !isSpotChartSymbol(chartSymbol) {
		chartSymbol = ""
	}
	endpoint := buildWSURL(defaultSpotWSBaseURL, getSpotTickerSymbols(), chartSymbol)
	dialer := websocket.Dialer{HandshakeTimeout: cfg.Timeout}
	conn, _, err := dialer.DialContext(ctx, endpoint, nil)
	if err != nil {
		return fmt.Errorf("dial spot websocket: %w", err)
	}
	defer conn.Close()

	state.clearSpotError()
	notify()

	conn.SetReadLimit(1 << 20)
	_ = conn.SetReadDeadline(time.Now().Add(2 * cfg.Timeout))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(2 * cfg.Timeout))
	})

	pingTicker := time.NewTicker(cfg.Timeout)
	defer pingTicker.Stop()
	resubscribeTicker := time.NewTicker(time.Second)
	defer resubscribeTicker.Stop()
	baselineSymbols := strings.Join(getSpotTickerSymbols(), ",") + "|" + chartSymbol

	readErrCh := make(chan error, 1)
	go func() {
		defer close(readErrCh)
		for {
			var envelope wsEnvelope
			if err := conn.ReadJSON(&envelope); err != nil {
				readErrCh <- err
				return
			}
			switch {
			case strings.HasSuffix(envelope.Stream, "@ticker"):
				ticker, err := parseWSTicker(envelope.Data)
				if err != nil {
					readErrCh <- fmt.Errorf("decode spot websocket ticker payload: %w", err)
					return
				}
				state.applySpotTicker(ticker)
				notify()
			case strings.HasSuffix(envelope.Stream, "@kline_1h"):
				candle, err := parseWSKline(envelope.Data)
				if err != nil {
					readErrCh <- fmt.Errorf("decode spot websocket kline payload: %w", err)
					return
				}
				if candle.Symbol == getChartSymbol() {
					state.applyChartCandle(panelSpot, candle, cfg.ChartLimit)
				}
				notify()
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "shutdown"), time.Now().Add(time.Second))
			return nil
		case err := <-readErrCh:
			if err == nil {
				return nil
			}
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) || errors.Is(err, netErrClosed) {
				return err
			}
			return err
		case <-pingTicker.C:
			if err := conn.WriteControl(websocket.PingMessage, []byte("ping"), time.Now().Add(time.Second)); err != nil {
				return fmt.Errorf("ping spot websocket: %w", err)
			}
		case <-resubscribeTicker.C:
			currentChartSymbol := getChartSymbol()
			if !isSpotChartSymbol(currentChartSymbol) {
				currentChartSymbol = ""
			}
			currentSymbols := strings.Join(getSpotTickerSymbols(), ",") + "|" + currentChartSymbol
			if currentSymbols != baselineSymbols {
				state.setSpotError("updating spot subscriptions...")
				notify()
				return errResubscribe
			}
		}
	}
}

func loadInitialSpotBalances(ctx context.Context, client *http.Client, cfg config, state *appState) error {
	balances, err := fetchSpotBalances(ctx, client, cfg)
	if err != nil {
		return err
	}
	state.setSpotBalances(balances)
	return nil
}

func fetchSpotBalances(ctx context.Context, client *http.Client, cfg config) ([]spotBalance, error) {
	if !cfg.hasAccountAuth() {
		return nil, nil
	}
	query := url.Values{}
	query.Set("timestamp", strconv.FormatInt(time.Now().UnixMilli(), 10))
	query.Set("recvWindow", strconv.FormatInt(int64(cfg.Timeout/time.Millisecond), 10))
	query.Set("omitZeroBalances", "true")

	endpoint, err := buildSignedURL(defaultSpotRESTBaseURL, spotAccountPath, query, cfg.APISecret)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build spot account request: %w", err)
	}
	req.Header.Set("X-MBX-APIKEY", cfg.APIKey)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send spot account request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read spot account response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("spot account status %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var payload spotAccountResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode spot account response: %w", err)
	}

	allowed := allowedSpotAssets(cfg.SpotSymbols)

	balances := make([]spotBalance, 0, len(payload.Balances))
	for _, item := range payload.Balances {
		balance, ok, err := parseSpotBalance(item, allowed)
		if err != nil {
			return nil, err
		}
		if ok {
			balances = append(balances, balance)
		}
	}
	sort.Slice(balances, func(i, j int) bool { return balances[i].Asset < balances[j].Asset })
	return balances, nil
}

func parseSpotBalance(item spotBalancePayload, allowed map[string]struct{}) (spotBalance, bool, error) {
	asset := strings.ToUpper(strings.TrimSpace(item.Asset))
	if _, ok := allowed[asset]; !ok {
		return spotBalance{}, false, nil
	}
	free, err := strconv.ParseFloat(item.Free, 64)
	if err != nil {
		return spotBalance{}, false, fmt.Errorf("parse spot free balance for %s: %w", asset, err)
	}
	locked, err := strconv.ParseFloat(item.Locked, 64)
	if err != nil {
		return spotBalance{}, false, fmt.Errorf("parse spot locked balance for %s: %w", asset, err)
	}
	total := free + locked
	if total <= 0 {
		return spotBalance{}, false, nil
	}
	priceSymbol := spotSymbolToTicker(asset)
	return spotBalance{Asset: asset, Free: free, Locked: locked, Total: total, PriceSymbol: priceSymbol, QuoteValueText: "-"}, true, nil
}

var (
	netErrClosed   = errors.New("use of closed network connection")
	errResubscribe = errors.New("market stream resubscribe requested")
)

func parseWSTicker(data []byte) (priceTicker, error) {
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(data, &payload); err != nil {
		return priceTicker{}, err
	}

	var symbol string
	if raw, ok := payload["s"]; ok {
		if err := json.Unmarshal(raw, &symbol); err != nil {
			return priceTicker{}, err
		}
	}

	var price string
	if raw, ok := payload["c"]; ok {
		if err := json.Unmarshal(raw, &price); err != nil {
			return priceTicker{}, err
		}
	}

	var eventTime jsonFlexibleInt64
	if raw, ok := payload["E"]; ok {
		if err := json.Unmarshal(raw, &eventTime); err != nil {
			return priceTicker{}, err
		}
	}

	if symbol == "" || price == "" {
		return priceTicker{}, fmt.Errorf("missing required fields in websocket payload")
	}

	return priceTicker{Symbol: symbol, Price: price, Time: int64(eventTime)}, nil
}

func parseWSKline(data []byte) (klineCandle, error) {
	var payload wsKlineEnvelope
	if err := json.Unmarshal(data, &payload); err != nil {
		return klineCandle{}, err
	}

	return newKlineCandle(
		payload.Symbol,
		int64(payload.Kline.StartTime),
		int64(payload.Kline.CloseTime),
		string(payload.Kline.Open),
		string(payload.Kline.High),
		string(payload.Kline.Low),
		string(payload.Kline.Close),
		payload.Kline.IsClosed,
	)
}

func loadChartHistory(ctx context.Context, client *http.Client, cfg config, state *appState) error {
	return loadChartHistoryForSymbol(ctx, client, cfg.RESTBase, panelFutures, cfg.ChartSymbol, cfg.ChartLimit, state)
}

func loadInitialPositions(ctx context.Context, client *http.Client, cfg config, state *appState) error {
	positions, err := fetchPositions(ctx, client, cfg)
	if err != nil {
		return err
	}
	state.setPositions(positions)
	return nil
}

func fetchPositions(ctx context.Context, client *http.Client, cfg config) ([]positionState, error) {
	query := url.Values{}
	query.Set("timestamp", strconv.FormatInt(time.Now().UnixMilli(), 10))
	query.Set("recvWindow", strconv.FormatInt(int64(cfg.Timeout/time.Millisecond), 10))

	endpoint, err := buildSignedURL(cfg.RESTBase, positionRiskPath, query, cfg.APISecret)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build positions request: %w", err)
	}
	req.Header.Set("X-MBX-APIKEY", cfg.APIKey)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send positions request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read positions response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("positions status %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var payload []positionRiskResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode positions response: %w", err)
	}

	positions := make([]positionState, 0, len(payload))
	for _, item := range payload {
		position, ok, err := parsePosition(item)
		if err != nil {
			return nil, err
		}
		if ok {
			positions = append(positions, position)
		}
	}

	sort.Slice(positions, func(i, j int) bool {
		if positions[i].Symbol == positions[j].Symbol {
			return positions[i].Side < positions[j].Side
		}
		return positions[i].Symbol < positions[j].Symbol
	})

	return positions, nil
}

func parsePosition(item positionRiskResponse) (positionState, bool, error) {
	size, err := strconv.ParseFloat(item.PositionAmt, 64)
	if err != nil {
		return positionState{}, false, fmt.Errorf("parse position size for %s: %w", item.Symbol, err)
	}
	if math.Abs(size) < 1e-12 {
		return positionState{}, false, nil
	}

	entryPrice, err := strconv.ParseFloat(item.EntryPrice, 64)
	if err != nil {
		return positionState{}, false, fmt.Errorf("parse entry price for %s: %w", item.Symbol, err)
	}
	markPrice, err := strconv.ParseFloat(item.MarkPrice, 64)
	if err != nil {
		return positionState{}, false, fmt.Errorf("parse mark price for %s: %w", item.Symbol, err)
	}
	unrealizedPnL, err := strconv.ParseFloat(item.UnrealizedProfit, 64)
	if err != nil {
		return positionState{}, false, fmt.Errorf("parse unrealized pnl for %s: %w", item.Symbol, err)
	}
	liquidationPrice, err := strconv.ParseFloat(item.LiquidationPrice, 64)
	if err != nil {
		return positionState{}, false, fmt.Errorf("parse liquidation price for %s: %w", item.Symbol, err)
	}

	side := strings.ToUpper(strings.TrimSpace(item.PositionSide))
	if side == "" || side == "BOTH" {
		if size > 0 {
			side = "LONG"
		} else {
			side = "SHORT"
		}
	}

	return positionState{
		Symbol:           strings.ToUpper(strings.TrimSpace(item.Symbol)),
		Side:             side,
		Size:             math.Abs(size),
		EntryPrice:       entryPrice,
		MarkPrice:        markPrice,
		UnrealizedPnL:    unrealizedPnL,
		LiquidationPrice: liquidationPrice,
		MarginType:       strings.ToUpper(strings.TrimSpace(item.MarginType)),
		Leverage:         strings.TrimSpace(item.Leverage),
		UpdateTime:       item.UpdateTime,
	}, true, nil
}

func buildSignedURL(baseURL, path string, query url.Values, secret string) (string, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("parse base url: %w", err)
	}
	parsed.Path = path
	encoded := query.Encode()
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(encoded))
	parsed.RawQuery = encoded + "&signature=" + hex.EncodeToString(mac.Sum(nil))
	return parsed.String(), nil
}

func runUserDataLoop(ctx context.Context, client *http.Client, cfg config, state *appState, notify func()) {
	for {
		if ctx.Err() != nil {
			return
		}

		state.setAccountError("connecting user data stream...")
		notify()

		listenKey, err := createListenKey(ctx, client, cfg)
		if err != nil {
			state.setAccountError(fmt.Sprintf("user data stream start failed: %v", err))
			notify()
		} else {
			state.setAccountError("")
			notify()
			err = consumeUserDataStream(ctx, client, cfg, state, notify, listenKey)
			if err == nil || ctx.Err() != nil {
				return
			}
			state.setAccountError(fmt.Sprintf("user data stream disconnected: %v | retry in %s", err, cfg.RetryDelay))
			notify()
		}

		select {
		case <-ctx.Done():
			return
		case <-time.After(cfg.RetryDelay):
		}
	}
}

func consumeUserDataStream(ctx context.Context, client *http.Client, cfg config, state *appState, notify func(), listenKey string) error {
	endpoint := strings.TrimRight(cfg.WSBase, "/") + "/ws/" + listenKey
	dialer := websocket.Dialer{HandshakeTimeout: cfg.Timeout}
	conn, _, err := dialer.DialContext(ctx, endpoint, nil)
	if err != nil {
		return fmt.Errorf("dial user data websocket: %w", err)
	}
	defer conn.Close()
	defer closeListenKey(context.Background(), client, cfg, listenKey)

	keepaliveTicker := time.NewTicker(userDataKeepaliveInterval)
	defer keepaliveTicker.Stop()
	pingTicker := time.NewTicker(cfg.Timeout)
	defer pingTicker.Stop()

	conn.SetReadLimit(1 << 20)
	_ = conn.SetReadDeadline(time.Now().Add(2 * cfg.Timeout))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(2 * cfg.Timeout))
	})

	readErrCh := make(chan error, 1)
	go func() {
		defer close(readErrCh)
		for {
			var payload userDataEvent
			if err := conn.ReadJSON(&payload); err != nil {
				readErrCh <- err
				return
			}

			switch payload.EventType {
			case "ACCOUNT_UPDATE":
				updates, err := parseAccountUpdateEvent(payload)
				if err != nil {
					readErrCh <- fmt.Errorf("decode account update payload: %w", err)
					return
				}
				state.applyPositionUpdates(updates)
				state.clearAccountError()
				notify()
			case "listenKeyExpired":
				readErrCh <- errors.New("listen key expired")
				return
			}
		}
	}()

	for {
		select {
		case <-ctx.Done():
			_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "shutdown"), time.Now().Add(time.Second))
			return nil
		case err := <-readErrCh:
			if err == nil {
				return nil
			}
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) || errors.Is(err, netErrClosed) {
				return err
			}
			return err
		case <-keepaliveTicker.C:
			if err := keepaliveListenKey(ctx, client, cfg, listenKey); err != nil {
				return fmt.Errorf("keepalive listen key: %w", err)
			}
		case <-pingTicker.C:
			if err := conn.WriteControl(websocket.PingMessage, []byte("ping"), time.Now().Add(time.Second)); err != nil {
				return fmt.Errorf("ping user data websocket: %w", err)
			}
		}
	}
}

func runSpotUserDataLoop(ctx context.Context, client *http.Client, cfg config, state *appState, notify func()) {
	for {
		if ctx.Err() != nil {
			return
		}

		state.setSpotAccountError("connecting spot user data stream...")
		notify()

		err := consumeSpotUserDataStream(ctx, client, cfg, state, notify)
		if err == nil || ctx.Err() != nil {
			return
		}

		state.setSpotAccountError(fmt.Sprintf("spot user data stream disconnected: %v | retry in %s", err, cfg.RetryDelay))
		notify()

		select {
		case <-ctx.Done():
			return
		case <-time.After(cfg.RetryDelay):
		}
	}
}

func consumeSpotUserDataStream(ctx context.Context, client *http.Client, cfg config, state *appState, notify func()) error {
	endpoint := defaultSpotWSAPIBaseURL
	dialer := websocket.Dialer{HandshakeTimeout: cfg.Timeout}
	conn, _, err := dialer.DialContext(ctx, endpoint, nil)
	if err != nil {
		return fmt.Errorf("dial spot user data websocket api: %w", err)
	}
	defer conn.Close()

	conn.SetReadLimit(1 << 20)
	_ = conn.SetReadDeadline(time.Now().Add(2 * cfg.Timeout))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(2 * cfg.Timeout))
	})

	timestamp := time.Now().UnixMilli()
	recvWindow := int64(cfg.Timeout / time.Millisecond)
	if recvWindow <= 0 {
		recvWindow = 5000
	}
	signature := signSpotUserDataStreamRequest(cfg.APIKey, cfg.APISecret, timestamp, recvWindow)
	request := map[string]any{
		"id":     fmt.Sprintf("spot-user-stream-%d", timestamp),
		"method": "userDataStream.subscribe.signature",
		"params": map[string]any{
			"apiKey":     cfg.APIKey,
			"timestamp":  timestamp,
			"recvWindow": recvWindow,
			"signature":  signature,
		},
	}
	if err := conn.WriteJSON(request); err != nil {
		return fmt.Errorf("subscribe spot user data stream: %w", err)
	}

	allowed := allowedSpotAssets(cfg.SpotSymbols)
	pingTicker := time.NewTicker(cfg.Timeout)
	defer pingTicker.Stop()

	readErrCh := make(chan error, 1)
	go func() {
		defer close(readErrCh)
		for {
			_, message, err := conn.ReadMessage()
			if err != nil {
				readErrCh <- err
				return
			}

			var eventEnvelope struct {
				SubscriptionID int             `json:"subscriptionId"`
				Event          json.RawMessage `json:"event"`
			}
			if err := json.Unmarshal(message, &eventEnvelope); err == nil && len(eventEnvelope.Event) > 0 {
				var payload spotUserDataEvent
				if err := json.Unmarshal(eventEnvelope.Event, &payload); err != nil {
					readErrCh <- fmt.Errorf("decode spot user event type: %w", err)
					return
				}

				switch payload.EventType {
				case "outboundAccountPosition":
					updates, err := parseSpotAccountUpdateEvent(eventEnvelope.Event, allowed)
					if err != nil {
						readErrCh <- fmt.Errorf("decode spot account update payload: %w", err)
						return
					}
					state.applySpotBalanceUpdates(updates)
					state.clearSpotAccountError()
					notify()
				case "balanceUpdate", "externalLockUpdate":
					balances, err := fetchSpotBalances(ctx, client, cfg)
					if err != nil {
						state.setSpotAccountError(fmt.Sprintf("spot account refresh failed: %v", err))
					} else {
						state.setSpotBalances(balances)
						state.clearSpotAccountError()
					}
					notify()
				case "eventStreamTerminated":
					readErrCh <- errors.New("spot user data stream terminated")
					return
				}
				continue
			}

			var response struct {
				Status int `json:"status"`
				Error  *struct {
					Code int    `json:"code"`
					Msg  string `json:"msg"`
				} `json:"error"`
			}
			if err := json.Unmarshal(message, &response); err != nil {
				readErrCh <- fmt.Errorf("decode spot websocket api payload: %w", err)
				return
			}
			if response.Status == 0 {
				continue
			}
			if response.Status != http.StatusOK {
				if response.Error != nil {
					readErrCh <- fmt.Errorf("spot subscribe failed: code %d: %s", response.Error.Code, response.Error.Msg)
				} else {
					readErrCh <- fmt.Errorf("spot subscribe failed: status %d", response.Status)
				}
				return
			}

			state.clearSpotAccountError()
			notify()
		}
	}()

	for {
		select {
		case <-ctx.Done():
			_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "shutdown"), time.Now().Add(time.Second))
			return nil
		case err := <-readErrCh:
			if err == nil {
				return nil
			}
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) || errors.Is(err, netErrClosed) {
				return err
			}
			return err
		case <-pingTicker.C:
			if err := conn.WriteControl(websocket.PingMessage, []byte("ping"), time.Now().Add(time.Second)); err != nil {
				return fmt.Errorf("ping spot user data websocket api: %w", err)
			}
		}
	}
}

func signSpotUserDataStreamRequest(apiKey, secret string, timestamp, recvWindow int64) string {
	payload := fmt.Sprintf("apiKey=%s&recvWindow=%d&timestamp=%d", apiKey, recvWindow, timestamp)
	mac := hmac.New(sha256.New, []byte(secret))
	_, _ = mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

func parseSpotAccountUpdateEvent(event json.RawMessage, allowed map[string]struct{}) ([]spotBalanceUpdate, error) {
	var payload spotOutboundAccountPositionEvent
	if err := json.Unmarshal(event, &payload); err != nil {
		return nil, err
	}

	updates := make([]spotBalanceUpdate, 0, len(payload.Balances))
	for _, item := range payload.Balances {
		update, ok, err := parseSpotBalanceUpdatePayload(item, int64(payload.UpdateTime), allowed)
		if err != nil {
			return nil, err
		}
		if ok {
			updates = append(updates, update)
		}
	}
	return updates, nil
}

func parseSpotBalanceUpdatePayload(item spotBalanceUpdatePayload, updateTime int64, allowed map[string]struct{}) (spotBalanceUpdate, bool, error) {
	asset := strings.ToUpper(strings.TrimSpace(item.Asset))
	if _, ok := allowed[asset]; !ok {
		return spotBalanceUpdate{}, false, nil
	}

	free, err := strconv.ParseFloat(string(item.Free), 64)
	if err != nil {
		return spotBalanceUpdate{}, false, fmt.Errorf("parse spot free update for %s: %w", asset, err)
	}
	locked, err := strconv.ParseFloat(string(item.Locked), 64)
	if err != nil {
		return spotBalanceUpdate{}, false, fmt.Errorf("parse spot locked update for %s: %w", asset, err)
	}

	return spotBalanceUpdate{
		Asset:      asset,
		Free:       free,
		Locked:     locked,
		UpdateTime: updateTime,
		Remove:     free+locked <= 0,
	}, true, nil
}

func createListenKey(ctx context.Context, client *http.Client, cfg config) (string, error) {
	resp, err := doListenKeyRequest(ctx, client, cfg, http.MethodPost, "")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read listen key response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("listen key status %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var payload userDataListenKeyResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("decode listen key response: %w", err)
	}
	if strings.TrimSpace(payload.ListenKey) == "" {
		return "", errors.New("missing listen key in response")
	}
	return payload.ListenKey, nil
}

func keepaliveListenKey(ctx context.Context, client *http.Client, cfg config, listenKey string) error {
	resp, err := doListenKeyRequest(ctx, client, cfg, http.MethodPut, listenKey)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read keepalive response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("keepalive status %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

func closeListenKey(ctx context.Context, client *http.Client, cfg config, listenKey string) {
	ctx, cancel := context.WithTimeout(ctx, cfg.Timeout)
	defer cancel()
	resp, err := doListenKeyRequest(ctx, client, cfg, http.MethodDelete, listenKey)
	if err != nil {
		return
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
}

func doListenKeyRequest(ctx context.Context, client *http.Client, cfg config, method, listenKey string) (*http.Response, error) {
	parsed, err := url.Parse(cfg.RESTBase)
	if err != nil {
		return nil, fmt.Errorf("parse base url: %w", err)
	}
	parsed.Path = listenKeyPath
	if listenKey != "" {
		query := url.Values{}
		query.Set("listenKey", listenKey)
		parsed.RawQuery = query.Encode()
	}

	req, err := http.NewRequestWithContext(ctx, method, parsed.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build listen key request: %w", err)
	}
	req.Header.Set("X-MBX-APIKEY", cfg.APIKey)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send listen key request: %w", err)
	}
	return resp, nil
}

func parseAccountUpdateEvent(event userDataEvent) ([]positionUpdate, error) {
	encoded, err := json.Marshal(event)
	if err != nil {
		return nil, err
	}

	var payload userDataAccountUpdateEvent
	if err := json.Unmarshal(encoded, &payload); err != nil {
		return nil, err
	}

	updates := make([]positionUpdate, 0, len(payload.Data.Positions))
	for _, item := range payload.Data.Positions {
		update, err := parsePositionUpdatePayload(item, int64(payload.EventTime))
		if err != nil {
			return nil, err
		}
		updates = append(updates, update)
	}
	return updates, nil
}

func parsePositionUpdatePayload(item userDataPositionPayload, updateTime int64) (positionUpdate, error) {
	size, err := strconv.ParseFloat(string(item.PositionAmt), 64)
	if err != nil {
		return positionUpdate{}, fmt.Errorf("parse position update size for %s: %w", item.Symbol, err)
	}
	entryPrice, err := strconv.ParseFloat(string(item.EntryPrice), 64)
	if err != nil {
		return positionUpdate{}, fmt.Errorf("parse position update entry for %s: %w", item.Symbol, err)
	}
	unrealizedPnL, err := strconv.ParseFloat(string(item.UnrealizedProfit), 64)
	if err != nil {
		return positionUpdate{}, fmt.Errorf("parse position update pnl for %s: %w", item.Symbol, err)
	}

	side := strings.ToUpper(strings.TrimSpace(item.PositionSide))
	if side == "" || side == "BOTH" {
		if size > 0 {
			side = "LONG"
		} else if size < 0 {
			side = "SHORT"
		}
	}

	return positionUpdate{
		Symbol:        strings.ToUpper(strings.TrimSpace(item.Symbol)),
		Side:          side,
		Size:          math.Abs(size),
		EntryPrice:    entryPrice,
		UnrealizedPnL: unrealizedPnL,
		MarginType:    strings.ToUpper(strings.TrimSpace(item.MarginType)),
		UpdateTime:    updateTime,
		Remove:        math.Abs(size) < 1e-12,
	}, nil
}

func loadChartHistoryForSymbol(ctx context.Context, client *http.Client, baseURL string, panel panelMode, symbol string, limit int, state *appState) error {
	klines, err := fetchKlines(ctx, client, baseURL, symbol, limit)
	if err != nil {
		return err
	}
	state.setChart(panel, symbol, klines)
	return nil
}

func fetchKlines(ctx context.Context, client *http.Client, baseURL, symbol string, limit int) ([]klineCandle, error) {
	endpoint, err := buildKlineURL(baseURL, symbol, limit)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status: %s", resp.Status)
	}

	var raw [][]interface{}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode kline response: %w", err)
	}

	klines := make([]klineCandle, 0, len(raw))
	for _, item := range raw {
		candle, err := parseRESTKline(symbol, item)
		if err != nil {
			return nil, err
		}
		klines = append(klines, candle)
	}

	return klines, nil
}

func buildKlineURL(baseURL, symbol string, limit int) (string, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("parse base url: %w", err)
	}
	parsed.Path = futuresKlinePath
	if strings.EqualFold(parsed.Hostname(), "api.binance.com") {
		parsed.Path = spotKlinePath
	}

	query := url.Values{}
	query.Set("symbol", symbol)
	query.Set("interval", "1h")
	query.Set("limit", strconv.Itoa(limit))
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func parseRESTKline(symbol string, item []interface{}) (klineCandle, error) {
	if len(item) < 7 {
		return klineCandle{}, fmt.Errorf("invalid kline length: %d", len(item))
	}

	openTime, err := interfaceToInt64(item[0])
	if err != nil {
		return klineCandle{}, err
	}
	open, err := interfaceToString(item[1])
	if err != nil {
		return klineCandle{}, err
	}
	high, err := interfaceToString(item[2])
	if err != nil {
		return klineCandle{}, err
	}
	low, err := interfaceToString(item[3])
	if err != nil {
		return klineCandle{}, err
	}
	closePrice, err := interfaceToString(item[4])
	if err != nil {
		return klineCandle{}, err
	}
	closeTime, err := interfaceToInt64(item[6])
	if err != nil {
		return klineCandle{}, err
	}

	return newKlineCandle(symbol, openTime, closeTime, open, high, low, closePrice, true)
}

func newKlineCandle(symbol string, openTime, closeTime int64, open, high, low, closePrice string, closed bool) (klineCandle, error) {
	openValue, err := strconv.ParseFloat(open, 64)
	if err != nil {
		return klineCandle{}, err
	}
	highValue, err := strconv.ParseFloat(high, 64)
	if err != nil {
		return klineCandle{}, err
	}
	lowValue, err := strconv.ParseFloat(low, 64)
	if err != nil {
		return klineCandle{}, err
	}
	closeValue, err := strconv.ParseFloat(closePrice, 64)
	if err != nil {
		return klineCandle{}, err
	}

	return klineCandle{
		Symbol:     symbol,
		OpenTime:   openTime,
		CloseTime:  closeTime,
		Open:       open,
		High:       high,
		Low:        low,
		Close:      closePrice,
		OpenValue:  openValue,
		HighValue:  highValue,
		LowValue:   lowValue,
		CloseValue: closeValue,
		Closed:     closed,
	}, nil
}

func interfaceToInt64(value interface{}) (int64, error) {
	switch v := value.(type) {
	case float64:
		return int64(v), nil
	case string:
		return strconv.ParseInt(v, 10, 64)
	default:
		return 0, fmt.Errorf("unsupported integer value %T", value)
	}
}

func interfaceToString(value interface{}) (string, error) {
	switch v := value.(type) {
	case string:
		return v, nil
	case float64:
		return strconv.FormatFloat(v, 'f', -1, 64), nil
	default:
		return "", fmt.Errorf("unsupported string value %T", value)
	}
}

func buildWSURL(baseURL string, symbols []string, chartSymbol string) string {
	symbols = normalizeSymbolList(symbols)
	streams := make([]string, 0, len(symbols)+1)
	for _, symbol := range symbols {
		streams = append(streams, strings.ToLower(symbol)+"@ticker")
	}
	if chartSymbol != "" {
		streams = append(streams, strings.ToLower(chartSymbol)+"@kline_1h")
	}
	return baseURL + "/stream?streams=" + strings.Join(streams, "/")
}

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

func newAppState(cfg config) *appState {
	rows := make(map[string]rowState, len(cfg.Symbols))
	for _, symbol := range cfg.Symbols {
		rows[symbol] = rowState{Symbol: symbol, Status: "waiting"}
	}
	spotRows := make(map[string]rowState, len(cfg.SpotSymbols))
	spotTickers := spotSymbolsToTickers(cfg.SpotSymbols)
	for _, symbol := range spotTickers {
		spotRows[symbol] = rowState{Symbol: symbol, Status: "waiting"}
	}
	panel := panelMode(cfg.DefaultPanel)
	if panel == "" {
		panel = panelFutures
	}

	futuresChartSymbol := ""
	if len(cfg.Symbols) > 0 {
		futuresChartSymbol = cfg.ChartSymbol
		if futuresChartSymbol == "" {
			futuresChartSymbol = cfg.Symbols[0]
		}
	}
	spotChartSymbol := ""
	if len(spotTickers) > 0 {
		spotChartSymbol = spotTickers[0]
	}

	return &appState{
		rows:               rows,
		spotRows:           spotRows,
		startedAt:          time.Now(),
		accountEnabled:     cfg.hasAccountAuth(),
		panel:              panel,
		futuresChartSymbol: futuresChartSymbol,
		spotChartSymbol:    spotChartSymbol,
	}
}

func (s *appState) setChart(panel panelMode, symbol string, candles []klineCandle) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if panel == panelSpot {
		s.spotChartSymbol = symbol
		s.spotChart = append([]klineCandle(nil), candles...)
	} else {
		s.futuresChartSymbol = symbol
		s.futuresChart = append([]klineCandle(nil), candles...)
	}
	if !s.lastUpdate.IsZero() {
		return
	}
	s.lastUpdate = time.Now()
}

func (s *appState) applyChartCandle(panel panelMode, candle klineCandle, limit int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 {
		limit = defaultChartLimit
	}

	chart := s.futuresChart
	if panel == panelSpot {
		chart = s.spotChart
	}

	if len(chart) > 0 && chart[len(chart)-1].OpenTime == candle.OpenTime {
		chart[len(chart)-1] = candle
	} else {
		chart = append(chart, candle)
		if len(chart) > limit {
			chart = append([]klineCandle(nil), chart[len(chart)-limit:]...)
		}
	}

	if panel == panelSpot {
		s.spotChart = chart
	} else {
		s.futuresChart = chart
	}
	s.lastUpdate = time.Now()
}

func (s *appState) applyTickers(tickers []priceTicker) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ticker := range tickers {
		s.applyTickerLocked(ticker)
	}
	s.lastUpdate = time.Now()
}

func (s *appState) applyTicker(ticker priceTicker) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.applyTickerLocked(ticker)
	s.lastUpdate = time.Now()
}

func (s *appState) applyTickerLocked(ticker priceTicker) {
	current, tracked := s.rows[ticker.Symbol]
	if !tracked {
		if value, err := strconv.ParseFloat(ticker.Price, 64); err == nil {
			s.refreshPositionMarketValueLocked(ticker.Symbol, value)
		}
		return
	}
	current.Symbol = ticker.Symbol
	current.Price = ticker.Price
	current.ExchangeTime = ticker.Time
	current.LocalTime = time.Now()
	current.Status = "ok"
	current.Updates++

	value, err := strconv.ParseFloat(ticker.Price, 64)
	if err == nil {
		if current.Updates > 1 || current.PriceValue != 0 {
			current.PrevValue = current.PriceValue
			current.HasPrev = true
		}
		current.PriceValue = value
		if current.HasPrev {
			current.Delta = value - current.PrevValue
			if current.PrevValue != 0 {
				current.DeltaPct = current.Delta / current.PrevValue * 100
			}
			current.Change = compareFloat(value, current.PrevValue)
		} else {
			current.Change = 0
			current.Delta = 0
			current.DeltaPct = 0
		}
		s.refreshPositionMarketValueLocked(ticker.Symbol, value)
	}

	s.rows[ticker.Symbol] = current
}

func compareFloat(a, b float64) int {
	if math.Abs(a-b) < 1e-12 {
		return 0
	}
	if a > b {
		return 1
	}
	return -1
}

func calculatePositionPnL(position positionState) float64 {
	if position.MarkPrice == 0 || position.EntryPrice == 0 || position.Size == 0 {
		return position.UnrealizedPnL
	}

	switch strings.ToUpper(strings.TrimSpace(position.Side)) {
	case "LONG":
		return (position.MarkPrice - position.EntryPrice) * position.Size
	case "SHORT":
		return (position.EntryPrice - position.MarkPrice) * position.Size
	default:
		return position.UnrealizedPnL
	}
}

func (s *appState) setPanel(panel panelMode) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.panel = panel
}

func (s *appState) setModal(message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.modalMessage = message
}

func (s *appState) clearModal() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.modalMessage = ""
}

func (s *appState) setError(message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastError = message
}

func (s *appState) clearError() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastError = ""
}

func (s *appState) setSpotError(message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.spotError = message
}

func (s *appState) clearSpotError() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.spotError = ""
}

func (s *appState) setSpotRows(symbols []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.spotRows = make(map[string]rowState, len(symbols))
	for _, symbol := range symbols {
		s.spotRows[symbol] = rowState{Symbol: symbol, Status: "waiting"}
	}
}

func (s *appState) applySpotTicker(ticker priceTicker) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, tracked := s.spotRows[ticker.Symbol]
	if !tracked {
		return
	}
	current.Symbol = ticker.Symbol
	current.Price = ticker.Price
	current.ExchangeTime = ticker.Time
	current.LocalTime = time.Now()
	current.Status = "ok"
	current.Updates++
	value, err := strconv.ParseFloat(ticker.Price, 64)
	if err == nil {
		if current.Updates > 1 || current.PriceValue != 0 {
			current.PrevValue = current.PriceValue
			current.HasPrev = true
		}
		current.PriceValue = value
		if current.HasPrev {
			current.Delta = value - current.PrevValue
			if current.PrevValue != 0 {
				current.DeltaPct = current.Delta / current.PrevValue * 100
			}
			current.Change = compareFloat(value, current.PrevValue)
		} else {
			current.Change = 0
			current.Delta = 0
			current.DeltaPct = 0
		}
		for i := range s.spotBalances {
			if s.spotBalances[i].PriceSymbol == ticker.Symbol {
				s.spotBalances[i].PriceValue = value
				s.spotBalances[i].QuoteValue = s.spotBalances[i].Total * value
				s.spotBalances[i].QuoteValueText = formatCompactFloat(s.spotBalances[i].QuoteValue)
				s.spotBalances[i].LocalUpdate = time.Now()
			}
		}
	}
	s.spotRows[ticker.Symbol] = current
	s.spotLastUpdate = time.Now()
}

func (s *appState) setSpotBalances(balances []spotBalance) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.spotBalances = append([]spotBalance(nil), balances...)
	for i := range s.spotBalances {
		if row, ok := s.spotRows[s.spotBalances[i].PriceSymbol]; ok && row.PriceValue > 0 {
			s.spotBalances[i].PriceValue = row.PriceValue
			s.spotBalances[i].QuoteValue = s.spotBalances[i].Total * row.PriceValue
			s.spotBalances[i].QuoteValueText = formatCompactFloat(s.spotBalances[i].QuoteValue)
		}
	}
	s.spotAccountLastUpdate = time.Now()
}

func (s *appState) applySpotBalanceUpdates(updates []spotBalanceUpdate) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(updates) == 0 {
		s.spotAccountLastUpdate = time.Now()
		return
	}

	indexByAsset := make(map[string]int, len(s.spotBalances))
	for i, balance := range s.spotBalances {
		indexByAsset[balance.Asset] = i
	}

	for _, update := range updates {
		if update.Asset == "" {
			continue
		}

		total := update.Free + update.Locked
		if idx, ok := indexByAsset[update.Asset]; ok {
			if update.Remove || total <= 0 {
				s.spotBalances = append(s.spotBalances[:idx], s.spotBalances[idx+1:]...)
				indexByAsset = make(map[string]int, len(s.spotBalances))
				for i, balance := range s.spotBalances {
					indexByAsset[balance.Asset] = i
				}
				continue
			}

			balance := s.spotBalances[idx]
			balance.Free = update.Free
			balance.Locked = update.Locked
			balance.Total = total
			balance.LocalUpdate = time.Now()
			if row, ok := s.spotRows[balance.PriceSymbol]; ok && row.PriceValue > 0 {
				balance.PriceValue = row.PriceValue
				balance.QuoteValue = total * row.PriceValue
				balance.QuoteValueText = formatCompactFloat(balance.QuoteValue)
			} else {
				balance.PriceValue = 0
				balance.QuoteValue = 0
				balance.QuoteValueText = "-"
			}
			s.spotBalances[idx] = balance
			continue
		}

		if update.Remove || total <= 0 {
			continue
		}

		balance := spotBalance{
			Asset:          update.Asset,
			Free:           update.Free,
			Locked:         update.Locked,
			Total:          total,
			PriceSymbol:    spotSymbolToTicker(update.Asset),
			QuoteValueText: "-",
			LocalUpdate:    time.Now(),
		}
		if row, ok := s.spotRows[balance.PriceSymbol]; ok && row.PriceValue > 0 {
			balance.PriceValue = row.PriceValue
			balance.QuoteValue = total * row.PriceValue
			balance.QuoteValueText = formatCompactFloat(balance.QuoteValue)
		}
		s.spotBalances = append(s.spotBalances, balance)
		indexByAsset[balance.Asset] = len(s.spotBalances) - 1
	}

	sort.Slice(s.spotBalances, func(i, j int) bool {
		return s.spotBalances[i].Asset < s.spotBalances[j].Asset
	})
	s.spotAccountLastUpdate = time.Now()
}

func (s *appState) setSpotAccountError(message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.spotAccountError = message
}

func (s *appState) clearSpotAccountError() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.spotAccountError = ""
}

func (s *appState) setPositions(positions []positionState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.positions = append([]positionState(nil), positions...)
	for i := range s.positions {
		if ticker, ok := s.rows[s.positions[i].Symbol]; ok && ticker.PriceValue > 0 {
			s.positions[i].MarkPrice = ticker.PriceValue
			s.positions[i].UnrealizedPnL = calculatePositionPnL(s.positions[i])
		}
	}
	s.accountLastUpdate = time.Now()
}

func (s *appState) refreshPositionMarketValueLocked(symbol string, markPrice float64) {
	for i := range s.positions {
		if s.positions[i].Symbol != symbol {
			continue
		}
		s.positions[i].MarkPrice = markPrice
		s.positions[i].UnrealizedPnL = calculatePositionPnL(s.positions[i])
	}
}

func (s *appState) applyPositionUpdates(updates []positionUpdate) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(updates) == 0 {
		s.accountLastUpdate = time.Now()
		return
	}

	indexByKey := make(map[string]int, len(s.positions))
	for i, position := range s.positions {
		indexByKey[position.Symbol+"|"+position.Side] = i
	}

	for _, update := range updates {
		if update.Symbol == "" {
			continue
		}
		key := update.Symbol + "|" + update.Side
		idx, exists := indexByKey[key]
		if update.Remove {
			if exists {
				s.positions = append(s.positions[:idx], s.positions[idx+1:]...)
				indexByKey = make(map[string]int, len(s.positions))
				for i, position := range s.positions {
					indexByKey[position.Symbol+"|"+position.Side] = i
				}
			}
			continue
		}

		position := positionState{
			Symbol:        update.Symbol,
			Side:          update.Side,
			Size:          update.Size,
			EntryPrice:    update.EntryPrice,
			UnrealizedPnL: update.UnrealizedPnL,
			MarginType:    update.MarginType,
			UpdateTime:    update.UpdateTime,
		}

		if exists {
			current := s.positions[idx]
			position.MarkPrice = current.MarkPrice
			position.LiquidationPrice = current.LiquidationPrice
			position.Leverage = current.Leverage
			if position.MarginType == "" {
				position.MarginType = current.MarginType
			}
			if position.MarkPrice > 0 {
				position.UnrealizedPnL = calculatePositionPnL(position)
			}
			s.positions[idx] = position
		} else {
			s.positions = append(s.positions, position)
			indexByKey[key] = len(s.positions) - 1
		}
	}

	sort.Slice(s.positions, func(i, j int) bool {
		if s.positions[i].Symbol == s.positions[j].Symbol {
			return s.positions[i].Side < s.positions[j].Side
		}
		return s.positions[i].Symbol < s.positions[j].Symbol
	})
	for _, row := range s.positions {
		if ticker, ok := s.rows[row.Symbol]; ok {
			positionMark := row.MarkPrice
			if positionMark == 0 {
				positionMark = ticker.PriceValue
			}
			if positionMark > 0 {
				for i := range s.positions {
					if s.positions[i].Symbol == row.Symbol && s.positions[i].Side == row.Side {
						s.positions[i].MarkPrice = positionMark
					}
				}
			}
		}
	}

	s.accountLastUpdate = time.Now()
}

func (s *appState) setAccountError(message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.accountError = message
}

func (s *appState) clearAccountError() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.accountError = ""
}

func (s *appState) snapshot() ([]rowState, []rowState, []klineCandle, []positionState, []spotBalance, string, string, string, string, string, time.Time, time.Time, time.Time, time.Time, time.Time, bool, panelMode, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows := make([]rowState, 0, len(s.rows))
	for _, row := range s.rows {
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Symbol < rows[j].Symbol })

	spotRows := make([]rowState, 0, len(s.spotRows))
	for _, row := range s.spotRows {
		spotRows = append(spotRows, row)
	}
	sort.Slice(spotRows, func(i, j int) bool { return spotRows[i].Symbol < spotRows[j].Symbol })

	chart := append([]klineCandle(nil), s.futuresChart...)
	positions := append([]positionState(nil), s.positions...)
	spotBalances := append([]spotBalance(nil), s.spotBalances...)
	chartSymbol := s.futuresChartSymbol
	if s.panel == panelSpot {
		chart = append([]klineCandle(nil), s.spotChart...)
		chartSymbol = s.spotChartSymbol
	}
	return rows, spotRows, chart, positions, spotBalances, chartSymbol, s.lastError, s.spotError, s.accountError, s.spotAccountError, s.startedAt, s.lastUpdate, s.spotLastUpdate, s.accountLastUpdate, s.spotAccountLastUpdate, s.accountEnabled, s.panel, s.modalMessage
}

func newUI(cfg config, loc *time.Location, state *appState, changeChart func(int)) *uiModel {
	app := tview.NewApplication()
	app.SetRoot(tview.NewBox(), true)
	tview.Styles.PrimitiveBackgroundColor = tcell.ColorDefault
	tview.Styles.ContrastBackgroundColor = tcell.ColorDefault
	tview.Styles.MoreContrastBackgroundColor = tcell.ColorDefault
	tview.Styles.BorderColor = tcell.ColorGray
	tview.Styles.TitleColor = tcell.ColorWhite
	tview.Styles.GraphicsColor = tcell.ColorGray
	tview.Styles.PrimaryTextColor = tcell.ColorWhite
	tview.Styles.SecondaryTextColor = tcell.ColorSilver
	tview.Styles.TertiaryTextColor = tcell.ColorGray
	tview.Styles.InverseTextColor = tcell.ColorBlack
	tview.Styles.ContrastSecondaryTextColor = tcell.ColorWhite

	header := tview.NewTextView().SetDynamicColors(true)
	status := tview.NewTextView().SetDynamicColors(true)
	table := tview.NewTable().SetBorders(false).SetSelectable(false, false).SetFixed(1, 0)
	positions := tview.NewTable().SetBorders(false).SetSelectable(false, false).SetFixed(1, 0)
	chart := tview.NewTextView().SetDynamicColors(true)
	footer := tview.NewTextView().SetDynamicColors(true)
	helpTable := tview.NewTable().SetBorders(false).SetSelectable(false, false)
	helpTable.SetBackgroundColor(tcell.ColorDefault)
	helpTable.SetCell(0, 0, tview.NewTableCell("KEY").SetAttributes(tcell.AttrBold).SetTextColor(tcell.ColorYellow).SetSelectable(false))
	helpTable.SetCell(0, 1, tview.NewTableCell("      ").SetSelectable(false))
	helpTable.SetCell(0, 2, tview.NewTableCell("ACTION").SetAttributes(tcell.AttrBold).SetTextColor(tcell.ColorYellow).SetSelectable(false))

	helpRows := [][2]string{
		{"/ or h", "Open or close help"},
		{"Tab", "Switch futures / spot panel"},
		{"Up / Left", "Previous chart symbol"},
		{"Down / Right", "Next chart symbol"},
		{"q", "Quit"},
		{"Ctrl+C", "Quit"},
		{"Esc", "Close help / modal"},
	}
	for i, row := range helpRows {
		helpTable.SetCell(i+1, 0, tview.NewTableCell(row[0]).SetSelectable(false))
		helpTable.SetCell(i+1, 1, tview.NewTableCell("      ").SetSelectable(false))
		helpTable.SetCell(i+1, 2, tview.NewTableCell(row[1]).SetSelectable(false))
	}

	helpTitle := tview.NewTextView().SetDynamicColors(true)
	helpTitle.SetBackgroundColor(tcell.ColorDefault)
	helpTitle.SetText("[::b]Shortcuts[-]")
	helpHint := tview.NewTextView().SetDynamicColors(true)
	helpHint.SetBackgroundColor(tcell.ColorDefault)
	helpHint.SetText("Esc / Enter / h / / to close")
	helpSpacerTop := tview.NewTextView()
	helpSpacerTop.SetBackgroundColor(tcell.ColorDefault)
	helpSpacerBottom := tview.NewTextView()
	helpSpacerBottom.SetBackgroundColor(tcell.ColorDefault)
	helpContent := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(helpTitle, 1, 0, false).
		AddItem(helpSpacerTop, 1, 0, false).
		AddItem(helpTable, 0, 1, false).
		AddItem(helpSpacerBottom, 1, 0, false).
		AddItem(helpHint, 1, 0, false)
	helpFrame := tview.NewFrame(helpContent)
	helpFrame.SetBorders(1, 1, 1, 1, 2, 2)
	helpFrame.SetBorder(true)
	helpFrame.SetTitle("Help")
	helpFrame.SetBackgroundColor(tcell.ColorDefault)
	help := tview.NewFlex().
		AddItem(nil, 0, 1, false).
		AddItem(tview.NewFlex().SetDirection(tview.FlexRow).
			AddItem(nil, 0, 1, false).
			AddItem(helpFrame, 12, 0, false).
			AddItem(nil, 0, 1, false), 64, 0, true).
		AddItem(nil, 0, 1, false)

	header.SetBackgroundColor(tcell.ColorDefault)
	status.SetBackgroundColor(tcell.ColorDefault)
	table.SetBackgroundColor(tcell.ColorDefault)
	positions.SetBackgroundColor(tcell.ColorDefault)
	chart.SetBackgroundColor(tcell.ColorDefault)
	footer.SetBackgroundColor(tcell.ColorDefault)
	header.SetBorder(true).SetTitle("Overview")
	status.SetBorder(true).SetTitle("Status")
	table.SetBorder(true).SetTitle("Contracts")
	positions.SetBorder(true).SetTitle("Positions")
	chart.SetBorder(true).SetTitle("1H Chart")
	footer.SetBorder(true)
	footer.SetText("/ or h help | Tab switch panel | Arrows switch chart | q / Ctrl+C quit")

	ui := &uiModel{app: app, header: header, status: status, table: table, positions: positions, chart: chart, footer: footer, help: help, cfg: cfg, loc: loc, state: state, changeChart: changeChart}
	ui.refresh()
	ui.pages = tview.NewPages().
		AddPage("main", ui.layout(), true, true).
		AddPage("help", help, true, false)

	app.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		_, _, _, _, _, _, _, _, _, _, _, _, _, _, _, _, _, modalMessage := ui.state.snapshot()
		if modalMessage != "" {
			switch event.Key() {
			case tcell.KeyEsc, tcell.KeyEnter:
				ui.state.clearModal()
				ui.refreshNow()
				return nil
			}
			switch event.Rune() {
			case 'q', 'Q', 'h', 'H', '/':
				ui.state.clearModal()
				ui.refreshNow()
				return nil
			}
			return nil
		}
		if ui.helpOpen {
			switch event.Key() {
			case tcell.KeyEsc, tcell.KeyEnter:
				ui.hideHelp()
				return nil
			case tcell.KeyTAB:
				ui.hideHelp()
				ui.togglePanel()
				return nil
			}
			switch event.Rune() {
			case 'h', 'H', '/', 'q', 'Q':
				ui.hideHelp()
				return nil
			}
			return nil
		}

		switch event.Key() {
		case tcell.KeyCtrlC:
			app.Stop()
			return nil
		case tcell.KeyUp, tcell.KeyLeft:
			if ui.changeChart != nil {
				ui.changeChart(-1)
			}
			return nil
		case tcell.KeyDown, tcell.KeyRight:
			if ui.changeChart != nil {
				ui.changeChart(1)
			}
			return nil
		case tcell.KeyTAB:
			ui.togglePanel()
			return nil
		}
		switch event.Rune() {
		case 'h', 'H', '/':
			ui.showHelp()
			return nil
		case 'q', 'Q':
			app.Stop()
			return nil
		}
		return event
	})

	return ui
}

func getChartSymbolForPanel(state *appState, panel panelMode) string {
	state.mu.RLock()
	defer state.mu.RUnlock()
	if panel == panelSpot {
		return state.spotChartSymbol
	}
	return state.futuresChartSymbol
}

func (ui *uiModel) togglePanel() {
	_, _, _, _, _, _, _, _, _, _, _, _, _, _, _, _, panel, _ := ui.state.snapshot()
	switch panel {
	case panelFutures:
		if len(ui.cfg.SpotSymbols) == 0 {
			ui.state.setModal("Spot panel is not configured")
			ui.refreshNow()
			return
		}
		ui.state.setPanel(panelSpot)
		if chartSymbol := getChartSymbolForPanel(ui.state, panelSpot); chartSymbol != "" {
			_ = loadChartHistoryForSymbol(context.Background(), &http.Client{Timeout: ui.cfg.Timeout}, defaultSpotRESTBaseURL, panelSpot, chartSymbol, ui.cfg.ChartLimit, ui.state)
		}
	case panelSpot:
		if len(ui.cfg.Symbols) == 0 {
			ui.state.setModal("Futures panel is not configured")
			ui.refreshNow()
			return
		}
		ui.state.setPanel(panelFutures)
		if chartSymbol := getChartSymbolForPanel(ui.state, panelFutures); chartSymbol != "" {
			_ = loadChartHistoryForSymbol(context.Background(), &http.Client{Timeout: ui.cfg.Timeout}, ui.cfg.RESTBase, panelFutures, chartSymbol, ui.cfg.ChartLimit, ui.state)
		}
	default:
		if len(ui.cfg.Symbols) > 0 {
			ui.state.setPanel(panelFutures)
		} else if len(ui.cfg.SpotSymbols) > 0 {
			ui.state.setPanel(panelSpot)
		}
	}
	ui.refreshNow()
}

func (ui *uiModel) layout() tview.Primitive {
	left := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(ui.table, 0, 4, false).
		AddItem(ui.positions, 0, 5, false)
	content := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(ui.header, 6, 0, false).
		AddItem(ui.status, 5, 0, false).
		AddItem(tview.NewFlex().SetDirection(tview.FlexColumn).AddItem(left, 0, 3, false).AddItem(ui.chart, 0, 2, false), 0, 1, false).
		AddItem(ui.footer, 3, 0, false)
	return content
}

func (ui *uiModel) root() tview.Primitive {
	return ui.pages
}

func (ui *uiModel) showHelp() {
	ui.helpOpen = true
	ui.pages.ShowPage("help")
	ui.app.SetFocus(ui.help)
}

func (ui *uiModel) hideHelp() {
	ui.helpOpen = false
	ui.pages.HidePage("help")
	ui.app.SetFocus(ui.chart)
}

func (ui *uiModel) runClock(ctx context.Context) {
	ticker := time.NewTicker(uiRefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ui.requestDraw()
		}
	}
}

func (ui *uiModel) requestDraw() {
	ui.app.QueueUpdateDraw(func() {
		ui.refresh()
	})
}

func (ui *uiModel) refreshNow() {
	ui.refresh()
}

func (ui *uiModel) refresh() {
	rows, spotRows, chart, positions, spotBalances, chartSymbol, lastError, spotError, accountError, spotAccountError, startedAt, lastUpdate, spotLastUpdate, accountLastUpdate, spotAccountLastUpdate, accountEnabled, panel, modalMessage := ui.state.snapshot()

	accountMode := "disabled"
	if accountEnabled {
		accountMode = "enabled | stream"
	}
	panelName := string(panel)
	if panelName == "" {
		panelName = string(panelFutures)
	}
	marketStatus := lastError
	marketUpdate := lastUpdate
	accountStatusError := accountError
	accountUpdate := accountLastUpdate
	if panel == panelSpot {
		marketStatus = spotError
		marketUpdate = spotLastUpdate
		accountStatusError = spotAccountError
		accountUpdate = spotAccountLastUpdate
	}

	ui.header.SetText(fmt.Sprintf(
		"panel: %s\nfutures: %s\nspot: %s\nconfig: %s\nnow: %s\nstarted: %s\nmarket update: %s",
		panelName,
		strings.Join(ui.cfg.Symbols, ","),
		strings.Join(ui.cfg.SpotSymbols, ","),
		ui.cfg.ConfigPath,
		formatTime(time.Now(), ui.loc, false),
		formatTime(startedAt, ui.loc, false),
		formatOptionalTime(marketUpdate, ui.loc),
	))

	statusText := "[green]ok[-]"
	if marketStatus != "" {
		statusText = ui.colorize("red", marketStatus)
	}
	accountStatus := ui.accountStatusText(accountEnabled, accountStatusError, accountUpdate)
	transport := fmt.Sprintf("retry delay=%s | futures ws=%s | futures rest=%s | spot ws=%s | spot rest=%s", ui.cfg.RetryDelay, ui.cfg.WSBase, ui.cfg.RESTBase, defaultSpotWSBaseURL, defaultSpotRESTBaseURL)
	ui.status.SetText(fmt.Sprintf("market: %s\naccount: %s\nmode: %s\n%s", statusText, accountStatus, accountMode, transport))

	if panel == panelSpot {
		ui.table.SetTitle("Spot")
		ui.positions.SetTitle("Spot Balances")
		ui.renderSpotTable(spotRows)
		ui.renderSpotBalances(spotBalances, spotAccountError)
		ui.renderChart(chart, chartSymbol)
	} else {
		ui.table.SetTitle("Contracts")
		ui.positions.SetTitle("Positions")
		ui.renderTable(rows)
		ui.renderPositions(positions, accountEnabled, accountError, accountLastUpdate)
		ui.renderChart(chart, chartSymbol)
	}

	if modalMessage != "" {
		ui.chart.SetTitle("Notice")
		ui.chart.SetText(modalMessage + "\n\nPress Esc or Enter to close")
	}
}

func (ui *uiModel) accountStatusText(accountEnabled bool, accountError string, accountLastUpdate time.Time) string {
	if !accountEnabled {
		return ui.colorize("gray", "disabled")
	}
	if accountError != "" {
		return ui.colorize("red", accountError)
	}
	if accountLastUpdate.IsZero() {
		return ui.colorize("yellow", "waiting for initial sync")
	}
	return ui.colorize("green", fmt.Sprintf("ok | last sync %s", formatOptionalTime(accountLastUpdate, ui.loc)))
}

func (ui *uiModel) renderTable(rows []rowState) {
	ui.table.Clear()
	headers := []string{"SYMBOL", "PRICE", "DELTA", "EXCHANGE_TIME", "LOCAL_UPDATE"}
	for col, header := range headers {
		cell := tview.NewTableCell(header).
			SetSelectable(false).
			SetAttributes(tcell.AttrBold).
			SetBackgroundColor(tcell.ColorDefault)
		if !ui.cfg.NoColor {
			cell.SetTextColor(tcell.ColorYellow)
		}
		ui.table.SetCell(0, col, cell)
	}

	for i, row := range rows {
		price := row.Price
		if price == "" {
			price = "-"
		}
		delta := formatDelta(row)
		exchangeTime := formatEpoch(row.ExchangeTime, ui.loc)
		localTime := formatOptionalTime(row.LocalTime, ui.loc)

		ui.table.SetCell(i+1, 0, tview.NewTableCell(row.Symbol).SetSelectable(false).SetBackgroundColor(tcell.ColorDefault))
		ui.table.SetCell(i+1, 1, tview.NewTableCell(ui.colorByChange(row.Change, price)).SetExpansion(1).SetSelectable(false).SetBackgroundColor(tcell.ColorDefault))
		ui.table.SetCell(i+1, 2, tview.NewTableCell(ui.colorByChange(row.Change, delta)).SetExpansion(1).SetSelectable(false).SetBackgroundColor(tcell.ColorDefault))
		ui.table.SetCell(i+1, 3, tview.NewTableCell(exchangeTime).SetExpansion(1).SetSelectable(false).SetBackgroundColor(tcell.ColorDefault))
		ui.table.SetCell(i+1, 4, tview.NewTableCell(localTime).SetExpansion(1).SetSelectable(false).SetBackgroundColor(tcell.ColorDefault))
	}
}

func (ui *uiModel) renderPositions(positions []positionState, accountEnabled bool, accountError string, accountLastUpdate time.Time) {
	ui.positions.Clear()
	headers := []string{"SYMBOL", "SIDE", "SIZE", "ENTRY", "MARK", "UPNL", "LIQ", "MODE"}
	for col, header := range headers {
		cell := tview.NewTableCell(header).
			SetSelectable(false).
			SetAttributes(tcell.AttrBold).
			SetBackgroundColor(tcell.ColorDefault)
		if !ui.cfg.NoColor {
			cell.SetTextColor(tcell.ColorYellow)
		}
		ui.positions.SetCell(0, col, cell)
	}

	if !accountEnabled {
		ui.positions.SetCell(1, 0, tview.NewTableCell("API credentials not configured").SetSelectable(false).SetExpansion(1).SetBackgroundColor(tcell.ColorDefault))
		return
	}

	if accountError != "" && len(positions) == 0 {
		ui.positions.SetCell(1, 0, tview.NewTableCell(ui.colorize("red", accountError)).SetSelectable(false).SetExpansion(1).SetBackgroundColor(tcell.ColorDefault))
		return
	}

	if len(positions) == 0 {
		message := "No open positions"
		if !accountLastUpdate.IsZero() {
			message = fmt.Sprintf("No open positions | last sync %s", formatOptionalTime(accountLastUpdate, ui.loc))
		}
		ui.positions.SetCell(1, 0, tview.NewTableCell(message).SetSelectable(false).SetExpansion(1).SetBackgroundColor(tcell.ColorDefault))
		return
	}

	for i, position := range positions {
		mode := strings.ToLower(position.MarginType)
		if position.Leverage != "" {
			mode = fmt.Sprintf("%s %sx", mode, position.Leverage)
		}

		ui.positions.SetCell(i+1, 0, tview.NewTableCell(position.Symbol).SetSelectable(false).SetBackgroundColor(tcell.ColorDefault))
		ui.positions.SetCell(i+1, 1, tview.NewTableCell(ui.colorBySide(position.Side, position.Side)).SetSelectable(false).SetBackgroundColor(tcell.ColorDefault))
		ui.positions.SetCell(i+1, 2, tview.NewTableCell(formatCompactFloat(position.Size)).SetSelectable(false).SetBackgroundColor(tcell.ColorDefault))
		ui.positions.SetCell(i+1, 3, tview.NewTableCell(formatCompactFloat(position.EntryPrice)).SetSelectable(false).SetBackgroundColor(tcell.ColorDefault))
		ui.positions.SetCell(i+1, 4, tview.NewTableCell(formatCompactFloat(position.MarkPrice)).SetSelectable(false).SetBackgroundColor(tcell.ColorDefault))
		ui.positions.SetCell(i+1, 5, tview.NewTableCell(ui.colorByChange(compareFloat(position.UnrealizedPnL, 0), formatSignedCompactFloat(position.UnrealizedPnL))).SetSelectable(false).SetBackgroundColor(tcell.ColorDefault))
		ui.positions.SetCell(i+1, 6, tview.NewTableCell(formatOptionalCompactFloat(position.LiquidationPrice)).SetSelectable(false).SetBackgroundColor(tcell.ColorDefault))
		ui.positions.SetCell(i+1, 7, tview.NewTableCell(mode).SetSelectable(false).SetBackgroundColor(tcell.ColorDefault))
	}
}

func (ui *uiModel) renderSpotTable(rows []rowState) {
	ui.table.Clear()
	headers := []string{"SYMBOL", "PRICE", "DELTA", "EXCHANGE_TIME", "LOCAL_UPDATE"}
	for col, header := range headers {
		cell := tview.NewTableCell(header).SetSelectable(false).SetAttributes(tcell.AttrBold).SetBackgroundColor(tcell.ColorDefault)
		if !ui.cfg.NoColor {
			cell.SetTextColor(tcell.ColorYellow)
		}
		ui.table.SetCell(0, col, cell)
	}
	for i, row := range rows {
		price := row.Price
		if price == "" {
			price = "-"
		}
		ui.table.SetCell(i+1, 0, tview.NewTableCell(row.Symbol).SetSelectable(false).SetBackgroundColor(tcell.ColorDefault))
		ui.table.SetCell(i+1, 1, tview.NewTableCell(ui.colorByChange(row.Change, price)).SetSelectable(false).SetBackgroundColor(tcell.ColorDefault))
		ui.table.SetCell(i+1, 2, tview.NewTableCell(ui.colorByChange(row.Change, formatDelta(row))).SetSelectable(false).SetBackgroundColor(tcell.ColorDefault))
		ui.table.SetCell(i+1, 3, tview.NewTableCell(formatEpoch(row.ExchangeTime, ui.loc)).SetSelectable(false).SetBackgroundColor(tcell.ColorDefault))
		ui.table.SetCell(i+1, 4, tview.NewTableCell(formatOptionalTime(row.LocalTime, ui.loc)).SetSelectable(false).SetBackgroundColor(tcell.ColorDefault))
	}
}

func (ui *uiModel) renderSpotBalances(balances []spotBalance, spotAccountError string) {
	ui.positions.Clear()
	headers := []string{"ASSET", "FREE", "LOCKED", "TOTAL", "USDT", "PRICE"}
	for col, header := range headers {
		cell := tview.NewTableCell(header).SetSelectable(false).SetAttributes(tcell.AttrBold).SetBackgroundColor(tcell.ColorDefault)
		if !ui.cfg.NoColor {
			cell.SetTextColor(tcell.ColorYellow)
		}
		ui.positions.SetCell(0, col, cell)
	}
	if spotAccountError != "" && len(balances) == 0 {
		ui.positions.SetCell(1, 0, tview.NewTableCell(ui.colorize("red", spotAccountError)).SetSelectable(false).SetExpansion(1).SetBackgroundColor(tcell.ColorDefault))
		return
	}
	if len(balances) == 0 {
		ui.positions.SetCell(1, 0, tview.NewTableCell("No configured spot balances").SetSelectable(false).SetExpansion(1).SetBackgroundColor(tcell.ColorDefault))
		return
	}
	for i, balance := range balances {
		ui.positions.SetCell(i+1, 0, tview.NewTableCell(balance.Asset).SetSelectable(false).SetBackgroundColor(tcell.ColorDefault))
		ui.positions.SetCell(i+1, 1, tview.NewTableCell(formatCompactFloat(balance.Free)).SetSelectable(false).SetBackgroundColor(tcell.ColorDefault))
		ui.positions.SetCell(i+1, 2, tview.NewTableCell(formatCompactFloat(balance.Locked)).SetSelectable(false).SetBackgroundColor(tcell.ColorDefault))
		ui.positions.SetCell(i+1, 3, tview.NewTableCell(formatCompactFloat(balance.Total)).SetSelectable(false).SetBackgroundColor(tcell.ColorDefault))
		ui.positions.SetCell(i+1, 4, tview.NewTableCell(balance.QuoteValueText).SetSelectable(false).SetBackgroundColor(tcell.ColorDefault))
		ui.positions.SetCell(i+1, 5, tview.NewTableCell(formatOptionalCompactFloat(balance.PriceValue)).SetSelectable(false).SetBackgroundColor(tcell.ColorDefault))
	}
}

func buildSpotSummary(balances []spotBalance) string {
	if len(balances) == 0 {
		return "waiting for spot balances..."
	}

	var total float64
	for _, balance := range balances {
		total += balance.QuoteValue
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("assets %d\n", len(balances)))
	b.WriteString(fmt.Sprintf("total usdt %s\n\n", formatCompactFloat(total)))
	for _, balance := range balances {
		b.WriteString(fmt.Sprintf("%-6s total %-12s usdt %-12s\n", balance.Asset, formatCompactFloat(balance.Total), balance.QuoteValueText))
	}
	return b.String()
}

func (ui *uiModel) renderChart(candles []klineCandle, symbol string) {
	if symbol == "" {
		symbol = ui.cfg.ChartSymbol
	}
	ui.chart.SetTitle(fmt.Sprintf("1H Chart - %s", symbol))
	ui.chart.SetText(buildChartText(candles, ui.cfg.NoColor))
}

func (ui *uiModel) colorize(color, text string) string {
	if ui.cfg.NoColor {
		return text
	}
	return fmt.Sprintf("[%s]%s[-]", color, escapeTView(text))
}

func (ui *uiModel) colorByChange(change int, text string) string {
	if ui.cfg.NoColor {
		return text
	}
	switch {
	case change > 0:
		return fmt.Sprintf("[%s]%s[-]", bullColorTag, escapeTView(text))
	case change < 0:
		return fmt.Sprintf("[%s]%s[-]", bearColorTag, escapeTView(text))
	default:
		return fmt.Sprintf("[%s]%s[-]", neutralColorTag, escapeTView(text))
	}
}

func (ui *uiModel) colorBySide(side, text string) string {
	if ui.cfg.NoColor {
		return text
	}
	switch strings.ToUpper(strings.TrimSpace(side)) {
	case "LONG":
		return fmt.Sprintf("[%s]%s[-]", bullColorTag, escapeTView(text))
	case "SHORT":
		return fmt.Sprintf("[%s]%s[-]", bearColorTag, escapeTView(text))
	default:
		return fmt.Sprintf("[%s]%s[-]", neutralColorTag, escapeTView(text))
	}
}

func escapeTView(text string) string {
	replacer := strings.NewReplacer("[", "[[", "]", "]]")
	return replacer.Replace(text)
}

func printSnapshot(cfg config, loc *time.Location, state *appState) {
	rows, spotRows, chart, positions, spotBalances, chartSymbol, lastError, spotError, accountError, spotAccountError, startedAt, lastUpdate, spotLastUpdate, accountLastUpdate, spotAccountLastUpdate, accountEnabled, panel, _ := state.snapshot()

	fmt.Printf("mode: ws\npanel: %s\nfutures symbols: %s\nspot symbols: %s\nconfig: %s\nstarted: %s\nfutures update: %s\nspot update: %s\n",
		panel,
		strings.Join(cfg.Symbols, ","),
		strings.Join(cfg.SpotSymbols, ","),
		cfg.ConfigPath,
		formatTime(startedAt, loc, false),
		formatOptionalTime(lastUpdate, loc),
		formatOptionalTime(spotLastUpdate, loc),
	)
	if lastError == "" {
		fmt.Println("futures status: ok")
	} else {
		fmt.Printf("futures status: %s\n", lastError)
	}
	if spotError == "" {
		fmt.Println("spot status: ok")
	} else {
		fmt.Printf("spot status: %s\n", spotError)
	}
	if accountEnabled {
		if accountError == "" {
			fmt.Printf("futures account: ok | last sync: %s\n", formatOptionalTime(accountLastUpdate, loc))
		} else {
			fmt.Printf("futures account: %s\n", accountError)
		}
		if spotAccountError == "" {
			fmt.Printf("spot account: ok | last sync: %s\n", formatOptionalTime(spotAccountLastUpdate, loc))
		} else {
			fmt.Printf("spot account: %s\n", spotAccountError)
		}
	} else {
		fmt.Println("account: disabled")
	}
	if len(rows) > 0 {
		fmt.Printf("\n%-14s %-18s %-18s %-26s %-26s\n", "SYMBOL", "PRICE", "DELTA", "EXCHANGE_TIME", "LOCAL_UPDATE")
		for _, row := range rows {
			price := row.Price
			if price == "" {
				price = "-"
			}
			fmt.Printf("%-14s %-18s %-18s %-26s %-26s\n", row.Symbol, price, formatDelta(row), formatEpoch(row.ExchangeTime, loc), formatOptionalTime(row.LocalTime, loc))
		}
	}
	if len(spotRows) > 0 {
		fmt.Println("\nSPOT")
		fmt.Printf("%-14s %-18s %-18s %-26s %-26s\n", "SYMBOL", "PRICE", "DELTA", "EXCHANGE_TIME", "LOCAL_UPDATE")
		for _, row := range spotRows {
			price := row.Price
			if price == "" {
				price = "-"
			}
			fmt.Printf("%-14s %-18s %-18s %-26s %-26s\n", row.Symbol, price, formatDelta(row), formatEpoch(row.ExchangeTime, loc), formatOptionalTime(row.LocalTime, loc))
		}
	}
	if len(positions) > 0 {
		fmt.Println("\nPOSITIONS")
		fmt.Printf("%-12s %-8s %-12s %-12s %-12s %-12s %-12s %-12s\n", "SYMBOL", "SIDE", "SIZE", "ENTRY", "MARK", "UPNL", "LIQ", "MODE")
		for _, position := range positions {
			mode := strings.ToLower(position.MarginType)
			if position.Leverage != "" {
				mode = fmt.Sprintf("%s %sx", mode, position.Leverage)
			}
			fmt.Printf("%-12s %-8s %-12s %-12s %-12s %-12s %-12s %-12s\n", position.Symbol, position.Side, formatCompactFloat(position.Size), formatCompactFloat(position.EntryPrice), formatCompactFloat(position.MarkPrice), formatSignedCompactFloat(position.UnrealizedPnL), formatOptionalCompactFloat(position.LiquidationPrice), mode)
		}
	}
	if len(spotBalances) > 0 {
		fmt.Println("\nSPOT BALANCES")
		fmt.Printf("%-10s %-14s %-14s %-14s %-14s %-14s\n", "ASSET", "FREE", "LOCKED", "TOTAL", "USDT", "PRICE")
		for _, balance := range spotBalances {
			fmt.Printf("%-10s %-14s %-14s %-14s %-14s %-14s\n", balance.Asset, formatCompactFloat(balance.Free), formatCompactFloat(balance.Locked), formatCompactFloat(balance.Total), balance.QuoteValueText, formatOptionalCompactFloat(balance.PriceValue))
		}
	}
	if len(chart) > 0 {
		fmt.Printf("\n1H chart (%s):\n%s\n", chartSymbol, stripTViewTags(buildChartText(chart, true)))
	}
}

func buildChartText(candles []klineCandle, noColor bool) string {
	if len(candles) == 0 {
		return "waiting for 1h kline data..."
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

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
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

func formatCompactFloat(value float64) string {
	abs := math.Abs(value)
	precision := 6
	switch {
	case abs >= 1000:
		precision = 2
	case abs >= 1:
		precision = 4
	case abs >= 0.01:
		precision = 5
	}
	return trimTrailingZeros(strconv.FormatFloat(value, 'f', precision, 64))
}

func formatSignedCompactFloat(value float64) string {
	formatted := formatCompactFloat(math.Abs(value))
	switch {
	case value > 0:
		return "+" + formatted
	case value < 0:
		return "-" + formatted
	default:
		return formatted
	}
}

func formatOptionalCompactFloat(value float64) string {
	if math.Abs(value) < 1e-12 {
		return "-"
	}
	return formatCompactFloat(value)
}

func trimTrailingZeros(value string) string {
	if !strings.Contains(value, ".") {
		return value
	}
	value = strings.TrimRight(value, "0")
	value = strings.TrimRight(value, ".")
	if value == "-0" || value == "+0" || value == "" {
		return "0"
	}
	return value
}

func formatDelta(row rowState) string {
	if !row.HasPrev {
		return "-"
	}
	return fmt.Sprintf("%+.6f (%+.2f%%)", row.Delta, row.DeltaPct)
}

func formatEpoch(timestampMS int64, loc *time.Location) string {
	if timestampMS <= 0 {
		return "-"
	}
	return time.UnixMilli(timestampMS).In(loc).Format("2006-01-02 15:04:05.000 MST")
}

func formatOptionalTime(t time.Time, loc *time.Location) string {
	if t.IsZero() {
		return "-"
	}
	return formatTime(t, loc, true)
}

func formatTime(t time.Time, loc *time.Location, millis bool) string {
	if millis {
		return t.In(loc).Format("2006-01-02 15:04:05.000 MST")
	}
	return t.In(loc).Format("2006-01-02 15:04:05 MST")
}

func mustLoadLocation(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		log.Fatalf("fatal: load timezone %q: %v", name, err)
	}
	return loc
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
