package main

// OKX WebSocket API v5:
// - Tickers (and most public feeds): wss://ws.okx.com:8443/ws/v5/public
// - Candlesticks: wss://ws.okx.com:8443/ws/v5/business (required since Jun 2023)

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

const defaultOKXBusinessWSURL = "wss://ws.okx.com:8443/ws/v5/business"

// okxBusinessWSURLFromPublic maps the configured public WS URL to the business endpoint for candle channels.
func okxBusinessWSURLFromPublic(publicWSBase string) string {
	u := strings.TrimRight(strings.TrimSpace(publicWSBase), "/")
	if strings.Contains(u, "/ws/v5/public") {
		return strings.Replace(u, "/ws/v5/public", "/ws/v5/business", 1)
	}
	return defaultOKXBusinessWSURL
}

func prepareOKXConn(conn *websocket.Conn, timeout time.Duration) {
	conn.SetReadLimit(1 << 20)
	_ = conn.SetReadDeadline(time.Now().Add(2 * timeout))
	conn.SetPongHandler(func(string) error {
		return conn.SetReadDeadline(time.Now().Add(2 * timeout))
	})
}

// okxReadNextJSON reads one JSON push from OKX. The exchange uses plain-text "ping"/"pong"
// on the wire (not {"op":"ping"} — that yields error 60012).
func okxReadNextJSON(conn *websocket.Conn, timeout time.Duration) (json.RawMessage, error) {
	for {
		_ = conn.SetReadDeadline(time.Now().Add(2 * timeout))
		mt, msg, err := conn.ReadMessage()
		if err != nil {
			return nil, err
		}
		if mt != websocket.TextMessage {
			continue
		}
		if len(msg) == 0 {
			continue
		}
		switch string(msg) {
		case "pong":
			continue
		case "ping":
			_ = conn.SetWriteDeadline(time.Now().Add(timeout))
			_ = conn.WriteMessage(websocket.TextMessage, []byte("pong"))
			continue
		}
		return json.RawMessage(msg), nil
	}
}

func okxSendPing(conn *websocket.Conn, timeout time.Duration) error {
	_ = conn.SetWriteDeadline(time.Now().Add(timeout))
	return conn.WriteMessage(websocket.TextMessage, []byte("ping"))
}

type okxWSPush struct {
	Arg   json.RawMessage `json:"arg"`
	Data  json.RawMessage `json:"data"`
	Event string          `json:"event"`
	Op    string          `json:"op"`
	Msg   string          `json:"msg"`
	Code  string          `json:"code"`
}

func okxCandleChannel(interval string) string {
	return "candle" + okxBar(interval)
}

func runOKXWSLoop(ctx context.Context, cfg config, state *appState, notify func(), getChartSymbol func() string, getChartInterval func() string, getTickerSymbols func() []string, isSpotChartSymbol func(string) bool) error {
	for {
		if ctx.Err() != nil {
			return nil
		}

		state.setError("connecting okx websocket...")
		notify()

		err := consumeOKXWS(ctx, cfg, state, notify, getChartSymbol, getChartInterval, getTickerSymbols, isSpotChartSymbol)
		if err == nil || ctx.Err() != nil {
			return nil
		}
		if errors.Is(err, errResubscribe) {
			continue
		}

		state.setError(fmt.Sprintf("okx websocket disconnected: %v | retry in %s", err, cfg.RetryDelay))
		notify()

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(cfg.RetryDelay):
		}
	}
}

