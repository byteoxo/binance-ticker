package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

// openOrder represents a single open (unfilled) order.
type openOrder struct {
	Symbol       string
	OrderID      int64
	Side         string // BUY / SELL / buy / sell
	Type         string // LIMIT, MARKET, TRIGGER, etc.
	Price        float64
	OrigQty      float64
	FilledQty    float64
	Status       string
	TimeInForce  string
	Time         int64
	TriggerPrice float64 // Gate.io price-triggered order: trigger price
	TriggerRule  int     // Gate.io price-triggered order: 1 = >=, 2 = <=
}

// ── Binance Futures open orders ───────────────────────────────────────────────

func fetchBinanceFuturesOpenOrders(ctx context.Context, client *http.Client, cfg config) ([]openOrder, error) {
	query := url.Values{}
	query.Set("timestamp", strconv.FormatInt(time.Now().UnixMilli(), 10))
	query.Set("recvWindow", strconv.FormatInt(int64(cfg.Timeout/time.Millisecond), 10))

	endpoint, err := buildSignedURL(cfg.RESTBase, "/fapi/v1/openOrders", query, cfg.APISecret)
	if err != nil {
		return nil, fmt.Errorf("build binance futures open orders url: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build binance futures open orders request: %w", err)
	}
	req.Header.Set("X-MBX-APIKEY", cfg.APIKey)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send binance futures open orders request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read binance futures open orders response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("binance futures open orders status %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var payload []struct {
		Symbol      string `json:"symbol"`
		OrderID     int64  `json:"orderId"`
		Side        string `json:"side"`
		Type        string `json:"type"`
		Price       string `json:"price"`
		OrigQty     string `json:"origQty"`
		ExecutedQty string `json:"executedQty"`
		Status      string `json:"status"`
		TimeInForce string `json:"timeInForce"`
		Time        int64  `json:"time"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode binance futures open orders: %w", err)
	}

	orders := make([]openOrder, 0, len(payload))
	for _, item := range payload {
		price, _ := strconv.ParseFloat(item.Price, 64)
		origQty, _ := strconv.ParseFloat(item.OrigQty, 64)
		filledQty, _ := strconv.ParseFloat(item.ExecutedQty, 64)
		orders = append(orders, openOrder{
			Symbol:      item.Symbol,
			OrderID:     item.OrderID,
			Side:        item.Side,
			Type:        item.Type,
			Price:       price,
			OrigQty:     origQty,
			FilledQty:   filledQty,
			Status:      item.Status,
			TimeInForce: item.TimeInForce,
			Time:        item.Time,
		})
	}
	return orders, nil
}

// ── Binance Spot open orders ──────────────────────────────────────────────────

func fetchBinanceSpotOpenOrders(ctx context.Context, client *http.Client, cfg config) ([]openOrder, error) {
	query := url.Values{}
	query.Set("timestamp", strconv.FormatInt(time.Now().UnixMilli(), 10))
	query.Set("recvWindow", strconv.FormatInt(int64(cfg.Timeout/time.Millisecond), 10))

	endpoint, err := buildSignedURL(defaultSpotRESTBaseURL, "/api/v3/openOrders", query, cfg.APISecret)
	if err != nil {
		return nil, fmt.Errorf("build binance spot open orders url: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("build binance spot open orders request: %w", err)
	}
	req.Header.Set("X-MBX-APIKEY", cfg.APIKey)

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("send binance spot open orders request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read binance spot open orders response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("binance spot open orders status %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var payload []struct {
		Symbol      string `json:"symbol"`
		OrderID     int64  `json:"orderId"`
		Side        string `json:"side"`
		Type        string `json:"type"`
		Price       string `json:"price"`
		OrigQty     string `json:"origQty"`
		ExecutedQty string `json:"executedQty"`
		Status      string `json:"status"`
		TimeInForce string `json:"timeInForce"`
		Time        int64  `json:"time"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode binance spot open orders: %w", err)
	}

	orders := make([]openOrder, 0, len(payload))
	for _, item := range payload {
		price, _ := strconv.ParseFloat(item.Price, 64)
		origQty, _ := strconv.ParseFloat(item.OrigQty, 64)
		filledQty, _ := strconv.ParseFloat(item.ExecutedQty, 64)
		orders = append(orders, openOrder{
			Symbol:      item.Symbol,
			OrderID:     item.OrderID,
			Side:        item.Side,
			Type:        item.Type,
			Price:       price,
			OrigQty:     origQty,
			FilledQty:   filledQty,
			Status:      item.Status,
			TimeInForce: item.TimeInForce,
			Time:        item.Time,
		})
	}
	return orders, nil
}

