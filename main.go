package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	defaultRESTBaseURL = "https://fapi.binance.com"
	defaultWSBaseURL   = "wss://fstream.binance.com"
	tickerPath         = "/fapi/v2/ticker/price"
	klinePath          = "/fapi/v1/klines"
	defaultTimeout     = 8 * time.Second
	uiRefreshInterval  = time.Second
	defaultChartLimit  = 48
	defaultChartHeight = 12
	chartCandleWidth   = 3
	chartCandleGap     = 1
	chartStride        = chartCandleWidth + chartCandleGap
	bullColorTag       = "#00c853"
	bearColorTag       = "#e53935"
	neutralColorTag    = "#9aa0a6"
)

type config struct {
	Symbols     []string
	ChartSymbol string
	ChartLimit  int
	Interval    time.Duration
	Timeout     time.Duration
	TZ          string
	RESTBase    string
	WSBase      string
	Mode        string
	Once        bool
	NoColor     bool
	RetryDelay  time.Duration
	ConfigPath  string
}

type rawConfig struct {
	Symbols     []string `toml:"symbols"`
	ChartSymbol string   `toml:"chart_symbol"`
	ChartLimit  int      `toml:"chart_limit"`
	Interval    string   `toml:"interval"`
	Timeout     string   `toml:"timeout"`
	TZ          string   `toml:"tz"`
	RESTBase    string   `toml:"rest_base"`
	WSBase      string   `toml:"ws_base"`
	Mode        string   `toml:"mode"`
	Once        bool     `toml:"once"`
	NoColor     bool     `toml:"no_color"`
	RetryDelay  string   `toml:"retry_delay"`
}