func consumeOKXWS(ctx context.Context, cfg config, state *appState, notify func(), getChartSymbol func() string, getChartInterval func() string, getTickerSymbols func() []string, isSpotChartSymbol func(string) bool) error {
	chartSym := getChartSymbol()
	if isSpotChartSymbol(chartSym) {
		chartSym = ""
	}
	chartInterval := getChartInterval()

	dialer := websocket.Dialer{HandshakeTimeout: cfg.Timeout}
	connPub, _, err := dialer.DialContext(ctx, cfg.WSBase, nil)
	if err != nil {
		return fmt.Errorf("dial okx websocket: %w", err)
	}
	defer connPub.Close()
	prepareOKXConn(connPub, cfg.Timeout)

	pubArgs := make([]map[string]string, 0)
	for _, sym := range getTickerSymbols() {
		inst := okxSwapInstID(sym)
		pubArgs = append(pubArgs,
			map[string]string{"channel": "tickers", "instId": inst},
			map[string]string{"channel": "funding-rate", "instId": inst},
			map[string]string{"channel": "open-interest", "instId": inst},
		)
	}
	if err := connPub.WriteJSON(map[string]interface{}{"op": "subscribe", "args": pubArgs}); err != nil {
		return fmt.Errorf("okx subscribe tickers: %w", err)
	}

	var connBiz *websocket.Conn
	if chartSym != "" {
		bizURL := okxBusinessWSURLFromPublic(cfg.WSBase)
		var errBiz error
		connBiz, _, errBiz = dialer.DialContext(ctx, bizURL, nil)
		if errBiz != nil {
			return fmt.Errorf("dial okx business websocket: %w", errBiz)
		}
		defer connBiz.Close()
		prepareOKXConn(connBiz, cfg.Timeout)
		bizArgs := []map[string]string{
			{"channel": okxCandleChannel(chartInterval), "instId": okxSwapInstID(chartSym)},
		}
		if err := connBiz.WriteJSON(map[string]interface{}{"op": "subscribe", "args": bizArgs}); err != nil {
			return fmt.Errorf("okx subscribe candles: %w", err)
		}
	}

	state.clearError()
	notify()

	pingTicker := time.NewTicker(cfg.Timeout)
	defer pingTicker.Stop()
	resubscribeTicker := time.NewTicker(time.Second)
	defer resubscribeTicker.Stop()
	baselineSymbols := strings.Join(getTickerSymbols(), ",") + "|" + chartSym + "|" + chartInterval

	readErrCh := make(chan error, 2)
	go okxReadTickerConn(connPub, readErrCh, state, notify, cfg.Timeout)
	if connBiz != nil {
		go okxReadFuturesCandleConn(connBiz, readErrCh, cfg, state, notify, getChartSymbol, cfg.Timeout)
	}

	for {
		select {
		case <-ctx.Done():
			_ = connPub.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "shutdown"), time.Now().Add(time.Second))
			if connBiz != nil {
				_ = connBiz.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "shutdown"), time.Now().Add(time.Second))
			}
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
			if err := okxSendPing(connPub, cfg.Timeout); err != nil {
				return fmt.Errorf("ping okx websocket: %w", err)
			}
			if connBiz != nil {
				if err := okxSendPing(connBiz, cfg.Timeout); err != nil {
					return fmt.Errorf("ping okx business websocket: %w", err)
				}
			}
		case <-resubscribeTicker.C:
			curChart := getChartSymbol()
			if isSpotChartSymbol(curChart) {
				curChart = ""
			}
			cur := strings.Join(getTickerSymbols(), ",") + "|" + curChart + "|" + getChartInterval()
			if cur != baselineSymbols {
				state.setError("updating okx market subscriptions...")
				notify()
				return errResubscribe
			}
		}
	}
}

type okxWSFundingPushRow struct {
	InstID          string `json:"instId"`
	FundingRate     string `json:"fundingRate"`
	NextFundingTime string `json:"nextFundingTime"`
	MarkPx          string `json:"markPx"`
	IdxPx           string `json:"idxPx"`
}

type okxWSOpenInterestPushRow struct {
	InstID string `json:"instId"`
	Oi     string `json:"oi"`
	Ts     string `json:"ts"`
}

