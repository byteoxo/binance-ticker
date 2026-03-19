package main

// Gate.io WebSocket helpers.
//
// Gate.io Futures WS: wss://fx-ws.gateio.ws/v4/ws/usdt
//   Subscribe: {"time": <unix>, "channel": "futures.tickers", "event": "subscribe", "payload": ["BTC_USDT"]}
//   Ticker push: {"channel": "futures.tickers", "event": "update", "result": [{...}]}
//   Kline subscribe: {"channel": "futures.candlesticks", "event": "subscribe", "payload": ["1h", "BTC_USDT"]}
//   Kline push: {"channel": "futures.candlesticks", "event": "update", "result": {...}}
//
// Gate.io Spot WS: wss://api.gateio.ws/ws/v4/
//   Subscribe: {"time": <unix>, "channel": "spot.tickers", "event": "subscribe", "payload": ["BTC_USDT"]}
//   Ticker push: {"channel": "spot.tickers", "event": "update", "result": {...}}
//   Kline: {"channel": "spot.candlesticks", "event": "subscribe", "payload": ["1h", "BTC_USDT"]}

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/gorilla/websocket"
)

type gateWSEnvelope struct {
	Channel string          `json:"channel"`
	Event   string          `json:"event"`
	Error   *gateWSError    `json:"error"`
	Result  json.RawMessage `json:"result"`
}

type gateWSError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Gate.io futures ticker payload (inside result array)
type gateFuturesTicker struct {
	Contract       string `json:"contract"`
	Last           string `json:"last"`
	ChangePercentage string `json:"change_percentage"`
	High24H        string `json:"high_24h"`
	Low24H         string `json:"low_24h"`
	Volume24H      string `json:"volume_24h"`     // contracts
	Volume24HUSDT  string `json:"volume_24h_settle"` // settle currency volume
	IndexPrice     string `json:"index_price"`
	MarkPrice      string `json:"mark_price"`
}

// Gate.io spot ticker payload
type gateSpotTicker struct {
	CurrencyPair    string `json:"currency_pair"`
	Last            string `json:"last"`
	ChangePercentage string `json:"change_percentage"`
	High24H         string `json:"high_24h"`
	Low24H          string `json:"low_24h"`
	BaseVolume      string `json:"base_volume"`
	QuoteVolume     string `json:"quote_volume"`
}

// Gate.io futures candlestick payload
type gateFuturesKline struct {
	T  int64  `json:"t"`  // unix seconds
	O  string `json:"o"`
	H  string `json:"h"`
	L  string `json:"l"`
	C  string `json:"c"`
	V  int64  `json:"v"`  // contracts volume
	N  string `json:"n"`  // contract name
}

// Gate.io spot candlestick inside result
type gateSpotKline struct {
	T  int64  `json:"t"`  // unix seconds
	O  string `json:"o"`
	H  string `json:"h"`
	L  string `json:"l"`
	C  string `json:"c"`
	V  string `json:"v"`  // base volume
	N  string `json:"n"`  // currency pair
}

func runGateWSLoop(ctx context.Context, cfg config, state *appState, notify func(), getChartSymbol func() string, getChartInterval func() string, getTickerSymbols func() []string, isSpotChartSymbol func(string) bool) error {
	for {
		if ctx.Err() != nil {
			return nil
		}

		state.setError("connecting gate websocket...")
		notify()

		err := consumeGateWS(ctx, cfg, state, notify, getChartSymbol, getChartInterval, getTickerSymbols, isSpotChartSymbol)
		if err == nil || ctx.Err() != nil {
			return nil
		}
		if errors.Is(err, errResubscribe) {
			continue
		}

		state.setError(fmt.Sprintf("gate websocket disconnected: %v | retry in %s", err, cfg.RetryDelay))
		notify()

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(cfg.RetryDelay):
		}
	}
}

