package main

// Gate.io REST API helpers.
//
// Gate.io Futures (USD-M perpetual): https://fx-api.gateio.ws
//   - GET /api/v4/futures/usdt/candlesticks   (klines)
//   - GET /api/v4/futures/usdt/order_book      (depth)
//   - GET /api/v4/futures/usdt/contracts/{contract} (contract info)
//
// Gate.io Spot: https://api.gateio.ws
//   - GET /api/v4/spot/candlesticks
//   - GET /api/v4/spot/order_book

import (
	"context"
	"crypto/hmac"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// gateIntervalMap converts Binance-style intervals to Gate.io intervals.
// Gate.io supports: 10s, 1m, 5m, 15m, 30m, 1h, 4h, 8h, 1d, 7d, 30d
var gateIntervalMap = map[string]string{
	"1m":  "1m",
	"5m":  "5m",
	"15m": "15m",
	"30m": "30m",
	"1h":  "1h",
	"2h":  "2h",
	"4h":  "4h",
	"1d":  "1d",
	"3d":  "1d", // no 3d on gate, fall back to 1d
}

func gateInterval(interval string) string {
	if v, ok := gateIntervalMap[interval]; ok {
		return v
	}
	return "1h"
}

// fetchKlinesGate fetches candlestick data from Gate.io.
// For futures, symbol looks like "BTC_USDT"; for spot it's also "BTC_USDT".
func fetchKlinesGate(ctx context.Context, client *http.Client, baseURL, symbol, interval string, limit int, panel panelMode) ([]klineCandle, error) {
	endpoint, err := buildKlineURLGate(baseURL, symbol, interval, limit, panel)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build gate kline request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gate kline request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read gate kline response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gate kline status %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	if panel == panelFutures {
		return parseGateFuturesKlines(symbol, body)
	}
	return parseGateSpotKlines(symbol, body)
}

func buildKlineURLGate(baseURL, symbol, interval string, limit int, panel panelMode) (string, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("parse gate base url: %w", err)
	}

	iv := gateInterval(interval)
	q := url.Values{}
	q.Set("interval", iv)
	q.Set("limit", strconv.Itoa(limit))

	if panel == panelFutures {
		parsed.Path = "/api/v4/futures/usdt/candlesticks"
		q.Set("contract", symbol)
	} else {
		parsed.Path = "/api/v4/spot/candlesticks"
		q.Set("currency_pair", symbol)
	}
	parsed.RawQuery = q.Encode()
	return parsed.String(), nil
}

// Gate.io futures kline response: array of objects
// { "t": unix_sec, "o": "str", "h": "str", "l": "str", "c": "str", "v": integer (contracts), ...}
func parseGateFuturesKlines(symbol string, body []byte) ([]klineCandle, error) {
	var raw []struct {
		T int64  `json:"t"` // unix seconds
		O string `json:"o"`
		H string `json:"h"`
		L string `json:"l"`
		C string `json:"c"`
		V int64  `json:"v"` // contract volume — integer, not string
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("decode gate futures kline: %w", err)
	}

	klines := make([]klineCandle, 0, len(raw))
	for _, item := range raw {
		openTime := item.T * 1000
		vol := strconv.FormatInt(item.V, 10)
		candle, err := newKlineCandle(symbol, openTime, openTime, item.O, item.H, item.L, item.C, vol, true)
		if err != nil {
			return nil, err
		}
		klines = append(klines, candle)
	}
	return klines, nil
}

// Gate.io spot kline response: array of arrays
// [unix_timestamp_sec, open, close, high, low, amount(quote), volume(base)]
// Note: Gate spot returns [t, o, c, h, l, quote_vol, base_vol] (different field order!)
func parseGateSpotKlines(symbol string, body []byte) ([]klineCandle, error) {
	var raw [][]interface{}
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("decode gate spot kline: %w", err)
	}

	klines := make([]klineCandle, 0, len(raw))
	for _, item := range raw {
		if len(item) < 7 {
			continue
		}
		// Gate spot: [timestamp, volume(quote), close, high, low, amount(base), open]
		// index:       0          1               2      3      4    5             6
		openTime, err := interfaceToInt64(item[0])
		if err != nil {
			return nil, err
		}
		openTime *= 1000 // to ms
		openStr, err := interfaceToString(item[6])
		if err != nil {
			return nil, err
		}
		highStr, err := interfaceToString(item[3])
		if err != nil {
			return nil, err
		}
		lowStr, err := interfaceToString(item[4])
		if err != nil {
			return nil, err
		}
		closeStr, err := interfaceToString(item[2])
		if err != nil {
			return nil, err
		}
		volumeStr, err := interfaceToString(item[5])
		if err != nil {
			return nil, err
		}
		candle, err := newKlineCandle(symbol, openTime, openTime, openStr, highStr, lowStr, closeStr, volumeStr, true)
		if err != nil {
			return nil, err
		}
		klines = append(klines, candle)
	}
	return klines, nil
}