func okxFundingRatesFromWSData(raw json.RawMessage) ([]fundingRate, error) {
	var rows []okxWSFundingPushRow
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		var single okxWSFundingPushRow
		if err := json.Unmarshal(raw, &single); err != nil {
			return nil, err
		}
		if single.InstID == "" {
			return nil, fmt.Errorf("okx funding-rate: empty instId")
		}
		rows = []okxWSFundingPushRow{single}
	}
	out := make([]fundingRate, 0, len(rows))
	for _, r := range rows {
		mark, _ := strconv.ParseFloat(r.MarkPx, 64)
		index, _ := strconv.ParseFloat(r.IdxPx, 64)
		rate, _ := strconv.ParseFloat(r.FundingRate, 64)
		nextMs, _ := strconv.ParseInt(r.NextFundingTime, 10, 64)
		out = append(out, fundingRate{
			Symbol:          okxCompactFromInstID(r.InstID),
			MarkPrice:       mark,
			IndexPrice:      index,
			LastFundingRate: rate,
			NextFundingTime: nextMs,
		})
	}
	return out, nil
}

func okxOpenInterestFromWSData(raw json.RawMessage) ([]openInterestData, error) {
	var rows []okxWSOpenInterestPushRow
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		var single okxWSOpenInterestPushRow
		if err := json.Unmarshal(raw, &single); err != nil {
			return nil, err
		}
		if single.InstID == "" {
			return nil, fmt.Errorf("okx open-interest: empty instId")
		}
		rows = []okxWSOpenInterestPushRow{single}
	}
	out := make([]openInterestData, 0, len(rows))
	for _, r := range rows {
		oi, _ := strconv.ParseFloat(r.Oi, 64)
		ts, _ := strconv.ParseInt(r.Ts, 10, 64)
		out = append(out, openInterestData{
			Symbol:       okxCompactFromInstID(r.InstID),
			OpenInterest: oi,
			Time:         ts,
		})
	}
	return out, nil
}

func okxReadTickerConn(conn *websocket.Conn, readErrCh chan<- error, state *appState, notify func(), timeout time.Duration) {
	for {
		raw, err := okxReadNextJSON(conn, timeout)
		if err != nil {
			readErrCh <- err
			return
		}
		var env okxWSPush
		if err := json.Unmarshal(raw, &env); err != nil {
			readErrCh <- fmt.Errorf("decode okx ws message: %w", err)
			return
		}
		if env.Event != "" {
			if env.Event == "error" || (env.Code != "" && env.Code != "0") {
				readErrCh <- fmt.Errorf("okx ws error event: %s %s", env.Code, env.Msg)
				return
			}
			continue
		}
		if env.Op == "pong" {
			continue
		}
		if len(env.Arg) == 0 {
			continue
		}
		var arg struct {
			Channel string `json:"channel"`
			InstID  string `json:"instId"`
		}
		if err := json.Unmarshal(env.Arg, &arg); err != nil || arg.Channel == "" {
			continue
		}
		switch arg.Channel {
		case "tickers":
			var rows []struct {
				InstID   string `json:"instId"`
				Last     string `json:"last"`
				Open24h  string `json:"open24h"`
				High24h  string `json:"high24h"`
				Low24h   string `json:"low24h"`
				Vol24h   string `json:"vol24h"`
				VolCcy24 string `json:"volCcy24h"`
			}
			if err := json.Unmarshal(env.Data, &rows); err != nil {
				readErrCh <- fmt.Errorf("decode okx tickers: %w", err)
				return
			}
			for _, row := range rows {
				ticker := okxWSTickerRowToPrice(row)
				state.applyTicker(ticker)
			}
			notify()
		case "funding-rate":
			rates, err := okxFundingRatesFromWSData(env.Data)
			if err != nil {
				readErrCh <- fmt.Errorf("decode okx funding-rate: %w", err)
				return
			}
			if len(rates) > 0 {
				state.setFundingRates(rates)
				notify()
			}
		case "open-interest":
			ois, err := okxOpenInterestFromWSData(env.Data)
			if err != nil {
				readErrCh <- fmt.Errorf("decode okx open-interest: %w", err)
				return
			}
			for _, oi := range ois {
				state.setOpenInterest(oi)
			}
			notify()
		default:
			continue
		}
	}
}