type priceTicker struct {
	Symbol string `json:"symbol"`
	Price  string `json:"price"`
	Time   int64  `json:"time"`
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

type appState struct {
	mu          sync.RWMutex
	rows        map[string]rowState
	chart       []klineCandle
	chartSymbol string
	startedAt   time.Time
	lastError   string
	lastUpdate  time.Time
	mode        string
}

type uiModel struct {
	app    *tview.Application
	header *tview.TextView
	status *tview.TextView
	table  *tview.Table
	chart  *tview.TextView
	footer *tview.TextView
	cfg    config
	loc    *time.Location
	state  *appState
}

func main() {
	log.SetFlags(0)
	log.SetPrefix("")

	cfg := mustLoadConfig()
	loc := mustLoadLocation(cfg.TZ)
	client := &http.Client{Timeout: cfg.Timeout}
	state := newAppState(cfg.Symbols, cfg.Mode)

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

	required := []string{"symbols", "chart_symbol", "chart_limit", "interval", "timeout", "tz", "rest_base", "ws_base", "mode", "once", "no_color", "retry_delay"}
	for _, key := range required {
		if !meta.IsDefined(key) {
			return config{}, fmt.Errorf("config %s missing required field %q", path, key)
		}
	}

	symbols := normalizeSymbols(strings.Join(raw.Symbols, ","))
	if len(symbols) == 0 {
		return config{}, fmt.Errorf("config %s has no valid symbols", path)
	}

	chartSymbol := strings.ToUpper(strings.TrimSpace(raw.ChartSymbol))
	if chartSymbol == "" {
		return config{}, fmt.Errorf("config %s field %q cannot be empty", path, "chart_symbol")
	}
	chartLimit := raw.ChartLimit
	if chartLimit <= 0 {
		return config{}, fmt.Errorf("config %s field %q must be greater than 0", path, "chart_limit")
	}

	interval, err := time.ParseDuration(strings.TrimSpace(raw.Interval))
	if err != nil || interval <= 0 {
		return config{}, fmt.Errorf("config %s field %q must be a valid positive duration", path, "interval")
	}
	timeout, err := time.ParseDuration(strings.TrimSpace(raw.Timeout))
	if err != nil || timeout <= 0 {
		return config{}, fmt.Errorf("config %s field %q must be a valid positive duration", path, "timeout")
	}
	retryDelay, err := time.ParseDuration(strings.TrimSpace(raw.RetryDelay))
	if err != nil || retryDelay <= 0 {
		return config{}, fmt.Errorf("config %s field %q must be a valid positive duration", path, "retry_delay")
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

	mode := strings.ToLower(strings.TrimSpace(raw.Mode))
	if mode != "poll" && mode != "ws" {
		return config{}, fmt.Errorf("config %s field %q must be either poll or ws", path, "mode")
	}
	if raw.Once {
		mode = "poll"
	}

	return config{
		Symbols:     symbols,
		ChartSymbol: chartSymbol,
		ChartLimit:  chartLimit,
		Interval:    interval,
		Timeout:     timeout,
		TZ:          tz,
		RESTBase:    restBase,
		WSBase:      wsBase,
		Mode:        mode,
		Once:        raw.Once,
		NoColor:     raw.NoColor || os.Getenv("NO_COLOR") != "",
		RetryDelay:  retryDelay,
		ConfigPath:  path,
	}, nil
}

func resolveConfigPath() (string, error) {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}

	candidates := []string{
		"./config.toml",
		filepath.Join(homeDir, ".config", "binance-futures-ticker", "config.toml"),
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

func run(ctx context.Context, client *http.Client, cfg config, loc *time.Location, state *appState) error {
	if err := loadChartHistory(ctx, client, cfg, state); err != nil {
		state.setError(fmt.Sprintf("chart init failed: %v", err))
	}

	if cfg.Once {
		if err := runPollOnce(ctx, client, cfg, state); err != nil {
			return err
		}
		printSnapshot(cfg, loc, state)
		return nil
	}

	ui := newUI(cfg, loc, state)
	errCh := make(chan error, 1)

	go func() {
		<-ctx.Done()
		ui.app.QueueUpdateDraw(func() {
			ui.app.Stop()
		})
	}()

	go func() {
		var err error
		if cfg.Mode == "poll" {
			err = runPollLoop(ctx, client, cfg, state, ui.requestDraw)
		} else {
			err = runWSLoop(ctx, cfg, state, ui.requestDraw)
		}
		if err != nil {
			select {
			case errCh <- err:
			default:
			}
		}
	}()

	go ui.runClock(ctx)

	if err := ui.app.SetRoot(ui.layout(), true).Run(); err != nil {
		return err
	}

	select {
	case err := <-errCh:
		return err
	default:
		return nil
	}
}

func runPollOnce(ctx context.Context, client *http.Client, cfg config, state *appState) error {
	tickers, err := fetchPrices(ctx, client, cfg.RESTBase, cfg.Symbols)
	if err != nil {
		return err
	}
	state.clearError()
	state.applyTickers(tickers)
	return nil
}

func runPollLoop(ctx context.Context, client *http.Client, cfg config, state *appState, notify func()) error {
	update := func() {
		tickers, err := fetchPrices(ctx, client, cfg.RESTBase, cfg.Symbols)
		if err != nil {
			state.setError(err.Error())
			notify()
			return
		}
		state.clearError()
		state.applyTickers(tickers)
		notify()
	}

	update()
	ticker := time.NewTicker(cfg.Interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			update()
		}
	}
}

func runWSLoop(ctx context.Context, cfg config, state *appState, notify func()) error {
	for {
		if ctx.Err() != nil {
			return nil
		}

		state.setError("connecting websocket...")
		notify()

		err := consumeWS(ctx, cfg, state, notify)
		if err == nil || ctx.Err() != nil {
			return nil
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

func consumeWS(ctx context.Context, cfg config, state *appState, notify func()) error {
	endpoint := buildWSURL(cfg.WSBase, cfg.Symbols, cfg.ChartSymbol)
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
				state.applyChartCandle(candle, cfg.ChartLimit)
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
		}
	}
}

var netErrClosed = errors.New("use of closed network connection")

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
	klines, err := fetchKlines(ctx, client, cfg.RESTBase, cfg.ChartSymbol, cfg.ChartLimit)
	if err != nil {
		return err
	}
	state.setChart(cfg.ChartSymbol, klines)
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
		candle, err := parseRESTKline(item)
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
	parsed.Path = klinePath

	query := url.Values{}
	query.Set("symbol", symbol)
	query.Set("interval", "1h")
	query.Set("limit", strconv.Itoa(limit))
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func parseRESTKline(item []interface{}) (klineCandle, error) {
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

	return newKlineCandle(openTime, closeTime, open, high, low, closePrice, true)
}

func newKlineCandle(openTime, closeTime int64, open, high, low, closePrice string, closed bool) (klineCandle, error) {
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

func fetchPrices(ctx context.Context, client *http.Client, baseURL string, symbols []string) ([]priceTicker, error) {
	tickers := make([]priceTicker, len(symbols))
	errCh := make(chan error, len(symbols))

	var wg sync.WaitGroup
	for i, symbol := range symbols {
		wg.Add(1)
		go func(idx int, symbol string) {
			defer wg.Done()

			ticker, err := fetchSinglePrice(ctx, client, baseURL, symbol)
			if err != nil {
				errCh <- fmt.Errorf("symbol %s: %w", symbol, err)
				return
			}

			tickers[idx] = ticker
		}(i, symbol)
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		if err != nil {
			return nil, err
		}
	}

	return tickers, nil
}

func fetchSinglePrice(ctx context.Context, client *http.Client, baseURL, symbol string) (priceTicker, error) {
	endpoint, err := buildRESTURL(baseURL, symbol)
	if err != nil {
		return priceTicker{}, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return priceTicker{}, fmt.Errorf("build request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return priceTicker{}, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return priceTicker{}, fmt.Errorf("unexpected status: %s", resp.Status)
	}

	var ticker priceTicker
	if err := json.NewDecoder(resp.Body).Decode(&ticker); err != nil {
		return priceTicker{}, fmt.Errorf("decode ticker response: %w", err)
	}
	if ticker.Symbol == "" {
		return priceTicker{}, fmt.Errorf("empty ticker response")
	}

	return ticker, nil
}

func buildRESTURL(baseURL, symbol string) (string, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("parse base url: %w", err)
	}
	parsed.Path = tickerPath

	query := url.Values{}
	query.Set("symbol", symbol)
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}

func buildWSURL(baseURL string, symbols []string, chartSymbol string) string {
	streams := make([]string, 0, len(symbols))
	for _, symbol := range symbols {
		streams = append(streams, strings.ToLower(symbol)+"@ticker")
	}
	if chartSymbol != "" {
		streams = append(streams, strings.ToLower(chartSymbol)+"@kline_1h")
	}
	return baseURL + "/stream?streams=" + strings.Join(streams, "/")
}

func newAppState(symbols []string, mode string) *appState {
	rows := make(map[string]rowState, len(symbols))
	for _, symbol := range symbols {
		rows[symbol] = rowState{Symbol: symbol, Status: "waiting"}
	}
	return &appState{rows: rows, startedAt: time.Now(), mode: mode}
}

func (s *appState) setChart(symbol string, candles []klineCandle) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.chartSymbol = symbol
	s.chart = append([]klineCandle(nil), candles...)
	if !s.lastUpdate.IsZero() {
		return
	}
	s.lastUpdate = time.Now()
}

func (s *appState) applyChartCandle(candle klineCandle, limit int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 {
		limit = defaultChartLimit
	}
	if len(s.chart) > 0 && s.chart[len(s.chart)-1].OpenTime == candle.OpenTime {
		s.chart[len(s.chart)-1] = candle
	} else {
		s.chart = append(s.chart, candle)
		if len(s.chart) > limit {
			s.chart = append([]klineCandle(nil), s.chart[len(s.chart)-limit:]...)
		}
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
	current := s.rows[ticker.Symbol]
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

func (s *appState) snapshot() ([]rowState, []klineCandle, string, string, time.Time, time.Time, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows := make([]rowState, 0, len(s.rows))
	for _, row := range s.rows {
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Symbol < rows[j].Symbol })

	chart := append([]klineCandle(nil), s.chart...)
	return rows, chart, s.chartSymbol, s.lastError, s.startedAt, s.lastUpdate, s.mode
}

func newUI(cfg config, loc *time.Location, state *appState) *uiModel {
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
	chart := tview.NewTextView().SetDynamicColors(true)
	footer := tview.NewTextView().SetDynamicColors(true)

	header.SetBackgroundColor(tcell.ColorDefault)
	status.SetBackgroundColor(tcell.ColorDefault)
	table.SetBackgroundColor(tcell.ColorDefault)
	chart.SetBackgroundColor(tcell.ColorDefault)
	footer.SetBackgroundColor(tcell.ColorDefault)
	header.SetBorder(true).SetTitle("Overview")
	status.SetBorder(true).SetTitle("Status")
	table.SetBorder(true).SetTitle("Contracts")
	chart.SetBorder(true).SetTitle("1H Chart")
	footer.SetBorder(true)
	footer.SetText("q / Ctrl+C to quit")

	ui := &uiModel{app: app, header: header, status: status, table: table, chart: chart, footer: footer, cfg: cfg, loc: loc, state: state}
	ui.refresh()

	app.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		switch event.Key() {
		case tcell.KeyCtrlC:
			app.Stop()
			return nil
		}
		switch event.Rune() {
		case 'q', 'Q':
			app.Stop()
			return nil
		}
		return event
	})

	return ui
}

func (ui *uiModel) layout() tview.Primitive {
	body := tview.NewFlex().SetDirection(tview.FlexColumn).
		AddItem(ui.table, 0, 3, false).
		AddItem(ui.chart, 0, 2, false)

	content := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(ui.header, 5, 0, false).
		AddItem(ui.status, 4, 0, false).
		AddItem(body, 0, 1, false).
		AddItem(ui.footer, 3, 0, false)
	return content
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

func (ui *uiModel) refresh() {
	rows, chart, chartSymbol, lastError, startedAt, lastUpdate, mode := ui.state.snapshot()

	ui.header.SetText(fmt.Sprintf(
		"mode: %s\nsymbols: %s\nconfig: %s\nnow: %s\nstarted: %s\nlast update: %s",
		mode,
		strings.Join(ui.cfg.Symbols, ","),
		ui.cfg.ConfigPath,
		formatTime(time.Now(), ui.loc, false),
		formatTime(startedAt, ui.loc, false),
		formatOptionalTime(lastUpdate, ui.loc),
	))

	statusText := "[green]ok[-]"
	if lastError != "" {
		statusText = ui.colorize("red", lastError)
	}
	transport := fmt.Sprintf("mode=%s | timeout=%s", mode, ui.cfg.Timeout)
	if mode == "poll" {
		transport = fmt.Sprintf("poll interval=%s | rest=%s", ui.cfg.Interval, ui.cfg.RESTBase)
	} else {
		transport = fmt.Sprintf("retry delay=%s | ws=%s", ui.cfg.RetryDelay, ui.cfg.WSBase)
	}
	ui.status.SetText(fmt.Sprintf("status: %s\n%s", statusText, transport))

	ui.renderTable(rows)
	ui.renderChart(chart, chartSymbol)
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

func escapeTView(text string) string {
	replacer := strings.NewReplacer("[", "[[", "]", "]]")
	return replacer.Replace(text)
}

func printSnapshot(cfg config, loc *time.Location, state *appState) {
	rows, chart, chartSymbol, lastError, startedAt, lastUpdate, mode := state.snapshot()
	fmt.Printf("mode: %s\nsymbols: %s\nconfig: %s\nstarted: %s\nlast update: %s\n", mode, strings.Join(cfg.Symbols, ","), cfg.ConfigPath, formatTime(startedAt, loc, false), formatOptionalTime(lastUpdate, loc))
	if lastError == "" {
		fmt.Println("status: ok")
	} else {
		fmt.Printf("status: %s\n", lastError)
	}
	fmt.Printf("%-14s %-18s %-18s %-26s %-26s\n", "SYMBOL", "PRICE", "DELTA", "EXCHANGE_TIME", "LOCAL_UPDATE")
	for _, row := range rows {
		price := row.Price
		if price == "" {
			price = "-"
		}
		fmt.Printf("%-14s %-18s %-18s %-26s %-26s\n", row.Symbol, price, formatDelta(row), formatEpoch(row.ExchangeTime, loc), formatOptionalTime(row.LocalTime, loc))
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

	chartWidth := len(candles)*chartStride - chartCandleGap
	rows := make([][]string, defaultChartHeight)
	for y := 0; y < defaultChartHeight; y++ {
		rows[y] = make([]string, chartWidth)
		for x := range rows[y] {
			rows[y][x] = " "
		}
	}

	for i, candle := range candles {
		baseX := i * chartStride
		wickX := baseX + chartCandleWidth/2
		highY := scaleValue(candle.HighValue, low, span)
		lowY := scaleValue(candle.LowValue, low, span)
		openY := scaleValue(candle.OpenValue, low, span)
		closeY := scaleValue(candle.CloseValue, low, span)

		color := bullColorTag
		if candle.CloseValue < candle.OpenValue {
			color = bearColorTag
		} else if candle.CloseValue == candle.OpenValue {
			color = neutralColorTag
		}

		upper := minInt(openY, closeY)
		lower := maxInt(openY, closeY)

		for y := highY; y <= lowY; y++ {
			rows[y][wickX] = uiGlyph("┃", noColor, color)
		}
		for y := upper; y <= lower; y++ {
			for dx := 0; dx < chartCandleWidth; dx++ {
				rows[y][baseX+dx] = uiGlyph("█", noColor, color)
			}
		}
		if upper == lower {
			for dx := 0; dx < chartCandleWidth; dx++ {
				rows[upper][baseX+dx] = uiGlyph("▀", noColor, color)
			}
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