// fetchOrderBookGate fetches the order book from Gate.io.
func fetchOrderBookGate(ctx context.Context, baseURL, symbol string, panel panelMode) (orderBookResponse, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return orderBookResponse{}, fmt.Errorf("parse gate base url: %w", err)
	}

	q := url.Values{}
	q.Set("limit", strconv.Itoa(orderBookLimit))

	if panel == panelFutures {
		parsed.Path = "/api/v4/futures/usdt/order_book"
		q.Set("contract", symbol)
	} else {
		parsed.Path = "/api/v4/spot/order_book"
		q.Set("currency_pair", symbol)
	}
	parsed.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return orderBookResponse{}, fmt.Errorf("build gate order book request: %w", err)
	}

	client := &http.Client{Timeout: defaultTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return orderBookResponse{}, fmt.Errorf("gate order book request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return orderBookResponse{}, fmt.Errorf("read gate order book response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return orderBookResponse{}, fmt.Errorf("gate order book status %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	// Gate.io order book format for both futures and spot:
	// { "id": ..., "asks": [{"p": "price", "s": size}, ...], "bids": [...] }
	// For spot: { "asks": [["price", "size"], ...], "bids": [...] }
	if panel == panelFutures {
		return parseGateFuturesOrderBook(body)
	}
	// Gate spot order book uses [[price, size], ...] format (same as Binance)
	var ob orderBookResponse
	if err := json.Unmarshal(body, &ob); err != nil {
		return orderBookResponse{}, fmt.Errorf("decode gate spot order book: %w", err)
	}
	return ob, nil
}

func parseGateFuturesOrderBook(body []byte) (orderBookResponse, error) {
	var raw struct {
		ID   int64 `json:"id"`
		Asks []struct {
			P string  `json:"p"`
			S float64 `json:"s"`
		} `json:"asks"`
		Bids []struct {
			P string  `json:"p"`
			S float64 `json:"s"`
		} `json:"bids"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return orderBookResponse{}, fmt.Errorf("decode gate futures order book: %w", err)
	}

	asks := make([][]string, len(raw.Asks))
	for i, a := range raw.Asks {
		asks[i] = []string{a.P, strconv.FormatFloat(a.S, 'f', -1, 64)}
	}
	bids := make([][]string, len(raw.Bids))
	for i, b := range raw.Bids {
		bids[i] = []string{b.P, strconv.FormatFloat(b.S, 'f', -1, 64)}
	}
	return orderBookResponse{LastUpdateID: raw.ID, Asks: asks, Bids: bids}, nil
}

// ── Gate.io Funding Rate ──────────────────────────────────────────────────────
//
// GET /api/v4/futures/usdt/contracts/{contract}
// Returns contract info including mark_price, index_price, funding_rate,
// funding_next_apply (unix seconds of next settlement).

func runGateFundingRateLoop(ctx context.Context, client *http.Client, cfg config, state *appState, notify func()) {
	fetch := func() {
		rates, err := fetchGateFundingRates(ctx, client, cfg.RESTBase, cfg.Symbols)
		if err != nil {
			return
		}
		state.setFundingRates(rates)
		notify()
	}
	fetch()
	ticker := time.NewTicker(fundingRateRefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			fetch()
		}
	}
}

func fetchGateFundingRates(ctx context.Context, client *http.Client, baseURL string, symbols []string) ([]fundingRate, error) {
	rates := make([]fundingRate, 0, len(symbols))
	for _, symbol := range symbols {
		parsed, err := url.Parse(baseURL)
		if err != nil {
			continue
		}
		parsed.Path = "/api/v4/futures/usdt/contracts/" + symbol

		req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
		if err != nil {
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			continue
		}

		var payload struct {
			Name            string `json:"name"`
			MarkPrice       string `json:"mark_price"`
			IndexPrice      string `json:"index_price"`
			FundingRate     string `json:"funding_rate"`
			FundingNextApply float64 `json:"funding_next_apply"` // unix seconds
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			continue
		}
		mark, _ := strconv.ParseFloat(payload.MarkPrice, 64)
		index, _ := strconv.ParseFloat(payload.IndexPrice, 64)
		rate, _ := strconv.ParseFloat(payload.FundingRate, 64)
		rates = append(rates, fundingRate{
			Symbol:          payload.Name,
			MarkPrice:       mark,
			IndexPrice:      index,
			LastFundingRate: rate,
			NextFundingTime: int64(payload.FundingNextApply) * 1000, // to ms
		})
	}
	return rates, nil
}

// ── Gate.io Market Stats (OI + L/S Ratio) ────────────────────────────────────
//
// GET /api/v4/futures/usdt/stats?contract=BTC_USDT&interval=5m&limit=2
// Returns an array of stat snapshots:
// [{"time":..., "open_interest":"...", "open_interest_usd":..., "lsr_account":..., "lsr_taker":...}]

func runGateMarketStatsLoop(ctx context.Context, client *http.Client, cfg config, state *appState, notify func()) {
	fetch := func() {
		for _, symbol := range cfg.Symbols {
			oi, ls, err := fetchGateMarketStats(ctx, client, cfg.RESTBase, symbol)
			if err != nil {
				continue
			}
			state.setOpenInterest(oi)
			state.setLongShortRatio(ls)
		}
		notify()
	}
	fetch()
	ticker := time.NewTicker(marketStatsRefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			fetch()
		}
	}
}

func fetchGateMarketStats(ctx context.Context, client *http.Client, baseURL, symbol string) (openInterestData, longShortRatioData, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return openInterestData{}, longShortRatioData{}, err
	}
	// /api/v4/futures/usdt/contract_stats is a public endpoint (no auth needed).
	parsed.Path = "/api/v4/futures/usdt/contract_stats"
	q := url.Values{}
	q.Set("contract", symbol)
	q.Set("interval", "5m")
	q.Set("limit", "2") // 2 points so we can compute OI change
	parsed.RawQuery = q.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return openInterestData{}, longShortRatioData{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return openInterestData{}, longShortRatioData{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return openInterestData{}, longShortRatioData{}, fmt.Errorf("gate contract_stats %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var payload []struct {
		Time         int64   `json:"time"`
		OpenInterest int64   `json:"open_interest"` // number of contracts (integer)
		LSRAccount   float64 `json:"lsr_account"`   // long/short account ratio
		LSRTaker     float64 `json:"lsr_taker"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return openInterestData{}, longShortRatioData{}, fmt.Errorf("decode gate contract_stats: %w", err)
	}
	if len(payload) == 0 {
		return openInterestData{}, longShortRatioData{}, fmt.Errorf("empty gate contract_stats response")
	}

	latest := payload[len(payload)-1]
	oi := float64(latest.OpenInterest)

	var prevOI float64
	if len(payload) >= 2 {
		prevOI = float64(payload[len(payload)-2].OpenInterest)
	}

	oiData := openInterestData{
		Symbol:           symbol,
		OpenInterest:     oi,
		PrevOpenInterest: prevOI,
		Time:             latest.Time * 1000, // to ms
	}

	// lsr_account = long accounts / short accounts
	// long% = lsr / (1 + lsr), short% = 1 / (1 + lsr)
	lsr := latest.LSRAccount
	var longPct, shortPct float64
	if lsr+1 > 0 {
		longPct = lsr / (lsr + 1)
		shortPct = 1 / (lsr + 1)
	}
	lsData := longShortRatioData{
		Symbol:       symbol,
		Ratio:        lsr,
		LongAccount:  longPct,
		ShortAccount: shortPct,
		Timestamp:    latest.Time * 1000,
	}

	return oiData, lsData, nil
}

// ── Gate.io Positions polling loop ───────────────────────────────────────────

func runGatePositionsLoop(ctx context.Context, client *http.Client, cfg config, state *appState, notify func()) {
	const interval = 5 * time.Second
	fetch := func() {
		positions, err := fetchGatePositions(ctx, client, cfg)
		if err != nil {
			state.setAccountError(fmt.Sprintf("gate positions: %v", err))
			notify()
			return
		}
		state.setPositions(positions)
		state.clearAccountError()
		notify()
	}
	fetch()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			fetch()
		}
	}
}