func okxReadFuturesCandleConn(conn *websocket.Conn, readErrCh chan<- error, cfg config, state *appState, notify func(), getChartSymbol func() string, timeout time.Duration) {
	for {
		raw, err := okxReadNextJSON(conn, timeout)
		if err != nil {
			readErrCh <- err
			return
		}
		var env okxWSPush
		if err := json.Unmarshal(raw, &env); err != nil {
			readErrCh <- fmt.Errorf("decode okx business ws message: %w", err)
			return
		}
		if env.Event != "" {
			if env.Event == "error" || (env.Code != "" && env.Code != "0") {
				readErrCh <- fmt.Errorf("okx business ws error event: %s %s", env.Code, env.Msg)
				return
			}
			continue
		}
		if env.Op == "pong" {
			continue
		}
		if len(env.Arg) == 0 {
			continue
		}
		var arg struct {
			Channel string `json:"channel"`
			InstID  string `json:"instId"`
		}
		if err := json.Unmarshal(env.Arg, &arg); err != nil || arg.Channel == "" {
			continue
		}
		if !strings.HasPrefix(arg.Channel, "candle") {
			continue
		}
		var rows [][]string
		if err := json.Unmarshal(env.Data, &rows); err != nil {
			readErrCh <- fmt.Errorf("decode okx candles: %w", err)
			return
		}
		if len(rows) == 0 {
			continue
		}
		row := rows[len(rows)-1]
		candle, err := parseOKXWSCandle(arg.InstID, row)
		if err != nil {
			readErrCh <- err
			return
		}
		if candle.Symbol == getChartSymbol() {
			state.applyChartCandle(panelFutures, candle, cfg.ChartLimit)
		}
		notify()
	}
}

func okxWSTickerRowToPrice(row struct {
	InstID   string `json:"instId"`
	Last     string `json:"last"`
	Open24h  string `json:"open24h"`
	High24h  string `json:"high24h"`
	Low24h   string `json:"low24h"`
	Vol24h   string `json:"vol24h"`
	VolCcy24 string `json:"volCcy24h"`
}) priceTicker {
	last, _ := strconv.ParseFloat(row.Last, 64)
	open, _ := strconv.ParseFloat(row.Open24h, 64)
	high, _ := strconv.ParseFloat(row.High24h, 64)
	low, _ := strconv.ParseFloat(row.Low24h, 64)
	vol, _ := strconv.ParseFloat(row.Vol24h, 64)
	if row.VolCcy24 != "" {
		if v, err := strconv.ParseFloat(row.VolCcy24, 64); err == nil {
			vol = v
		}
	}
	var chgPct float64
	if open > 1e-12 {
		chgPct = (last - open) / open * 100
	}
	return priceTicker{
		Symbol:       okxCompactFromInstID(row.InstID),
		Price:        row.Last,
		Time:         time.Now().UnixMilli(),
		ChangePct24h: chgPct,
		Change24h:    last - open,
		High24h:      high,
		Low24h:       low,
		Volume24h:    vol,
	}
}

func parseOKXWSCandle(instID string, row []string) (klineCandle, error) {
	if len(row) < 6 {
		return klineCandle{}, fmt.Errorf("okx candle row too short")
	}
	openMs, err := strconv.ParseInt(row[0], 10, 64)
	if err != nil {
		return klineCandle{}, err
	}
	symbol := okxCompactFromInstID(instID)
	closed := len(row) > 0 && row[len(row)-1] == "1"
	candle, err := newKlineCandle(symbol, openMs, openMs, row[1], row[2], row[3], row[4], row[5], closed)
	if err != nil {
		return klineCandle{}, err
	}
	return candle, nil
}

