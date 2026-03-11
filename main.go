package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
)

const (
	defaultRESTBaseURL = "https://fapi.binance.com"
	defaultWSBaseURL   = "wss://fstream.binance.com"
	tickerPath         = "/fapi/v2/ticker/price"
	defaultTimeout     = 8 * time.Second
	renderInterval     = 250 * time.Millisecond

	ansiReset = "\033[0m"
	ansiRed   = "\033[31m"
	ansiGreen = "\033[32m"
	ansiGray  = "\033[90m"
	ansiBold  = "\033[1m"
)

type config struct {
	Symbols    []string
	Interval   time.Duration
	Timeout    time.Duration
	TZ         string
	RESTBase   string
	WSBase     string
	Mode       string
	Once       bool
	NoColor    bool
	RetryDelay time.Duration
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

type jsonFlexibleInt64 int64

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

type appState struct {
	mu         sync.RWMutex
	rows       map[string]rowState
	startedAt  time.Time
	lastError  string
	lastUpdate time.Time
	mode       string
}

func main() {
	log.SetFlags(0)
	log.SetPrefix("")

	cfg := parseFlags()
	loc := mustLoadLocation(cfg.TZ)
	client := &http.Client{Timeout: cfg.Timeout}
	state := newAppState(cfg.Symbols, cfg.Mode)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, client, cfg, loc, state); err != nil {
		log.Fatalf("fatal: %v", err)
	}
}

func parseFlags() config {
	symbolsFlag := flag.String("symbols", "ETHUSDT", "Comma-separated Binance USD-M futures symbols, e.g. ETHUSDT,BTCUSDT")
	intervalFlag := flag.Duration("interval", 3*time.Second, "Polling interval for poll mode, e.g. 1s, 1500ms, 5s")
	timeoutFlag := flag.Duration("timeout", defaultTimeout, "HTTP/WebSocket dial timeout")
	tzFlag := flag.String("tz", "Asia/Shanghai", "IANA timezone name for terminal output, e.g. Asia/Shanghai")
	restBaseFlag := flag.String("base-url", defaultRESTBaseURL, "Binance Futures REST API base URL")
	wsBaseFlag := flag.String("ws-base-url", defaultWSBaseURL, "Binance Futures WebSocket base URL")
	modeFlag := flag.String("mode", "poll", "Data source mode: poll or ws")
	onceFlag := flag.Bool("once", false, "Fetch once and exit; only valid in poll mode")
	noColorFlag := flag.Bool("no-color", false, "Disable ANSI colors")
	retryDelayFlag := flag.Duration("retry-delay", 2*time.Second, "Reconnect delay for ws mode")
	flag.Parse()

	if *intervalFlag <= 0 {
		log.Fatal("fatal: -interval must be greater than 0")
	}
	if *timeoutFlag <= 0 {
		log.Fatal("fatal: -timeout must be greater than 0")
	}
	if *retryDelayFlag <= 0 {
		log.Fatal("fatal: -retry-delay must be greater than 0")
	}

	mode := strings.ToLower(strings.TrimSpace(*modeFlag))
	if mode != "poll" && mode != "ws" {
		log.Fatal("fatal: -mode must be either poll or ws")
	}
	if *onceFlag && mode != "poll" {
		log.Fatal("fatal: -once is only supported in poll mode")
	}

	symbols := normalizeSymbols(*symbolsFlag)
	if len(symbols) == 0 {
		log.Fatal("fatal: at least one symbol is required")
	}

	noColor := *noColorFlag || os.Getenv("NO_COLOR") != ""

	return config{
		Symbols:    symbols,
		Interval:   *intervalFlag,
		Timeout:    *timeoutFlag,
		TZ:         *tzFlag,
		RESTBase:   strings.TrimRight(*restBaseFlag, "/"),
		WSBase:     strings.TrimRight(*wsBaseFlag, "/"),
		Mode:       mode,
		Once:       *onceFlag,
		NoColor:    noColor,
		RetryDelay: *retryDelayFlag,
	}
}

