package main

// OKX REST API helpers (API v5).
//
// Production: https://www.okx.com
// Signing: Base64(HMAC-SHA256(secret, timestamp + method + requestPath + body))

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
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

const okxAPIPrefix = "/api/v5"

var okxBarMap = map[string]string{
	"1m":  "1m",
	"5m":  "5m",
	"15m": "15m",
	"30m": "30m",
	"1h":  "1H",
	"2h":  "2H",
	"4h":  "4H",
	"1d":  "1D",
	"3d":  "3D",
}

func okxBar(interval string) string {
	if interval == "" {
		return "1H"
	}
	if v, ok := okxBarMap[strings.ToLower(interval)]; ok {
		return v
	}
	return "1H"
}

func isOKXBaseURL(baseURL string) bool {
	h := strings.ToLower(baseURL)
	return strings.Contains(h, "okx.com")
}

func okxRESTHost(baseURL string) string {
	parsed, err := url.Parse(baseURL)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return "https://www.okx.com"
	}
	parsed.Path = ""
	parsed.RawQuery = ""
	return strings.TrimRight(parsed.String(), "/")
}

func okxTimestampRFC3339MS() string {
	return time.Now().UTC().Format("2006-01-02T15:04:05.000Z")
}

func buildOKXSign(secret, timestamp, method, requestPath, body string) string {
	prehash := timestamp + strings.ToUpper(method) + requestPath + body
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(prehash))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