func runOKXSpotWSLoop(ctx context.Context, cfg config, state *appState, notify func(), getSpotTickerSymbols func() []string, spotWSBase string) error {
	for {
		if ctx.Err() != nil {
			return nil
		}

		state.setSpotError("connecting okx spot websocket...")
		notify()

		err := consumeOKXSpotWS(ctx, cfg, state, notify, getSpotTickerSymbols, getChartSymbolForActivePanel(state), state.getChartInterval, isSpotTickerSymbolFunc(cfg), spotWSBase)
		if err == nil || ctx.Err() != nil {
			return nil
		}
		if errors.Is(err, errResubscribe) {
			continue
		}

		state.setSpotError(fmt.Sprintf("okx spot websocket disconnected: %v | retry in %s", err, cfg.RetryDelay))
		notify()

		select {
		case <-ctx.Done():
			return nil
		case <-time.After(cfg.RetryDelay):
		}
	}
}

func consumeOKXSpotWS(ctx context.Context, cfg config, state *appState, notify func(), getSpotTickerSymbols func() []string, getChartSymbol func() string, getChartInterval func() string, isSpotChartSymbol func(string) bool, wsBase string) error {
	chartSym := getChartSymbol()
	if !isSpotChartSymbol(chartSym) {
		chartSym = ""
	}
	chartInterval := getChartInterval()

	dialer := websocket.Dialer{HandshakeTimeout: cfg.Timeout}
	connPub, _, err := dialer.DialContext(ctx, wsBase, nil)
	if err != nil {
		return fmt.Errorf("dial okx spot websocket: %w", err)
	}
	defer connPub.Close()
	prepareOKXConn(connPub, cfg.Timeout)

	pubArgs := make([]map[string]string, 0)
	for _, sym := range getSpotTickerSymbols() {
		pubArgs = append(pubArgs, map[string]string{"channel": "tickers", "instId": okxSpotInstID(sym)})
	}
	if err := connPub.WriteJSON(map[string]interface{}{"op": "subscribe", "args": pubArgs}); err != nil {
		return fmt.Errorf("okx spot subscribe tickers: %w", err)
	}

	var connBiz *websocket.Conn
	if chartSym != "" {
		bizURL := okxBusinessWSURLFromPublic(wsBase)
		var errBiz error
		connBiz, _, errBiz = dialer.DialContext(ctx, bizURL, nil)
		if errBiz != nil {
			return fmt.Errorf("dial okx spot business websocket: %w", errBiz)
		}
		defer connBiz.Close()
		prepareOKXConn(connBiz, cfg.Timeout)
		bizArgs := []map[string]string{
			{"channel": okxCandleChannel(chartInterval), "instId": okxSpotInstID(chartSym)},
		}
		if err := connBiz.WriteJSON(map[string]interface{}{"op": "subscribe", "args": bizArgs}); err != nil {
			return fmt.Errorf("okx spot subscribe candles: %w", err)
		}
	}

	state.clearSpotError()
	notify()

	pingTicker := time.NewTicker(cfg.Timeout)
	defer pingTicker.Stop()
	resubscribeTicker := time.NewTicker(time.Second)
	defer resubscribeTicker.Stop()
	baselineSymbols := strings.Join(getSpotTickerSymbols(), ",") + "|" + chartSym + "|" + chartInterval

	readErrCh := make(chan error, 2)
	go okxReadSpotTickerConn(connPub, readErrCh, state, notify, cfg.Timeout)
	if connBiz != nil {
		go okxReadSpotCandleConn(connBiz, readErrCh, cfg, state, notify, getChartSymbol, cfg.Timeout)
	}

	for {
		select {
		case <-ctx.Done():
			_ = connPub.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "shutdown"), time.Now().Add(time.Second))
			if connBiz != nil {
				_ = connBiz.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, "shutdown"), time.Now().Add(time.Second))
			}
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
			if err := okxSendPing(connPub, cfg.Timeout); err != nil {
				return fmt.Errorf("ping okx spot ws: %w", err)
			}
			if connBiz != nil {
				if err := okxSendPing(connBiz, cfg.Timeout); err != nil {
					return fmt.Errorf("ping okx spot business ws: %w", err)
				}
			}
		case <-resubscribeTicker.C:
			curChart := getChartSymbol()
			if !isSpotChartSymbol(curChart) {
				curChart = ""
			}
			cur := strings.Join(getSpotTickerSymbols(), ",") + "|" + curChart + "|" + getChartInterval()
			if cur != baselineSymbols {
				state.setSpotError("updating okx spot subscriptions...")
				notify()
				return errResubscribe
			}
		}
	}
}