func run(ctx context.Context, client *http.Client, cfg config, loc *time.Location, state *appState) error {
	defer restoreTerminal()
	fmt.Print("\033[?25l")

	if cfg.Mode == "poll" && cfg.Once {
		if err := runPollOnce(ctx, client, cfg, state); err != nil {
			state.setError(err.Error())
			renderScreen(loc, cfg, state)
			return err
		}
		renderScreen(loc, cfg, state)
		return nil
	}

	errCh := make(chan error, 1)
	go renderLoop(ctx, loc, cfg, state)

	go func() {
		var err error
		if cfg.Mode == "poll" {
			err = runPollLoop(ctx, client, cfg, state)
		} else {
			err = runWSLoop(ctx, cfg, state)
		}
		if err != nil {
			select {
			case errCh <- err:
			default:
			}
		}
	}()

	select {
	case <-ctx.Done():
		renderScreen(loc, cfg, state)
		return nil
	case err := <-errCh:
		state.setError(err.Error())
		renderScreen(loc, cfg, state)
		return err
	}
}

func runPollOnce(ctx context.Context, client *http.Client, cfg config, state *appState) error {
	tickers, err := fetchPrices(ctx, client, cfg.RESTBase, cfg.Symbols)
	if err != nil {
		return err
	}
	state.applyTickers(tickers)
	return nil
}

