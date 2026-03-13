package main

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

var (
	netErrClosed   = errors.New("use of closed network connection")
	errResubscribe = errors.New("market stream resubscribe requested")
)

func runWSLoop(ctx context.Context, cfg config, state *appState, notify func(), getChartSymbol func() string, getChartInterval func() string, getTickerSymbols func() []string, isSpotChartSymbol func(string) bool) error {
	for {
		if ctx.Err() != nil {
			return nil
		}

		state.setError("connecting websocket...")
		notify()

		err := consumeWS(ctx, cfg, state, notify, getChartSymbol, getChartInterval, getTickerSymbols, isSpotChartSymbol)
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

func consumeWS(ctx context.Context, cfg config, state *appState, notify func(), getChartSymbol func() string, getChartInterval func() string, getTickerSymbols func() []string, isSpotChartSymbol func(string) bool) error {
	chartSymbol := getChartSymbol()
	if isSpotChartSymbol(chartSymbol) {
		chartSymbol = ""
	}
	chartInterval := getChartInterval()
	endpoint := buildWSURL(cfg.WSBase, getTickerSymbols(), chartSymbol, chartInterval)
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
	klineStreamSuffix := "@kline_" + chartInterval
	baselineSymbols := strings.Join(getTickerSymbols(), ",") + "|" + chartSymbol + "|" + chartInterval

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
			case strings.HasSuffix(envelope.Stream, klineStreamSuffix):
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
			currentSymbols := strings.Join(getTickerSymbols(), ",") + "|" + currentChartSymbol + "|" + getChartInterval()
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

		err := consumeSpotWS(ctx, cfg, state, notify, getSpotTickerSymbols, getChartSymbolForActivePanel(state), state.getChartInterval, isSpotTickerSymbolFunc(cfg))
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

func consumeSpotWS(ctx context.Context, cfg config, state *appState, notify func(), getSpotTickerSymbols func() []string, getChartSymbol func() string, getChartInterval func() string, isSpotChartSymbol func(string) bool) error {
	chartSymbol := getChartSymbol()
	if !isSpotChartSymbol(chartSymbol) {
		chartSymbol = ""
	}
	chartInterval := getChartInterval()
	endpoint := buildWSURL(defaultSpotWSBaseURL, getSpotTickerSymbols(), chartSymbol, chartInterval)
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
	klineStreamSuffix := "@kline_" + chartInterval
	baselineSymbols := strings.Join(getSpotTickerSymbols(), ",") + "|" + chartSymbol + "|" + chartInterval

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
			case strings.HasSuffix(envelope.Stream, klineStreamSuffix):
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
			currentSymbols := strings.Join(getSpotTickerSymbols(), ",") + "|" + currentChartSymbol + "|" + getChartInterval()
			if currentSymbols != baselineSymbols {
				state.setSpotError("updating spot subscriptions...")
				notify()
				return errResubscribe
			}
		}
	}
}

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

	parseFloat := func(key string) float64 {
		raw, ok := payload[key]
		if !ok {
			return 0
		}
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return 0
		}
		v, _ := strconv.ParseFloat(s, 64)
		return v
	}

	return priceTicker{
		Symbol:       symbol,
		Price:        price,
		Time:         int64(eventTime),
		Change24h:    parseFloat("p"),
		ChangePct24h: parseFloat("P"),
		High24h:      parseFloat("h"),
		Low24h:       parseFloat("l"),
		Volume24h:    parseFloat("v"),
	}, nil
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
		string(payload.Kline.Volume),
		payload.Kline.IsClosed,
	)
}

func buildWSURL(baseURL string, symbols []string, chartSymbol, chartInterval string) string {
	symbols = normalizeSymbolList(symbols)
	streams := make([]string, 0, len(symbols)+1)
	for _, symbol := range symbols {
		streams = append(streams, strings.ToLower(symbol)+"@ticker")
	}
	if chartSymbol != "" {
		if chartInterval == "" {
			chartInterval = defaultChartInterval
		}
		streams = append(streams, strings.ToLower(chartSymbol)+"@kline_"+chartInterval)
	}
	return baseURL + "/stream?streams=" + strings.Join(streams, "/")
}