func decodeOKXJSON(body []byte, into interface{}) error {
	var env struct {
		Code string          `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return fmt.Errorf("decode okx envelope: %w", err)
	}
	if env.Code != "0" {
		return fmt.Errorf("okx api error %s: %s", env.Code, strings.TrimSpace(env.Msg))
	}
	if into == nil || len(env.Data) == 0 || string(env.Data) == "null" {
		return nil
	}
	if err := json.Unmarshal(env.Data, into); err != nil {
		return fmt.Errorf("decode okx data: %w", err)
	}
	return nil
}

func okxPrivateRequest(ctx context.Context, client *http.Client, cfg config, method, pathWithQuery, body string) ([]byte, error) {
	ts := okxTimestampRFC3339MS()
	sign := buildOKXSign(cfg.APISecret, ts, method, pathWithQuery, body)
	host := okxRESTHost(cfg.RESTBase)
	reqURL := host + pathWithQuery

	var bodyReader io.Reader
	if body != "" {
		bodyReader = strings.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, reqURL, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("build okx request: %w", err)
	}
	req.Header.Set("OK-ACCESS-KEY", cfg.APIKey)
	req.Header.Set("OK-ACCESS-SIGN", sign)
	req.Header.Set("OK-ACCESS-TIMESTAMP", ts)
	req.Header.Set("OK-ACCESS-PASSPHRASE", cfg.APIPassphrase)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("okx request: %w", err)
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read okx response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("okx status %s: %s", resp.Status, strings.TrimSpace(string(respBody)))
	}
	return respBody, nil
}

func fetchKlinesOKX(ctx context.Context, client *http.Client, baseURL, compactSymbol, interval string, limit int, panel panelMode) ([]klineCandle, error) {
	var instID string
	if panel == panelFutures {
		instID = okxSwapInstID(compactSymbol)
	} else {
		instID = okxSpotInstID(compactSymbol)
	}
	if instID == "" {
		return nil, fmt.Errorf("okx: empty instrument for %s", compactSymbol)
	}
	bar := okxBar(interval)
	if limit <= 0 {
		limit = 100
	}
	if limit > 300 {
		limit = 300
	}

	path := okxAPIPrefix + "/market/candles?" + url.Values{
		"instId": {instID},
		"bar":    {bar},
		"limit":  {strconv.Itoa(limit)},
	}.Encode()

	reqURL := okxRESTHost(baseURL) + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build okx candles request: %w", err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("okx candles request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read okx candles: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("okx candles status %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var raw [][]string
	if err := decodeOKXJSON(body, &raw); err != nil {
		return nil, err
	}

	out := make([]klineCandle, 0, len(raw))
	for _, row := range raw {
		if len(row) < 7 {
			continue
		}
		openMs, err := strconv.ParseInt(row[0], 10, 64)
		if err != nil {
			continue
		}
		vol := row[5]
		closeMs := openMs
		candle, err := newKlineCandle(compactSymbol, openMs, closeMs, row[1], row[2], row[3], row[4], vol, true)
		if err != nil {
			return nil, err
		}
		out = append(out, candle)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("okx: empty candle response")
	}
	sort.Slice(out, func(i, j int) bool { return out[i].OpenTime < out[j].OpenTime })
	return out, nil
}

func fetchOrderBookOKX(ctx context.Context, baseURL, compactSymbol string, panel panelMode) (orderBookResponse, error) {
	var instID string
	if panel == panelFutures {
		instID = okxSwapInstID(compactSymbol)
	} else {
		instID = okxSpotInstID(compactSymbol)
	}
	path := okxAPIPrefix + "/market/books?" + url.Values{
		"instId": {instID},
		"sz":     {strconv.Itoa(orderBookLimit)},
	}.Encode()

	reqURL := okxRESTHost(baseURL) + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return orderBookResponse{}, fmt.Errorf("build okx books request: %w", err)
	}
	client := &http.Client{Timeout: defaultTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return orderBookResponse{}, fmt.Errorf("okx books request: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return orderBookResponse{}, fmt.Errorf("read okx books: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return orderBookResponse{}, fmt.Errorf("okx books status %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var payload []struct {
		Asks [][]string `json:"asks"`
		Bids [][]string `json:"bids"`
		Ts   string     `json:"ts"`
	}
	if err := decodeOKXJSON(body, &payload); err != nil {
		return orderBookResponse{}, err
	}
	if len(payload) == 0 {
		return orderBookResponse{}, fmt.Errorf("okx: empty books response")
	}
	p := payload[0]
	ts, _ := strconv.ParseInt(p.Ts, 10, 64)
	return orderBookResponse{
		LastUpdateID: ts,
		Asks:         p.Asks,
		Bids:         p.Bids,
	}, nil
}

func runOKXMarketStatsLoop(ctx context.Context, client *http.Client, cfg config, state *appState, notify func()) {
	fetch := func() {
		for _, sym := range cfg.Symbols {
			instID := okxSwapInstID(sym)
			if ls, err := fetchOKXLongShortRatio(ctx, client, cfg.RESTBase, instID); err == nil {
				state.setLongShortRatio(ls)
			}
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

func fetchOKXLongShortRatio(ctx context.Context, client *http.Client, baseURL, instID string) (longShortRatioData, error) {
	ccy := okxRubikCCYFromInstID(instID)
	if ccy == "" {
		return longShortRatioData{}, fmt.Errorf("okx: cannot derive rubik ccy from %s", instID)
	}
	path := okxAPIPrefix + "/rubik/stat/contracts/long-short-account-ratio?" + url.Values{
		"instId": {instID},
		"period": {"5m"},
		"ccy":    {ccy},
	}.Encode()
	reqURL := okxRESTHost(baseURL) + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return longShortRatioData{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return longShortRatioData{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return longShortRatioData{}, fmt.Errorf("okx l/s ratio %s", resp.Status)
	}

	var raw [][]json.RawMessage
	if err := decodeOKXJSON(body, &raw); err != nil {
		return longShortRatioData{}, err
	}
	if len(raw) == 0 {
		return longShortRatioData{}, fmt.Errorf("okx: empty l/s ratio")
	}
	row := raw[len(raw)-1]
	if len(row) < 2 {
		return longShortRatioData{}, fmt.Errorf("okx: bad l/s row")
	}
	var ts int64
	if err := json.Unmarshal(row[0], &ts); err != nil {
		var tsStr string
		if err := json.Unmarshal(row[0], &tsStr); err != nil {
			return longShortRatioData{}, fmt.Errorf("okx l/s timestamp: %w", err)
		}
		ts, _ = strconv.ParseInt(tsStr, 10, 64)
	}
	var lsrStr string
	if err := json.Unmarshal(row[1], &lsrStr); err != nil {
		return longShortRatioData{}, fmt.Errorf("okx l/s ratio field: %w", err)
	}
	lsr, _ := strconv.ParseFloat(lsrStr, 64)
	var longPct, shortPct float64
	if lsr >= 0 && lsr+1 > 0 {
		longPct = lsr / (lsr + 1)
		shortPct = 1 / (lsr + 1)
	}
	return longShortRatioData{
		Symbol:       okxCompactFromInstID(instID),
		Ratio:        lsr,
		LongAccount:  longPct,
		ShortAccount: shortPct,
		Timestamp:    ts,
	}, nil
}

func runOKXPositionsLoop(ctx context.Context, client *http.Client, cfg config, state *appState, notify func()) {
	const interval = 5 * time.Second
	fetch := func() {
		positions, err := fetchOKXPositions(ctx, client, cfg)
		if err != nil {
			state.setAccountError(fmt.Sprintf("okx positions: %v", err))
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

func fetchOKXPositions(ctx context.Context, client *http.Client, cfg config) ([]positionState, error) {
	pathWithQuery := okxAPIPrefix + "/account/positions?" + url.Values{
		"instType": {"SWAP"},
	}.Encode()
	body, err := okxPrivateRequest(ctx, client, cfg, http.MethodGet, pathWithQuery, "")
	if err != nil {
		return nil, err
	}

	var payload []struct {
		InstID  string `json:"instId"`
		PosSide string `json:"posSide"`
		Pos     string `json:"pos"`
		AvgPx   string `json:"avgPx"`
		MarkPx  string `json:"markPx"`
		Upl     string `json:"upl"`
		Lever   string `json:"lever"`
		MgnMode string `json:"mgnMode"`
		LiqPx   string `json:"liqPx"`
		UTime   string `json:"uTime"`
	}
	if err := decodeOKXJSON(body, &payload); err != nil {
		return nil, err
	}

	out := make([]positionState, 0)
	for _, item := range payload {
		posQty, _ := strconv.ParseFloat(item.Pos, 64)
		if math.Abs(posQty) < 1e-12 {
			continue
		}
		entry, _ := strconv.ParseFloat(item.AvgPx, 64)
		mark, _ := strconv.ParseFloat(item.MarkPx, 64)
		upl, _ := strconv.ParseFloat(item.Upl, 64)
		liq, _ := strconv.ParseFloat(item.LiqPx, 64)

		posSide := strings.ToLower(strings.TrimSpace(item.PosSide))
		var side string
		size := math.Abs(posQty)
		switch posSide {
		case "long":
			side = "LONG"
		case "short":
			side = "SHORT"
		default:
			if posQty > 0 {
				side = "LONG"
			} else {
				side = "SHORT"
			}
		}

		marginType := "CROSS"
		if strings.EqualFold(item.MgnMode, "isolated") {
			marginType = "ISOLATED"
		}

		ut, _ := strconv.ParseInt(item.UTime, 10, 64)

		out = append(out, positionState{
			Symbol:           okxCompactFromInstID(item.InstID),
			Side:             side,
			Size:             size,
			EntryPrice:       entry,
			MarkPrice:        mark,
			UnrealizedPnL:    upl,
			LiquidationPrice: liq,
			MarginType:       marginType,
			Leverage:         strings.TrimSpace(item.Lever),
			UpdateTime:       ut,
			PnLFromAPI:       true,
		})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].Symbol == out[j].Symbol {
			return out[i].Side < out[j].Side
		}
		return out[i].Symbol < out[j].Symbol
	})
	return out, nil
}

func fetchOKXSpotBalances(ctx context.Context, client *http.Client, cfg config) ([]spotBalance, error) {
	pathWithQuery := okxAPIPrefix + "/account/balance"
	body, err := okxPrivateRequest(ctx, client, cfg, http.MethodGet, pathWithQuery, "")
	if err != nil {
		return nil, err
	}

	var payload []struct {
		Details []struct {
			Ccy       string `json:"ccy"`
			AvailBal  string `json:"availBal"`
			FrozenBal string `json:"frozenBal"`
		} `json:"details"`
	}
	if err := decodeOKXJSON(body, &payload); err != nil {
		return nil, err
	}

	allowed := allowedSpotAssets(okxSpotBalanceFilterAssets(cfg.SpotSymbols))
	out := make([]spotBalance, 0)
	for _, bucket := range payload {
		for _, item := range bucket.Details {
			asset := strings.ToUpper(strings.TrimSpace(item.Ccy))
			if _, ok := allowed[asset]; !ok {
				continue
			}
			free, err := strconv.ParseFloat(strings.TrimSpace(item.AvailBal), 64)
			if err != nil {
				continue
			}
			locked, err := strconv.ParseFloat(strings.TrimSpace(item.FrozenBal), 64)
			if err != nil {
				continue
			}
			total := free + locked
			if total <= 0 {
				continue
			}
			ps := spotSymbolToTicker(asset)
			out = append(out, spotBalance{
				Asset:          asset,
				Free:           free,
				Locked:         locked,
				Total:          total,
				QuoteValueText: "-",
				PriceSymbol:    ps,
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Asset < out[j].Asset })
	return out, nil
}

func fetchOKXPendingOrders(ctx context.Context, client *http.Client, cfg config, instType string) ([]openOrder, error) {
	pathWithQuery := okxAPIPrefix + "/trade/orders-pending?" + url.Values{
		"instType": {instType},
	}.Encode()
	respBody, err := okxPrivateRequest(ctx, client, cfg, http.MethodGet, pathWithQuery, "")
	if err != nil {
		return nil, err
	}

	var payload []struct {
		InstID    string `json:"instId"`
		OrdID     string `json:"ordId"`
		Side      string `json:"side"`
		OrdType   string `json:"ordType"`
		State     string `json:"state"`
		Px        string `json:"px"`
		Sz        string `json:"sz"`
		AccFillSz string `json:"accFillSz"`
		FeeCcy    string `json:"feeCcy"`
		CTime     string `json:"cTime"`
	}
	if err := decodeOKXJSON(respBody, &payload); err != nil {
		return nil, err
	}

	out := make([]openOrder, 0, len(payload))
	for _, item := range payload {
		price, _ := strconv.ParseFloat(item.Px, 64)
		sz, _ := strconv.ParseFloat(item.Sz, 64)
		filled, _ := strconv.ParseFloat(item.AccFillSz, 64)
		tif := "GTC"
		if strings.EqualFold(item.OrdType, "market") {
			tif = ""
		}
		t64, _ := strconv.ParseInt(item.CTime, 10, 64)
		oid, _ := strconv.ParseInt(item.OrdID, 10, 64)
		side := strings.ToUpper(item.Side)
		out = append(out, openOrder{
			Symbol:      okxCompactFromInstID(item.InstID),
			OrderID:     oid,
			Side:        side,
			Type:        strings.ToUpper(item.OrdType),
			Price:       price,
			OrigQty:     sz,
			FilledQty:   filled,
			Status:      strings.ToUpper(item.State),
			TimeInForce: tif,
			Time:        t64,
		})
	}
	return out, nil
}

func placeOKXFuturesLimitOrder(ctx context.Context, client *http.Client, cfg config, compactSymbol string, side string, priceStr, qtyStr string) (openOrder, error) {
	instID := okxSwapInstID(compactSymbol)
	okSide := strings.ToLower(strings.TrimSpace(side))
	switch strings.ToUpper(strings.TrimSpace(side)) {
	case "BUY":
		okSide = "buy"
	case "SELL":
		okSide = "sell"
	}
	if okSide != "buy" && okSide != "sell" {
		return openOrder{}, fmt.Errorf("invalid side %s", side)
	}
	bodyMap := map[string]string{
		"instId":  instID,
		"tdMode":  "cross",
		"side":    okSide,
		"ordType": "limit",
		"px":      priceStr,
		"sz":      qtyStr,
	}
	bodyBytes, err := json.Marshal(bodyMap)
	if err != nil {
		return openOrder{}, err
	}
	pathWithQuery := okxAPIPrefix + "/trade/order"
	ts := okxTimestampRFC3339MS()
	sign := buildOKXSign(cfg.APISecret, ts, "POST", pathWithQuery, string(bodyBytes))
	host := okxRESTHost(cfg.RESTBase)
	reqURL := host + pathWithQuery

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return openOrder{}, err
	}
	req.Header.Set("OK-ACCESS-KEY", cfg.APIKey)
	req.Header.Set("OK-ACCESS-SIGN", sign)
	req.Header.Set("OK-ACCESS-TIMESTAMP", ts)
	req.Header.Set("OK-ACCESS-PASSPHRASE", cfg.APIPassphrase)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return openOrder{}, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return openOrder{}, err
	}
	if resp.StatusCode != http.StatusOK {
		return openOrder{}, fmt.Errorf("okx order %s: %s", resp.Status, strings.TrimSpace(string(respBody)))
	}

	var payload []struct {
		OrdID   string `json:"ordId"`
		InstID  string `json:"instId"`
		Side    string `json:"side"`
		OrdType string `json:"ordType"`
		Px      string `json:"px"`
		Sz      string `json:"sz"`
		State   string `json:"state"`
		CTime   string `json:"cTime"`
	}
	if err := decodeOKXJSON(respBody, &payload); err != nil {
		return openOrder{}, err
	}
	if len(payload) == 0 {
		return openOrder{}, fmt.Errorf("okx: empty order response")
	}
	p := payload[0]
	price, _ := strconv.ParseFloat(p.Px, 64)
	sz, _ := strconv.ParseFloat(p.Sz, 64)
	t64, _ := strconv.ParseInt(p.CTime, 10, 64)
	oid, _ := strconv.ParseInt(p.OrdID, 10, 64)
	return openOrder{
		Symbol:      okxCompactFromInstID(p.InstID),
		OrderID:     oid,
		Side:        strings.ToUpper(p.Side),
		Type:        strings.ToUpper(p.OrdType),
		Price:       price,
		OrigQty:     sz,
		Status:      strings.ToUpper(p.State),
		TimeInForce: "GTC",
		Time:        t64,
	}, nil
}

func cancelOKXFuturesOrder(ctx context.Context, client *http.Client, cfg config, compactSymbol string, orderID int64) error {
	instID := okxSwapInstID(compactSymbol)
	bodyMap := map[string]string{
		"instId": instID,
		"ordId":  strconv.FormatInt(orderID, 10),
	}
	bodyBytes, err := json.Marshal(bodyMap)
	if err != nil {
		return err
	}
	pathWithQuery := okxAPIPrefix + "/trade/cancel-order"
	ts := okxTimestampRFC3339MS()
	sign := buildOKXSign(cfg.APISecret, ts, "POST", pathWithQuery, string(bodyBytes))
	host := okxRESTHost(cfg.RESTBase)
	reqURL := host + pathWithQuery

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return err
	}
	req.Header.Set("OK-ACCESS-KEY", cfg.APIKey)
	req.Header.Set("OK-ACCESS-SIGN", sign)
	req.Header.Set("OK-ACCESS-TIMESTAMP", ts)
	req.Header.Set("OK-ACCESS-PASSPHRASE", cfg.APIPassphrase)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("okx cancel %s: %s", resp.Status, strings.TrimSpace(string(respBody)))
	}
	var dummy []json.RawMessage
	return decodeOKXJSON(respBody, &dummy)
}

func amendOKXFuturesOrderPrice(ctx context.Context, client *http.Client, cfg config, compactSymbol string, orderID int64, newPrice string) error {
	instID := okxSwapInstID(compactSymbol)
	bodyMap := map[string]string{
		"instId": instID,
		"ordId":  strconv.FormatInt(orderID, 10),
		"newPx":  newPrice,
	}
	bodyBytes, err := json.Marshal(bodyMap)
	if err != nil {
		return err
	}
	pathWithQuery := okxAPIPrefix + "/trade/amend-order"
	ts := okxTimestampRFC3339MS()
	sign := buildOKXSign(cfg.APISecret, ts, "POST", pathWithQuery, string(bodyBytes))
	host := okxRESTHost(cfg.RESTBase)
	reqURL := host + pathWithQuery

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, reqURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return err
	}
	req.Header.Set("OK-ACCESS-KEY", cfg.APIKey)
	req.Header.Set("OK-ACCESS-SIGN", sign)
	req.Header.Set("OK-ACCESS-TIMESTAMP", ts)
	req.Header.Set("OK-ACCESS-PASSPHRASE", cfg.APIPassphrase)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("okx amend %s: %s", resp.Status, strings.TrimSpace(string(respBody)))
	}
	var dummy []json.RawMessage
	return decodeOKXJSON(respBody, &dummy)
}