func okxReadSpotTickerConn(conn *websocket.Conn, readErrCh chan<- error, state *appState, notify func(), timeout time.Duration) {
	for {
		raw, err := okxReadNextJSON(conn, timeout)
		if err != nil {
			readErrCh <- err
			return
		}
		var env okxWSPush
		if err := json.Unmarshal(raw, &env); err != nil {
			readErrCh <- fmt.Errorf("decode okx spot ws: %w", err)
			return
		}
		if env.Event != "" {
			if env.Event == "error" || (env.Code != "" && env.Code != "0") {
				readErrCh <- fmt.Errorf("okx spot ws error: %s %s", env.Code, env.Msg)
				return
			}
			continue
		}
		if env.Op == "pong" || len(env.Arg) == 0 {
			continue
		}
		var arg struct {
			Channel string `json:"channel"`
			InstID  string `json:"instId"`
		}
		if err := json.Unmarshal(env.Arg, &arg); err != nil || arg.Channel == "" {
			continue
		}
		if arg.Channel != "tickers" {
			continue
		}
		var rows []struct {
			InstID   string `json:"instId"`
			Last     string `json:"last"`
			Open24h  string `json:"open24h"`
			High24h  string `json:"high24h"`
			Low24h   string `json:"low24h"`
			Vol24h   string `json:"vol24h"`
			VolCcy24 string `json:"volCcy24h"`
		}
		if err := json.Unmarshal(env.Data, &rows); err != nil {
			readErrCh <- fmt.Errorf("decode okx spot tickers: %w", err)
			return
		}
		for _, row := range rows {
			state.applySpotTicker(okxWSTickerRowToPrice(row))
		}
		notify()
	}
}

func okxReadSpotCandleConn(conn *websocket.Conn, readErrCh chan<- error, cfg config, state *appState, notify func(), getChartSymbol func() string, timeout time.Duration) {
	for {
		raw, err := okxReadNextJSON(conn, timeout)
		if err != nil {
			readErrCh <- err
			return
		}
		var env okxWSPush
		if err := json.Unmarshal(raw, &env); err != nil {
			readErrCh <- fmt.Errorf("decode okx spot business ws: %w", err)
			return
		}
		if env.Event != "" {
			if env.Event == "error" || (env.Code != "" && env.Code != "0") {
				readErrCh <- fmt.Errorf("okx spot business ws error: %s %s", env.Code, env.Msg)
				return
			}
			continue
		}
		if env.Op == "pong" || len(env.Arg) == 0 {
			continue
		}
		var arg struct {
			Channel string `json:"channel"`
			InstID  string `json:"instId"`
		}
		if err := json.Unmarshal(env.Arg, &arg); err != nil || arg.Channel == "" {
			continue
		}
		if !strings.HasPrefix(arg.Channel, "candle") {
			continue
		}
		var rows [][]string
		if err := json.Unmarshal(env.Data, &rows); err != nil {
			readErrCh <- fmt.Errorf("decode okx spot candles: %w", err)
			return
		}
		if len(rows) == 0 {
			continue
		}
		candle, err := parseOKXWSCandle(arg.InstID, rows[len(rows)-1])
		if err != nil {
			readErrCh <- err
			return
		}
		if candle.Symbol == getChartSymbol() {
			state.applyChartCandle(panelSpot, candle, cfg.ChartLimit)
		}
		notify()
	}
}