func consumeGateWS(ctx context.Context, cfg config, state *appState, notify func(), getChartSymbol func() string, getChartInterval func() string, getTickerSymbols func() []string, isSpotChartSymbol func(string) bool) error {
	chartSymbol := getChartSymbol()
	if isSpotChartSymbol(chartSymbol) {
		chartSymbol = ""
	}
	chartInterval := getChartInterval()

	dialer := websocket.Dialer{HandshakeTimeout: cfg.Timeout}
	conn, _, err := dialer.DialContext(ctx, cfg.WSBase, nil)
	if err != nil {
		return fmt.Errorf("dial gate websocket: %w", err)
	}
	defer conn.Close()

	// Subscribe to tickers.
	symbols := getTickerSymbols()
	if err := gateSubscribe(conn, "futures.tickers", symbols, ""); err != nil {
		return fmt.Errorf("gate ticker subscribe: %w", err)
	}
	// Subscribe to kline if chart symbol set.
	if chartSymbol != "" {
		iv := gateInterval(chartInterval)
		if err := gateSubscribe(conn, "futures.candlesticks", []string{chartSymbol}, iv); err != nil {
			return fmt.Errorf("gate kline subscribe: %w", err)
		}
	}

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
	baselineSymbols := strings.Join(symbols, ",") + "|" + chartSymbol + "|" + chartInterval

	readErrCh := make(chan error, 1)
	go func() {
		defer close(readErrCh)
		for {
			var env gateWSEnvelope
			if err := conn.ReadJSON(&env); err != nil {
				readErrCh <- err
				return
			}
			if env.Error != nil {
				readErrCh <- fmt.Errorf("gate ws error %d: %s", env.Error.Code, env.Error.Message)
				return
			}
			if env.Event != "update" {
				continue
			}

			switch env.Channel {
			case "futures.tickers":
				var tickers []gateFuturesTicker
				if err := json.Unmarshal(env.Result, &tickers); err != nil {
					readErrCh <- fmt.Errorf("decode gate futures tickers: %w", err)
					return
				}
				for _, t := range tickers {
					ticker := gateTickerToPrice(t)
					state.applyTicker(ticker)
				}
				notify()
			case "futures.candlesticks":
				candle, err := parseGateFuturesWSKline(env.Result)
				if err != nil {
					readErrCh <- fmt.Errorf("decode gate futures kline: %w", err)
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
			// Gate.io uses application-level ping: {"time": ..., "channel": "futures.ping"}
			ping := map[string]interface{}{
				"time":    time.Now().Unix(),
				"channel": "futures.ping",
			}
			if err := conn.WriteJSON(ping); err != nil {
				return fmt.Errorf("ping gate websocket: %w", err)
			}
		case <-resubscribeTicker.C:
			currentChartSymbol := getChartSymbol()
			if isSpotChartSymbol(currentChartSymbol) {
				currentChartSymbol = ""
			}
			currentSymbols := strings.Join(getTickerSymbols(), ",") + "|" + currentChartSymbol + "|" + getChartInterval()
			if currentSymbols != baselineSymbols {
				state.setError("updating gate market subscriptions...")
				notify()
				return errResubscribe
			}
		}
	}
}

func runGateSpotWSLoop(ctx context.Context, cfg config, state *appState, notify func(), getSpotTickerSymbols func() []string, spotWSBase string) error {
	for {
		if ctx.Err() != nil {
			return nil
		}

		state.setSpotError("connecting gate spot websocket...")
		notify()

		err := consumeGateSpotWS(ctx, cfg, state, notify, getSpotTickerSymbols, getChartSymbolForActivePanel(state), state.getChartInterval, isSpotTickerSymbolFunc(cfg), spotWSBase)
		if err == nil || ctx.Err() != nil {
			return nil
		}
		if errors.Is(err, errResubscribe) {
			continue
		}

		state.setSpotError(fmt.Sprintf("gate spot websocket disconnected: %v | retry in %s", err, cfg.RetryDelay))
		notify()

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(cfg.RetryDelay):
		}
	}
}

func consumeGateSpotWS(ctx context.Context, cfg config, state *appState, notify func(), getSpotTickerSymbols func() []string, getChartSymbol func() string, getChartInterval func() string, isSpotChartSymbol func(string) bool, wsBase string) error {
	chartSymbol := getChartSymbol()
	if !isSpotChartSymbol(chartSymbol) {
		chartSymbol = ""
	}
	chartInterval := getChartInterval()

	dialer := websocket.Dialer{HandshakeTimeout: cfg.Timeout}
	conn, _, err := dialer.DialContext(ctx, wsBase, nil)
	if err != nil {
		return fmt.Errorf("dial gate spot websocket: %w", err)
	}
	defer conn.Close()

	symbols := getSpotTickerSymbols()
	if err := gateSubscribe(conn, "spot.tickers", symbols, ""); err != nil {
		return fmt.Errorf("gate spot ticker subscribe: %w", err)
	}
	if chartSymbol != "" {
		iv := gateInterval(chartInterval)
		if err := gateSubscribe(conn, "spot.candlesticks", []string{chartSymbol}, iv); err != nil {
			return fmt.Errorf("gate spot kline subscribe: %w", err)
		}
	}

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
	baselineSymbols := strings.Join(symbols, ",") + "|" + chartSymbol + "|" + chartInterval

	readErrCh := make(chan error, 1)
	go func() {
		defer close(readErrCh)
		for {
			var env gateWSEnvelope
			if err := conn.ReadJSON(&env); err != nil {
				readErrCh <- err
				return
			}
			if env.Error != nil {
				readErrCh <- fmt.Errorf("gate spot ws error %d: %s", env.Error.Code, env.Error.Message)
				return
			}
			if env.Event != "update" {
				continue
			}

			switch env.Channel {
			case "spot.tickers":
				var t gateSpotTicker
				if err := json.Unmarshal(env.Result, &t); err != nil {
					readErrCh <- fmt.Errorf("decode gate spot ticker: %w", err)
					return
				}
				ticker := gateSpotTickerToPrice(t)
				state.applySpotTicker(ticker)
				notify()
			case "spot.candlesticks":
				candle, err := parseGateSpotWSKline(env.Result)
				if err != nil {
					readErrCh <- fmt.Errorf("decode gate spot kline: %w", err)
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
			ping := map[string]interface{}{
				"time":    time.Now().Unix(),
				"channel": "spot.ping",
			}
			if err := conn.WriteJSON(ping); err != nil {
				return fmt.Errorf("ping gate spot websocket: %w", err)
			}
		case <-resubscribeTicker.C:
			currentChartSymbol := getChartSymbol()
			if !isSpotChartSymbol(currentChartSymbol) {
				currentChartSymbol = ""
			}
			currentSymbols := strings.Join(getSpotTickerSymbols(), ",") + "|" + currentChartSymbol + "|" + getChartInterval()
			if currentSymbols != baselineSymbols {
				state.setSpotError("updating gate spot subscriptions...")
				notify()
				return errResubscribe
			}
		}
	}
}

// gateSubscribe sends a subscription request.
// For tickers: payload = symbols (no extra param).
// For candlesticks: payload = [interval, symbol].
func gateSubscribe(conn *websocket.Conn, channel string, symbols []string, interval string) error {
	var payload []string
	if interval != "" {
		// candlestick: payload is [interval, symbol1], [interval, symbol2]…
		// Gate.io only supports one symbol per candlestick subscription message,
		// so we only subscribe to the first (chart) symbol.
		if len(symbols) > 0 {
			payload = []string{interval, symbols[0]}
		}
	} else {
		payload = symbols
	}

	msg := map[string]interface{}{
		"time":    time.Now().Unix(),
		"channel": channel,
		"event":   "subscribe",
		"payload": payload,
	}
	return conn.WriteJSON(msg)
}

func gateTickerToPrice(t gateFuturesTicker) priceTicker {
	changePct, _ := strconv.ParseFloat(t.ChangePercentage, 64)
	high, _ := strconv.ParseFloat(t.High24H, 64)
	low, _ := strconv.ParseFloat(t.Low24H, 64)
	vol, _ := strconv.ParseFloat(t.Volume24HUSDT, 64)
	price, _ := strconv.ParseFloat(t.Last, 64)
	change := price * changePct / 100
	return priceTicker{
		Symbol:       t.Contract,
		Price:        t.Last,
		Time:         time.Now().UnixMilli(),
		ChangePct24h: changePct,
		Change24h:    change,
		High24h:      high,
		Low24h:       low,
		Volume24h:    vol,
	}
}

func gateSpotTickerToPrice(t gateSpotTicker) priceTicker {
	changePct, _ := strconv.ParseFloat(t.ChangePercentage, 64)
	high, _ := strconv.ParseFloat(t.High24H, 64)
	low, _ := strconv.ParseFloat(t.Low24H, 64)
	vol, _ := strconv.ParseFloat(t.BaseVolume, 64)
	price, _ := strconv.ParseFloat(t.Last, 64)
	change := price * changePct / 100
	return priceTicker{
		Symbol:       t.CurrencyPair,
		Price:        t.Last,
		Time:         time.Now().UnixMilli(),
		ChangePct24h: changePct,
		Change24h:    change,
		High24h:      high,
		Low24h:       low,
		Volume24h:    vol,
	}
}

func parseGateFuturesWSKline(data json.RawMessage) (klineCandle, error) {
	// Gate.io futures candlesticks WS result is an array of objects:
	// [{"t": unix_sec, "o": "...", "h": "...", "l": "...", "c": "...", "v": 123, "n": "1h_BTC_USDT"}]
	// The "n" field is "<interval>_<contract>", e.g. "1h_BTC_USDT".
	var items []struct {
		T int64  `json:"t"`
		O string `json:"o"`
		H string `json:"h"`
		L string `json:"l"`
		C string `json:"c"`
		V int64  `json:"v"`
		N string `json:"n"`
	}
	if err := json.Unmarshal(data, &items); err != nil {
		return klineCandle{}, err
	}
	if len(items) == 0 {
		return klineCandle{}, fmt.Errorf("empty gate futures kline result")
	}
	k := items[0]
	// Extract contract symbol from "n" field: "1h_BTC_USDT" -> "BTC_USDT"
	symbol := k.N
	if idx := strings.Index(k.N, "_"); idx >= 0 {
		symbol = k.N[idx+1:]
	}
	openTime := k.T * 1000
	vol := strconv.FormatInt(k.V, 10)
	return newKlineCandle(symbol, openTime, openTime, k.O, k.H, k.L, k.C, vol, false)
}

func parseGateSpotWSKline(data json.RawMessage) (klineCandle, error) {
	// Gate.io spot candlesticks WS result is an array of objects:
	// [{"t": unix_sec, "o": "...", "h": "...", "l": "...", "c": "...", "v": "base_vol", "n": "1h_BTC_USDT"}]
	// The "n" field is "<interval>_<currency_pair>".
	var items []struct {
		T int64  `json:"t"`
		O string `json:"o"`
		H string `json:"h"`
		L string `json:"l"`
		C string `json:"c"`
		V string `json:"v"`
		N string `json:"n"`
	}
	if err := json.Unmarshal(data, &items); err != nil {
		return klineCandle{}, err
	}
	if len(items) == 0 {
		return klineCandle{}, fmt.Errorf("empty gate spot kline result")
	}
	k := items[0]
	// Extract currency pair from "n" field: "1h_BTC_USDT" -> "BTC_USDT"
	symbol := k.N
	if idx := strings.Index(k.N, "_"); idx >= 0 {
		symbol = k.N[idx+1:]
	}
	openTime := k.T * 1000
	return newKlineCandle(symbol, openTime, openTime, k.O, k.H, k.L, k.C, k.V, false)
}