func runPollLoop(ctx context.Context, client *http.Client, cfg config, state *appState) error {
	update := func() {
		tickers, err := fetchPrices(ctx, client, cfg.RESTBase, cfg.Symbols)
		if err != nil {
			state.setError(err.Error())
			return
		}
		state.clearError()
		state.applyTickers(tickers)
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

func runWSLoop(ctx context.Context, cfg config, state *appState) error {
	for {
		if ctx.Err() != nil {
			return nil
		}

		state.setError("connecting websocket...")
		err := consumeWS(ctx, cfg, state)
		if err == nil || ctx.Err() != nil {
			return nil
		}

		state.setError(fmt.Sprintf("websocket disconnected: %v | retry in %s", err, cfg.RetryDelay))
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(cfg.RetryDelay):
		}
	}
}

func consumeWS(ctx context.Context, cfg config, state *appState) error {
	endpoint := buildWSURL(cfg.WSBase, cfg.Symbols)
	dialer := websocket.Dialer{HandshakeTimeout: cfg.Timeout}
	conn, _, err := dialer.DialContext(ctx, endpoint, nil)
	if err != nil {
		return fmt.Errorf("dial websocket: %w", err)
	}
	defer conn.Close()

	state.clearError()
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

			ticker, err := parseWSTicker(envelope.Data)
			if err != nil {
				readErrCh <- fmt.Errorf("decode websocket payload: %w", err)
				return
			}

			state.applyTicker(ticker)
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

	return priceTicker{
		Symbol: symbol,
		Price:  price,
		Time:   int64(eventTime),
	}, nil
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

func buildWSURL(baseURL string, symbols []string) string {
	streams := make([]string, 0, len(symbols))
	for _, symbol := range symbols {
		streams = append(streams, strings.ToLower(symbol)+"@ticker")
	}
	return baseURL + "/stream?streams=" + strings.Join(streams, "/")
}

func newAppState(symbols []string, mode string) *appState {
	rows := make(map[string]rowState, len(symbols))
	for _, symbol := range symbols {
		rows[symbol] = rowState{Symbol: symbol, Status: "waiting"}
	}
	return &appState{
		rows:      rows,
		startedAt: time.Now(),
		mode:      mode,
	}
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

func (s *appState) snapshot() ([]rowState, string, time.Time, time.Time, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows := make([]rowState, 0, len(s.rows))
	for _, row := range s.rows {
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Symbol < rows[j].Symbol })

	return rows, s.lastError, s.startedAt, s.lastUpdate, s.mode
}

func renderLoop(ctx context.Context, loc *time.Location, cfg config, state *appState) {
	ticker := time.NewTicker(renderInterval)
	defer ticker.Stop()

	for {
		renderScreen(loc, cfg, state)
		select {
		case <-ctx.Done():
			renderScreen(loc, cfg, state)
			return
		case <-ticker.C:
		}
	}
}

func renderScreen(loc *time.Location, cfg config, state *appState) {
	rows, lastError, startedAt, lastUpdate, mode := state.snapshot()

	var b strings.Builder
	b.WriteString("\033[H\033[2J")
	b.WriteString(style(cfg, ansiBold, "Binance USD-M Futures Ticker"))
	b.WriteString("\n")
	b.WriteString(fmt.Sprintf("mode: %s | symbols: %s | now: %s\n", mode, strings.Join(cfg.Symbols, ","), formatTime(time.Now(), loc, false)))
	if mode == "poll" {
		b.WriteString(fmt.Sprintf("poll interval: %s | timeout: %s | rest: %s\n", cfg.Interval, cfg.Timeout, cfg.RESTBase))
	} else {
		b.WriteString(fmt.Sprintf("retry delay: %s | timeout: %s | ws: %s\n", cfg.RetryDelay, cfg.Timeout, cfg.WSBase))
	}
	b.WriteString(fmt.Sprintf("started: %s | last update: %s\n", formatTime(startedAt, loc, false), formatOptionalTime(lastUpdate, loc)))
	if lastError == "" {
		b.WriteString("status: ok\n")
	} else {
		b.WriteString("status: ")
		b.WriteString(style(cfg, ansiRed, lastError))
		b.WriteString("\n")
	}
	b.WriteString("\n")
	b.WriteString(fmt.Sprintf("%-14s %-18s %-18s %-26s %-26s\n", "SYMBOL", "PRICE", "DELTA", "EXCHANGE_TIME", "LOCAL_UPDATE"))
	b.WriteString(fmt.Sprintf("%-14s %-18s %-18s %-26s %-26s\n", strings.Repeat("-", 14), strings.Repeat("-", 18), strings.Repeat("-", 18), strings.Repeat("-", 26), strings.Repeat("-", 26)))

	for _, row := range rows {
		price := row.Price
		if price == "" {
			price = "-"
		}
		delta := formatDelta(row)
		exchangeTime := formatEpoch(row.ExchangeTime, loc)
		localTime := formatOptionalTime(row.LocalTime, loc)

		b.WriteString(fmt.Sprintf(
			"%-14s %-18s %-18s %-26s %-26s\n",
			row.Symbol,
			padStyled(colorByChange(cfg, row.Change, price), 18),
			padStyled(colorByChange(cfg, row.Change, delta), 18),
			exchangeTime,
			localTime,
		))
	}

	b.WriteString("\nCtrl+C to quit\n")
	fmt.Print(b.String())
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

func colorByChange(cfg config, change int, text string) string {
	switch {
	case cfg.NoColor:
		return text
	case change > 0:
		return ansiGreen + text + ansiReset
	case change < 0:
		return ansiRed + text + ansiReset
	default:
		return ansiGray + text + ansiReset
	}
}

func style(cfg config, ansi, text string) string {
	if cfg.NoColor {
		return text
	}
	return ansi + text + ansiReset
}

func padStyled(text string, width int) string {
	plain := stripANSI(text)
	if len(plain) >= width {
		return text
	}
	return text + strings.Repeat(" ", width-len(plain))
}

func stripANSI(input string) string {
	var b strings.Builder
	inEscape := false
	for i := 0; i < len(input); i++ {
		ch := input[i]
		if inEscape {
			if (ch >= 'A' && ch <= 'Z') || (ch >= 'a' && ch <= 'z') {
				inEscape = false
			}
			continue
		}
		if ch == 0x1b {
			inEscape = true
			continue
		}
		b.WriteByte(ch)
	}
	return b.String()
}

func restoreTerminal() {
	fmt.Print("\033[0m\033[?25h\n")
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