// ── Binance Futures Order Management ──────────────────────────────────────────

// placeBinanceFuturesOrder places an order on Binance futures.
// orderType: LIMIT, STOP, TAKE_PROFIT, STOP_MARKET, TAKE_PROFIT_MARKET.
func placeBinanceFuturesOrder(ctx context.Context, client *http.Client, cfg config, symbol, side, orderType, quantity, price, stopPrice, timeInForce string) (openOrder, error) {
	query := url.Values{}
	query.Set("symbol", symbol)
	query.Set("side", strings.ToUpper(side))
	query.Set("type", strings.ToUpper(orderType))
	query.Set("quantity", quantity)
	if timeInForce != "" {
		query.Set("timeInForce", timeInForce)
	}
	if price != "" {
		query.Set("price", price)
	}
	if stopPrice != "" {
		query.Set("stopPrice", stopPrice)
	}
	query.Set("timestamp", strconv.FormatInt(time.Now().UnixMilli(), 10))
	query.Set("recvWindow", strconv.FormatInt(int64(cfg.Timeout/time.Millisecond), 10))

	endpoint, err := buildSignedURL(cfg.RESTBase, "/fapi/v1/order", query, cfg.APISecret)
	if err != nil {
		return openOrder{}, fmt.Errorf("build binance order url: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return openOrder{}, fmt.Errorf("build binance order request: %w", err)
	}
	req.Header.Set("X-MBX-APIKEY", cfg.APIKey)

	resp, err := client.Do(req)
	if err != nil {
		return openOrder{}, fmt.Errorf("binance order request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return openOrder{}, fmt.Errorf("read binance order response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return openOrder{}, fmt.Errorf("binance order status %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var payload struct {
		Symbol      string `json:"symbol"`
		OrderID     int64  `json:"orderId"`
		Side        string `json:"side"`
		Type        string `json:"type"`
		Price       string `json:"price"`
		OrigQty     string `json:"origQty"`
		ExecutedQty string `json:"executedQty"`
		Status      string `json:"status"`
		TimeInForce string `json:"timeInForce"`
		UpdateTime  int64  `json:"updateTime"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return openOrder{}, fmt.Errorf("decode binance order response: %w", err)
	}

	p, _ := strconv.ParseFloat(payload.Price, 64)
	origQty, _ := strconv.ParseFloat(payload.OrigQty, 64)
	filledQty, _ := strconv.ParseFloat(payload.ExecutedQty, 64)
	return openOrder{
		Symbol:      payload.Symbol,
		OrderID:     payload.OrderID,
		Side:        payload.Side,
		Type:        payload.Type,
		Price:       p,
		OrigQty:     origQty,
		FilledQty:   filledQty,
		Status:      payload.Status,
		TimeInForce: payload.TimeInForce,
		Time:        payload.UpdateTime,
	}, nil
}

// cancelBinanceFuturesOrder cancels a single futures order on Binance.
func cancelBinanceFuturesOrder(ctx context.Context, client *http.Client, cfg config, symbol string, orderID int64) error {
	query := url.Values{}
	query.Set("symbol", symbol)
	query.Set("orderId", strconv.FormatInt(orderID, 10))
	query.Set("timestamp", strconv.FormatInt(time.Now().UnixMilli(), 10))
	query.Set("recvWindow", strconv.FormatInt(int64(cfg.Timeout/time.Millisecond), 10))

	endpoint, err := buildSignedURL(cfg.RESTBase, "/fapi/v1/order", query, cfg.APISecret)
	if err != nil {
		return fmt.Errorf("build binance cancel url: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, endpoint, nil)
	if err != nil {
		return fmt.Errorf("build binance cancel request: %w", err)
	}
	req.Header.Set("X-MBX-APIKEY", cfg.APIKey)

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("binance cancel request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("binance cancel status %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

// amendBinanceFuturesOrder modifies the price and quantity of an existing futures order on Binance.
func amendBinanceFuturesOrder(ctx context.Context, client *http.Client, cfg config, symbol string, orderID int64, side, quantity, price string) error {
	query := url.Values{}
	query.Set("orderId", strconv.FormatInt(orderID, 10))
	query.Set("symbol", symbol)
	query.Set("side", strings.ToUpper(side))
	query.Set("quantity", quantity)
	query.Set("price", price)
	query.Set("timestamp", strconv.FormatInt(time.Now().UnixMilli(), 10))
	query.Set("recvWindow", strconv.FormatInt(int64(cfg.Timeout/time.Millisecond), 10))

	endpoint, err := buildSignedURL(cfg.RESTBase, "/fapi/v1/order", query, cfg.APISecret)
	if err != nil {
		return fmt.Errorf("build binance amend url: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, nil)
	if err != nil {
		return fmt.Errorf("build binance amend request: %w", err)
	}
	req.Header.Set("X-MBX-APIKEY", cfg.APIKey)

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("binance amend request: %w", err)
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("binance amend status %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return nil
}

// ── Gate.io Futures open orders ───────────────────────────────────────────────

func fetchGateOpenOrders(ctx context.Context, client *http.Client, cfg config) ([]openOrder, error) {
	const path = "/api/v4/futures/usdt/orders"
	ts := time.Now().Unix()

	query := url.Values{}
	query.Set("status", "open")
	queryStr := query.Encode()

	sig := buildGateSignature(cfg.APISecret, "GET", path, queryStr, "", ts)

	parsed, err := url.Parse(cfg.RESTBase)
	if err != nil {
		return nil, fmt.Errorf("parse gate rest base: %w", err)
	}
	parsed.Path = path
	parsed.RawQuery = queryStr

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build gate open orders request: %w", err)
	}
	req.Header.Set("KEY", cfg.APIKey)
	req.Header.Set("SIGN", sig)
	req.Header.Set("Timestamp", strconv.FormatInt(ts, 10))
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gate open orders request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read gate open orders response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gate open orders status %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var payload []struct {
		ID         int64  `json:"id"`
		Contract   string `json:"contract"`
		Size       int64  `json:"size"`  // positive=buy, negative=sell
		Price      string `json:"price"`
		Left       int64  `json:"left"`  // remaining unfilled size
		Type       string `json:"type"`  // "limit" or "market"
		Tif        string `json:"tif"`   // time in force: gtc, ioc, poc, fok
		Status     string `json:"status"`
		CreateTime float64 `json:"create_time"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode gate open orders: %w", err)
	}

	orders := make([]openOrder, 0, len(payload))
	for _, item := range payload {
		price, _ := strconv.ParseFloat(item.Price, 64)
		origQty := float64(item.Size)
		if origQty < 0 {
			origQty = -origQty
		}
		filled := origQty - float64(item.Left)
		if filled < 0 {
			filled = 0
		}
		side := "BUY"
		if item.Size < 0 {
			side = "SELL"
		}
		orders = append(orders, openOrder{
			Symbol:      item.Contract,
			OrderID:     item.ID,
			Side:        side,
			Type:        strings.ToUpper(item.Type),
			Price:       price,
			OrigQty:     origQty,
			FilledQty:   filled,
			Status:      strings.ToUpper(item.Status),
			TimeInForce: strings.ToUpper(item.Tif),
			Time:        int64(item.CreateTime * 1000),
		})
	}
	return orders, nil
}

// ── Gate.io Futures price-triggered (conditional) orders ──────────────────────

func fetchGatePriceTriggeredOrders(ctx context.Context, client *http.Client, cfg config) ([]openOrder, error) {
	const path = "/api/v4/futures/usdt/price_orders"
	ts := time.Now().Unix()

	query := url.Values{}
	query.Set("status", "open")
	queryStr := query.Encode()

	sig := buildGateSignature(cfg.APISecret, "GET", path, queryStr, "", ts)

	parsed, err := url.Parse(cfg.RESTBase)
	if err != nil {
		return nil, fmt.Errorf("parse gate rest base: %w", err)
	}
	parsed.Path = path
	parsed.RawQuery = queryStr

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return nil, fmt.Errorf("build gate price orders request: %w", err)
	}
	req.Header.Set("KEY", cfg.APIKey)
	req.Header.Set("SIGN", sig)
	req.Header.Set("Timestamp", strconv.FormatInt(ts, 10))
	req.Header.Set("Accept", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("gate price orders request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read gate price orders response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("gate price orders status %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var payload []struct {
		ID         int64   `json:"id"`
		Status     string  `json:"status"`
		CreateTime float64 `json:"create_time"`
		OrderType  string  `json:"order_type"`
		Trigger    struct {
			StrategyType int    `json:"strategy_type"`
			PriceType    int    `json:"price_type"`
			Price        string `json:"price"`
			Rule         int    `json:"rule"`
			Expiration   int    `json:"expiration"`
		} `json:"trigger"`
		Initial struct {
			Contract     string `json:"contract"`
			Size         int64  `json:"size"`
			Price        string `json:"price"`
			Tif          string `json:"tif"`
			IsClose      bool   `json:"is_close"`
			IsReduceOnly bool   `json:"is_reduce_only"`
		} `json:"initial"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode gate price orders: %w", err)
	}

	orders := make([]openOrder, 0, len(payload))
	for _, item := range payload {
		price, _ := strconv.ParseFloat(item.Initial.Price, 64)
		triggerPrice, _ := strconv.ParseFloat(item.Trigger.Price, 64)

		origQty := float64(item.Initial.Size)
		if origQty < 0 {
			origQty = -origQty
		}
		side := "BUY"
		if item.Initial.Size < 0 {
			side = "SELL"
		}
		if item.Initial.Size == 0 {
			switch item.OrderType {
			case "close-long-order", "close-long-position", "plan-close-long-position":
				side = "SELL"
			case "close-short-order", "close-short-position", "plan-close-short-position":
				side = "BUY"
			}
		}

		orders = append(orders, openOrder{
			Symbol:       item.Initial.Contract,
			OrderID:      item.ID,
			Side:         side,
			Type:         "TRIGGER",
			Price:        price,
			OrigQty:      origQty,
			FilledQty:    0,
			Status:       strings.ToUpper(item.Status),
			TimeInForce:  strings.ToUpper(item.Initial.Tif),
			Time:         int64(item.CreateTime * 1000),
			TriggerPrice: triggerPrice,
			TriggerRule:  item.Trigger.Rule,
		})
	}
	return orders, nil
}

// fetchOpenOrders routes to the correct exchange.
func fetchOpenOrders(ctx context.Context, client *http.Client, cfg config) (futures []openOrder, spot []openOrder, err error) {
	if !cfg.hasAccountAuth() {
		return nil, nil, nil
	}
	if cfg.isGate() {
		f, e := fetchGateOpenOrders(ctx, client, cfg)
		if e != nil {
			return nil, nil, e
		}
		triggered, _ := fetchGatePriceTriggeredOrders(ctx, client, cfg)
		f = append(f, triggered...)
		return f, nil, nil
	}
	f, e1 := fetchBinanceFuturesOpenOrders(ctx, client, cfg)
	if e1 != nil {
		return nil, nil, e1
	}
	s, e2 := fetchBinanceSpotOpenOrders(ctx, client, cfg)
	return f, s, e2
}

// ── Open Orders UI overlay ────────────────────────────────────────────────────

const openOrdersRefreshInterval = 5 * time.Second

func buildOpenOrdersUI() (tview.Primitive, *tview.Frame, *tview.Table, *tview.TextView) {
	ooTable := tview.NewTable().SetBorders(false).SetSelectable(false, false).SetFixed(1, 0)
	ooTable.SetBackgroundColor(tcell.ColorDefault)

	hint := tview.NewTextView().SetDynamicColors(true)
	hint.SetBackgroundColor(tcell.ColorDefault)
	hint.SetText("r refresh  Esc/u close")

	content := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(ooTable, 0, 1, false).
		AddItem(hint, 1, 0, false)

	frame := tview.NewFrame(content)
	frame.SetBorders(1, 1, 1, 1, 2, 2)
	frame.SetBorder(true)
	frame.SetTitle("Open Orders")
	frame.SetBackgroundColor(tcell.ColorDefault)

	overlay := tview.NewFlex().
		AddItem(nil, 0, 1, false).
		AddItem(tview.NewFlex().SetDirection(tview.FlexRow).
			AddItem(nil, 0, 1, false).
			AddItem(frame, 0, 3, false).
			AddItem(nil, 0, 1, false), 90, 0, true).
		AddItem(nil, 0, 1, false)

	return overlay, frame, ooTable, hint
}

func (ui *uiModel) showOpenOrders() {
	if !ui.cfg.hasAccountAuth() {
		ui.state.setModal("Account auth not configured (api_key / api_secret missing)")
		ui.refreshNow()
		return
	}
	ui.openOrdersOpen = true
	ui.pages.ShowPage("openorders")

	if ui.cfg.isGate() {
		ui.openOrdersTable.SetSelectable(true, false)
		ui.openOrdersTable.SetFixed(2, 0)
		ui.openOrdersTable.SetSelectedStyle(tcell.StyleDefault.
			Foreground(tcell.ColorWhite).
			Background(tcell.ColorDarkBlue).
			Attributes(tcell.AttrBold))
		ui.resetOpenOrdersHint()
		ui.app.SetFocus(ui.openOrdersTable)
	} else {
		ui.openOrdersTable.SetSelectable(false, false)
		ui.openOrdersTable.SetFixed(1, 0)
		ui.resetOpenOrdersHint()
		ui.app.SetFocus(ui.openOrders)
	}

	ctx, cancel := context.WithCancel(context.Background())
	ui.openOrdersCancel = cancel
	go ui.runOpenOrdersLoop(ctx)
}

func (ui *uiModel) hideOpenOrders() {
	ui.openOrdersOpen = false
	if ui.orderFormOpen {
		ui.orderFormOpen = false
		ui.pages.RemovePage("orderform")
	}
	ui.pages.HidePage("openorders")
	ui.app.SetFocus(ui.chart)
	if ui.openOrdersCancel != nil {
		ui.openOrdersCancel()
		ui.openOrdersCancel = nil
	}
}

func (ui *uiModel) runOpenOrdersLoop(ctx context.Context) {
	ui.doOpenOrdersFetch(ctx)

	ticker := time.NewTicker(openOrdersRefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ui.doOpenOrdersFetch(ctx)
		}
	}
}

func (ui *uiModel) doOpenOrdersFetch(ctx context.Context) {
	client := &http.Client{Timeout: ui.cfg.Timeout}
	futures, spot, err := fetchOpenOrders(ctx, client, ui.cfg)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		ui.app.QueueUpdateDraw(func() {
			ui.renderOpenOrdersError(err.Error())
		})
		return
	}
	ui.app.QueueUpdateDraw(func() {
		ui.renderOpenOrders(futures, spot)
	})
}

func (ui *uiModel) renderOpenOrders(futures, spot []openOrder) {
	ui.openOrdersTable.Clear()
	ui.openOrdersData = nil
	ui.openOrdersDataOffset = 0
	gateMode := ui.cfg.isGate()

	row := 0
	headers := []string{"SYMBOL", "SIDE", "TYPE", "PRICE", "QTY", "FILLED", "TIF", "STATUS", "TIME"}

	setHeader := func(text string, col int) {
		cell := tview.NewTableCell(ui.ooCell("[::b][yellow]", text)).
			SetSelectable(false).
			SetBackgroundColor(tcell.ColorDefault).
			SetExpansion(1)
		ui.openOrdersTable.SetCell(row, col, cell)
	}

	setCell := func(text, color string, r, col int) {
		cell := tview.NewTableCell(ui.ooCell(color, text)).
			SetSelectable(gateMode).
			SetBackgroundColor(tcell.ColorDefault).
			SetExpansion(1)
		ui.openOrdersTable.SetCell(r, col, cell)
	}

	renderSection := func(title string, orders []openOrder) int {
		sectionStart := row
		titleCell := tview.NewTableCell(ui.ooCell("[::b][white]", title)).
			SetSelectable(false).
			SetBackgroundColor(tcell.ColorDefault).
			SetExpansion(len(headers))
		ui.openOrdersTable.SetCell(row, 0, titleCell)
		row++

		for col, h := range headers {
			setHeader(h, col)
		}
		row++

		dataStart := row

		if len(orders) == 0 {
			cell := tview.NewTableCell(ui.ooCell("[gray]", "no open orders")).
				SetSelectable(false).
				SetBackgroundColor(tcell.ColorDefault).
				SetExpansion(len(headers))
			ui.openOrdersTable.SetCell(row, 0, cell)
			row++
			_ = sectionStart
			return dataStart
		}

		for _, o := range orders {
			sideColor := "[green]"
			if strings.ToUpper(o.Side) == "SELL" {
				sideColor = "[red]"
			}
			priceStr := formatCompactFloat(o.Price)
			if o.Price == 0 {
				priceStr = "MKT"
			}
			if o.TriggerPrice > 0 {
				rule := "≥"
				if o.TriggerRule == 2 {
					rule = "≤"
				}
				priceStr = fmt.Sprintf("%s (%s%s)", priceStr, rule, formatCompactFloat(o.TriggerPrice))
			}
			timeStr := time.Unix(o.Time/1000, 0).Format("01-02 15:04")

			setCell(o.Symbol, "[white]", row, 0)
			setCell(strings.ToUpper(o.Side), sideColor, row, 1)
			setCell(strings.ToUpper(o.Type), "[white]", row, 2)
			setCell(priceStr, "[white]", row, 3)
			setCell(formatCompactFloat(o.OrigQty), "[white]", row, 4)
			setCell(formatCompactFloat(o.FilledQty), "[gray]", row, 5)
			setCell(o.TimeInForce, "[gray]", row, 6)
			setCell(strings.ToUpper(o.Status), "[white]", row, 7)
			setCell(timeStr, "[gray]", row, 8)
			row++
		}
		return dataStart
	}

	if gateMode {
		dataStart := renderSection("Gate.io Futures", futures)
		ui.openOrdersData = futures
		ui.openOrdersDataOffset = dataStart
		if len(futures) > 0 {
			ui.openOrdersTable.Select(dataStart, 0)
		}
	} else {
		renderSection("Binance Futures", futures)
		renderSection("Binance Spot", spot)
	}
}

func (ui *uiModel) renderOpenOrdersError(msg string) {
	ui.openOrdersTable.Clear()
	cell := tview.NewTableCell(ui.ooCell("[red]", "fetch error: "+msg)).
		SetSelectable(false).
		SetBackgroundColor(tcell.ColorDefault).
		SetExpansion(1)
	ui.openOrdersTable.SetCell(0, 0, cell)
}

func (ui *uiModel) ooCell(colorTag, text string) string {
	if ui.cfg.NoColor {
		return text
	}
	return colorTag + escapeTView(text) + "[-]"
}

// ── Gate.io order management helpers ──────────────────────────────────────────

func (ui *uiModel) resetOpenOrdersHint() {
	if ui.cfg.isGate() {
		ui.openOrdersHint.SetText("n new  d cancel  e edit price  r refresh  Esc/u close")
	} else {
		ui.openOrdersHint.SetText("r refresh  Esc/u close")
	}
}

func (ui *uiModel) selectedOpenOrder() (openOrder, bool) {
	if len(ui.openOrdersData) == 0 {
		return openOrder{}, false
	}
	row, _ := ui.openOrdersTable.GetSelection()
	idx := row - ui.openOrdersDataOffset
	if idx < 0 || idx >= len(ui.openOrdersData) {
		return openOrder{}, false
	}
	return ui.openOrdersData[idx], true
}

func (ui *uiModel) closeOrderForm() {
	ui.orderFormOpen = false
	ui.pages.RemovePage("orderform")
	ui.app.SetFocus(ui.openOrdersTable)
}

func (ui *uiModel) showNewOrderForm() {
	_, _, _, _, _, chartSymbol, _, _, _, _, _, _, _, _, _, _, _, _, _ := ui.state.snapshot()

	selectedStyle := tcell.StyleDefault.
		Foreground(tcell.ColorWhite).
		Background(tcell.ColorDarkBlue).
		Attributes(tcell.AttrBold)
	unselectedStyle := tcell.StyleDefault.
		Foreground(tcell.ColorWhite).
		Background(tcell.ColorDefault)

	form := tview.NewForm()
	form.AddInputField("Contract", chartSymbol, 20, nil, nil)
	dropdownReady := false
	form.AddDropDown("Side", []string{"BUY (long)", "SELL (short)"}, 0, func(option string, optionIndex int) {
		if dropdownReady {
			if p, ok := form.GetFormItem(2).(tview.Primitive); ok {
				ui.app.SetFocus(p)
			}
		}
	})
	dropdownReady = true
	form.GetFormItem(1).(*tview.DropDown).SetListStyles(unselectedStyle, selectedStyle)
	form.AddInputField("Price", "", 20, func(text string, ch rune) bool {
		return ch == '.' || (ch >= '0' && ch <= '9')
	}, nil)
	form.AddInputField("Size", "1", 10, func(text string, ch rune) bool {
		return ch >= '0' && ch <= '9'
	}, nil)
	form.AddButton("Submit", func() {
		contractField := form.GetFormItem(0).(*tview.InputField)
		sideDropdown := form.GetFormItem(1).(*tview.DropDown)
		priceField := form.GetFormItem(2).(*tview.InputField)
		sizeField := form.GetFormItem(3).(*tview.InputField)

		contract := strings.TrimSpace(contractField.GetText())
		if contract == "" {
			return
		}
		priceStr := strings.TrimSpace(priceField.GetText())
		if _, err := strconv.ParseFloat(priceStr, 64); err != nil {
			return
		}
		sizeVal, err := strconv.ParseInt(strings.TrimSpace(sizeField.GetText()), 10, 64)
		if err != nil || sizeVal <= 0 {
			return
		}
		sideIdx, _ := sideDropdown.GetCurrentOption()
		if sideIdx == 1 {
			sizeVal = -sizeVal
		}

		ui.closeOrderForm()
		ui.openOrdersHint.SetText("Placing order...")

		go func() {
			client := &http.Client{Timeout: ui.cfg.Timeout}
			_, err := placeGateFuturesLimitOrder(context.Background(), client, ui.cfg, contract, sizeVal, priceStr)
			if err != nil {
				ui.app.QueueUpdateDraw(func() {
					ui.renderOpenOrdersError("place order: " + err.Error())
					ui.resetOpenOrdersHint()
				})
				return
			}
			ui.doOpenOrdersFetch(context.Background())
			ui.app.QueueUpdateDraw(func() {
				ui.resetOpenOrdersHint()
			})
		}()
	})
	form.AddButton("Cancel", func() {
		ui.closeOrderForm()
	})
	form.SetBorder(true).SetTitle(" New Limit Order ").SetTitleAlign(tview.AlignCenter)
	form.SetBackgroundColor(tcell.ColorDefault)

	overlay := tview.NewFlex().
		AddItem(nil, 0, 1, false).
		AddItem(tview.NewFlex().SetDirection(tview.FlexRow).
			AddItem(nil, 0, 1, false).
			AddItem(form, 15, 0, true).
			AddItem(nil, 0, 1, false), 50, 0, true).
		AddItem(nil, 0, 1, false)

	ui.orderFormOpen = true
	ui.pages.AddPage("orderform", overlay, true, true)
	ui.app.SetFocus(form)
}

func (ui *uiModel) showCancelOrderConfirm() {
	order, ok := ui.selectedOpenOrder()
	if !ok {
		return
	}

	modal := tview.NewModal().
		SetText(fmt.Sprintf("Cancel order #%d?\n%s %s @ %s",
			order.OrderID, order.Side, order.Symbol, formatCompactFloat(order.Price))).
		AddButtons([]string{"Yes", "No"}).
		SetDoneFunc(func(_ int, label string) {
			ui.closeOrderForm()
			if label != "Yes" {
				return
			}
			ui.openOrdersHint.SetText("Cancelling order...")
			go func() {
				client := &http.Client{Timeout: ui.cfg.Timeout}
				var err error
				if order.Type == "TRIGGER" {
					err = cancelGateFuturesPriceOrder(context.Background(), client, ui.cfg, order.OrderID)
				} else {
					err = cancelGateFuturesOrder(context.Background(), client, ui.cfg, order.OrderID)
				}
				if err != nil {
					ui.app.QueueUpdateDraw(func() {
						ui.renderOpenOrdersError("cancel order: " + err.Error())
						ui.resetOpenOrdersHint()
					})
					return
				}
				ui.doOpenOrdersFetch(context.Background())
				ui.app.QueueUpdateDraw(func() {
					ui.resetOpenOrdersHint()
				})
			}()
		})
	modal.SetBackgroundColor(tcell.ColorDefault)

	ui.orderFormOpen = true
	ui.pages.AddPage("orderform", modal, true, true)
	ui.app.SetFocus(modal)
}

func (ui *uiModel) showEditPriceForm() {
	order, ok := ui.selectedOpenOrder()
	if !ok {
		return
	}
	if order.Type == "TRIGGER" {
		return
	}

	form := tview.NewForm()
	form.AddInputField("New Price", formatCompactFloat(order.Price), 20, func(text string, ch rune) bool {
		return ch == '.' || (ch >= '0' && ch <= '9')
	}, nil)
	form.AddButton("Submit", func() {
		priceField := form.GetFormItem(0).(*tview.InputField)
		newPrice := strings.TrimSpace(priceField.GetText())
		if _, err := strconv.ParseFloat(newPrice, 64); err != nil {
			return
		}

		ui.closeOrderForm()
		ui.openOrdersHint.SetText("Amending order price...")

		go func() {
			client := &http.Client{Timeout: ui.cfg.Timeout}
			err := amendGateFuturesOrderPrice(context.Background(), client, ui.cfg, order.OrderID, newPrice)
			if err != nil {
				ui.app.QueueUpdateDraw(func() {
					ui.renderOpenOrdersError("amend price: " + err.Error())
					ui.resetOpenOrdersHint()
				})
				return
			}
			ui.doOpenOrdersFetch(context.Background())
			ui.app.QueueUpdateDraw(func() {
				ui.resetOpenOrdersHint()
			})
		}()
	})
	form.AddButton("Cancel", func() {
		ui.closeOrderForm()
	})
	form.SetBorder(true).
		SetTitle(fmt.Sprintf(" Edit #%d %s %s @ %s ",
			order.OrderID, order.Side, order.Symbol, formatCompactFloat(order.Price))).
		SetTitleAlign(tview.AlignCenter)
	form.SetBackgroundColor(tcell.ColorDefault)

	overlay := tview.NewFlex().
		AddItem(nil, 0, 1, false).
		AddItem(tview.NewFlex().SetDirection(tview.FlexRow).
			AddItem(nil, 0, 1, false).
			AddItem(form, 9, 0, true).
			AddItem(nil, 0, 1, false), 50, 0, true).
		AddItem(nil, 0, 1, false)

	ui.orderFormOpen = true
	ui.pages.AddPage("orderform", overlay, true, true)
	ui.app.SetFocus(form)
}
