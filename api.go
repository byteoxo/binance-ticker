package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
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

func runFundingRateLoop(ctx context.Context, client *http.Client, cfg config, state *appState, notify func()) {
	fetch := func() {
		rates, err := fetchFundingRates(ctx, client, cfg.RESTBase, cfg.Symbols)
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

func fetchFundingRates(ctx context.Context, client *http.Client, baseURL string, symbols []string) ([]fundingRate, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return nil, err
	}
	parsed.Path = "/fapi/v1/premiumIndex"

	rates := make([]fundingRate, 0, len(symbols))
	for _, symbol := range symbols {
		q := url.Values{}
		q.Set("symbol", symbol)
		parsed.RawQuery = q.Encode()

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
			Symbol          string `json:"symbol"`
			MarkPrice       string `json:"markPrice"`
			IndexPrice      string `json:"indexPrice"`
			LastFundingRate string `json:"lastFundingRate"`
			NextFundingTime int64  `json:"nextFundingTime"`
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			continue
		}
		mark, _ := strconv.ParseFloat(payload.MarkPrice, 64)
		index, _ := strconv.ParseFloat(payload.IndexPrice, 64)
		rate, _ := strconv.ParseFloat(payload.LastFundingRate, 64)
		rates = append(rates, fundingRate{
			Symbol:          payload.Symbol,
			MarkPrice:       mark,
			IndexPrice:      index,
			LastFundingRate: rate,
			NextFundingTime: payload.NextFundingTime,
		})
	}
	return rates, nil
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

func loadChartHistoryForSymbol(ctx context.Context, client *http.Client, baseURL string, panel panelMode, symbol string, limit int, state *appState) error {
	interval := state.getChartInterval()
	klines, err := fetchKlines(ctx, client, baseURL, symbol, interval, limit)
	if err != nil {
		return err
	}
	state.setChart(panel, symbol, klines)
	return nil
}

func fetchKlines(ctx context.Context, client *http.Client, baseURL, symbol, interval string, limit int) ([]klineCandle, error) {
	endpoint, err := buildKlineURL(baseURL, symbol, interval, limit)
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

func buildKlineURL(baseURL, symbol, interval string, limit int) (string, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("parse base url: %w", err)
	}
	parsed.Path = futuresKlinePath
	if strings.EqualFold(parsed.Hostname(), "api.binance.com") {
		parsed.Path = spotKlinePath
	}

	if interval == "" {
		interval = defaultChartInterval
	}
	query := url.Values{}
	query.Set("symbol", symbol)
	query.Set("interval", interval)
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
	volume, err := interfaceToString(item[5])
	if err != nil {
		return klineCandle{}, err
	}
	closeTime, err := interfaceToInt64(item[6])
	if err != nil {
		return klineCandle{}, err
	}

	return newKlineCandle(symbol, openTime, closeTime, open, high, low, closePrice, volume, true)
}

func newKlineCandle(symbol string, openTime, closeTime int64, open, high, low, closePrice, volume string, closed bool) (klineCandle, error) {
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
	volumeValue, _ := strconv.ParseFloat(volume, 64)

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
		Volume:     volumeValue,
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