// ── Gate.io Positions (signed) ────────────────────────────────────────────────
//
// Gate.io uses HMAC-SHA512 with a different signing scheme than Binance.
// Signature = HMAC-SHA512(key=APISecret, message=method+"\n"+path+"\n"+query+"\n"+bodyHash+"\n"+timestamp)
// Header: KEY, SIGN, Timestamp

// buildGateSignature computes the Gate.io API v4 request signature.
// Signature = HexEncode(HMAC-SHA512(secret, method+"\n"+path+"\n"+query+"\n"+sha512(body)+"\n"+timestamp))
func buildGateSignature(secret, method, path, query, body string, ts int64) string {
	bodyHashBytes := sha512.Sum512([]byte(body))
	bodyHash := hex.EncodeToString(bodyHashBytes[:])
	message := strings.Join([]string{method, path, query, bodyHash, strconv.FormatInt(ts, 10)}, "\n")
	mac := hmac.New(sha512.New, []byte(secret))
	mac.Write([]byte(message))
	return hex.EncodeToString(mac.Sum(nil))
}

func fetchGatePositions(ctx context.Context, client *http.Client, cfg config) ([]positionState, error) {
	const path = "/api/v4/futures/usdt/positions"
	ts := time.Now().Unix()
	sig := buildGateSignature(cfg.APISecret, "GET", path, "", "", ts)

	parsed, err := url.Parse(cfg.RESTBase)
	if err != nil {
		return nil, fmt.Errorf("parse gate rest base: %w", err)
	}
	parsed.Path = path

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build gate positions request: %w", err)
	}
	req.Header.Set("KEY", cfg.APIKey)
	req.Header.Set("SIGN", sig)
	req.Header.Set("Timestamp", strconv.FormatInt(ts, 10))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gate positions request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read gate positions response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gate positions status %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var payload []struct {
		Contract           string `json:"contract"`
		Size               int64  `json:"size"` // positive = long, negative = short
		EntryPrice         string `json:"entry_price"`
		MarkPrice          string `json:"mark_price"`
		UnrealisedPnl      string `json:"unrealised_pnl"`
		LiqPrice           string `json:"liq_price"`
		Leverage           string `json:"leverage"`
		CrossLeverageLimit string `json:"cross_leverage_limit"`
		Mode               string `json:"mode"` // "single" or "dual_long"/"dual_short"
		UpdateTime         int64  `json:"update_time"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode gate positions: %w", err)
	}

	positions := make([]positionState, 0)
	for _, item := range payload {
		if item.Size == 0 {
			continue
		}
		size := math.Abs(float64(item.Size))
		entryPrice, _ := strconv.ParseFloat(item.EntryPrice, 64)
		markPrice, _ := strconv.ParseFloat(item.MarkPrice, 64)
		unrealPnl, _ := strconv.ParseFloat(item.UnrealisedPnl, 64)
		liqPrice, _ := strconv.ParseFloat(item.LiqPrice, 64)

		side := "LONG"
		if item.Size < 0 {
			side = "SHORT"
		}

		marginType := "CROSS"
		lev := item.Leverage
		if item.Leverage != "0" && item.Leverage != "" {
			marginType = "ISOLATED"
		} else {
			lev = item.CrossLeverageLimit
		}

		positions = append(positions, positionState{
			Symbol:           item.Contract,
			Side:             side,
			Size:             size,
			EntryPrice:       entryPrice,
			MarkPrice:        markPrice,
			UnrealizedPnL:    unrealPnl,
			LiquidationPrice: liqPrice,
			MarginType:       marginType,
			Leverage:         lev,
			UpdateTime:       item.UpdateTime * 1000,
		})
	}

	sort.Slice(positions, func(i, j int) bool {
		if positions[i].Symbol == positions[j].Symbol {
			return positions[i].Side < positions[j].Side
		}
		return positions[i].Symbol < positions[j].Symbol
	})
	return positions, nil
}
