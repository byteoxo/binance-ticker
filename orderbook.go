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

type orderBookResponse struct {
	LastUpdateID int64      `json:"lastUpdateId"`
	Bids         [][]string `json:"bids"`
	Asks         [][]string `json:"asks"`
}

func fetchOrderBook(ctx context.Context, baseURL, symbol string) (orderBookResponse, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return orderBookResponse{}, fmt.Errorf("parse base url: %w", err)
	}
	parsed.Path = futuresDepthPath
	if strings.EqualFold(parsed.Hostname(), "api.binance.com") {
		parsed.Path = spotDepthPath
	}

	query := url.Values{}
	query.Set("symbol", symbol)
	query.Set("limit", strconv.Itoa(orderBookLimit))
	parsed.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return orderBookResponse{}, fmt.Errorf("build request: %w", err)
	}

	client := &http.Client{Timeout: defaultTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return orderBookResponse{}, fmt.Errorf("send request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return orderBookResponse{}, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return orderBookResponse{}, fmt.Errorf("unexpected status %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var ob orderBookResponse
	if err := json.Unmarshal(body, &ob); err != nil {
		return orderBookResponse{}, fmt.Errorf("decode response: %w", err)
	}
	return ob, nil
}

func buildOrderBookUI() (tview.Primitive, *tview.Frame, *tview.Table) {
	obTable := tview.NewTable().SetBorders(false).SetSelectable(false, false)
	obTable.SetBackgroundColor(tcell.ColorDefault)

	hint := tview.NewTextView().SetDynamicColors(true)
	hint.SetBackgroundColor(tcell.ColorDefault)
	hint.SetText("r refresh  Esc/o close")

	content := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(obTable, 0, 1, false).
		AddItem(hint, 1, 0, false)

	frame := tview.NewFrame(content)
	frame.SetBorders(1, 1, 1, 1, 2, 2)
	frame.SetBorder(true)
	frame.SetTitle("Order Book")
	frame.SetBackgroundColor(tcell.ColorDefault)

	overlay := tview.NewFlex().
		AddItem(nil, 0, 1, false).
		AddItem(tview.NewFlex().SetDirection(tview.FlexRow).
			AddItem(nil, 0, 1, false).
			AddItem(frame, 0, 1, false).
			AddItem(nil, 0, 1, false), 68, 0, true).
		AddItem(nil, 0, 1, false)

	return overlay, frame, obTable
}

func (ui *uiModel) showOrderBook() {
	_, _, _, _, _, _, _, _, _, _, _, _, _, _, _, _, panel, _ := ui.state.snapshot()

	var symbol, baseURL string
	switch panel {
	case panelSpot:
		symbol = getChartSymbolForPanel(ui.state, panelSpot)
		baseURL = defaultSpotRESTBaseURL
	default:
		symbol = getChartSymbolForPanel(ui.state, panelFutures)
		baseURL = ui.cfg.RESTBase
	}

	if symbol == "" {
		return
	}

	ui.orderBookSymbol = symbol
	ui.orderBookBaseURL = baseURL
	ui.orderBookOpen = true
	ui.pages.ShowPage("orderbook")
	ui.app.SetFocus(ui.orderBook)

	ctx, cancel := context.WithCancel(context.Background())
	ui.orderBookCancel = cancel
	go ui.runOrderBookLoop(ctx, baseURL, symbol)
}

func (ui *uiModel) restartOrderBook() {
	_, _, _, _, _, _, _, _, _, _, _, _, _, _, _, _, panel, _ := ui.state.snapshot()
	var symbol, baseURL string
	switch panel {
	case panelSpot:
		symbol = getChartSymbolForPanel(ui.state, panelSpot)
		baseURL = defaultSpotRESTBaseURL
	default:
		symbol = getChartSymbolForPanel(ui.state, panelFutures)
		baseURL = ui.cfg.RESTBase
	}
	if symbol == "" || symbol == ui.orderBookSymbol {
		return
	}
	if ui.orderBookCancel != nil {
		ui.orderBookCancel()
	}
	ui.orderBookSymbol = symbol
	ui.orderBookBaseURL = baseURL
	ctx, cancel := context.WithCancel(context.Background())
	ui.orderBookCancel = cancel
	go ui.runOrderBookLoop(ctx, baseURL, symbol)
}

func (ui *uiModel) hideOrderBook() {
	ui.orderBookOpen = false
	ui.pages.HidePage("orderbook")
	ui.app.SetFocus(ui.chart)
	if ui.orderBookCancel != nil {
		ui.orderBookCancel()
		ui.orderBookCancel = nil
	}
}

func (ui *uiModel) runOrderBookLoop(ctx context.Context, baseURL, symbol string) {
	ui.doOrderBookFetch(ctx, baseURL, symbol)

	ticker := time.NewTicker(orderBookRefreshInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			ui.doOrderBookFetch(ctx, baseURL, symbol)
		}
	}
}

func (ui *uiModel) doOrderBookFetch(ctx context.Context, baseURL, symbol string) {
	ob, err := fetchOrderBook(ctx, baseURL, symbol)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		ui.app.QueueUpdateDraw(func() {
			ui.renderOrderBookError()
		})
		return
	}
	ui.app.QueueUpdateDraw(func() {
		ui.renderOrderBook(ob.Bids, ob.Asks, symbol)
	})
}

func (ui *uiModel) renderOrderBook(bids, asks [][]string, symbol string) {
	ui.orderBookTable.Clear()
	ui.orderBookFrame.SetTitle(fmt.Sprintf("Order Book - %s", symbol))

	row := 0

	// ASKS header
	asksHeaderCell := tview.NewTableCell(ui.obCell("[::b][yellow]", "ASKS")).
		SetSelectable(false).
		SetBackgroundColor(tcell.ColorDefault).
		SetExpansion(1)
	ui.orderBookTable.SetCell(row, 0, asksHeaderCell)
	row++

	// Column headers
	colHeaders := []string{"PRICE", "SIZE", "TOTAL"}
	for col, h := range colHeaders {
		cell := tview.NewTableCell(ui.obCell("[::b][yellow]", h)).
			SetSelectable(false).
			SetBackgroundColor(tcell.ColorDefault).
			SetExpansion(1)
		ui.orderBookTable.SetCell(row, col, cell)
	}
	row++

	// Asks: reversed (highest→lowest, closest to spread at bottom)
	// Cumulative total from lowest ask upward (from bottom of asks list)
	var askTotal float64
	askLevels := make([]struct{ price, size, total string }, len(asks))
	for i := len(asks) - 1; i >= 0; i-- {
		size := 0.0
		if len(asks[i]) >= 2 {
			size, _ = strconv.ParseFloat(asks[i][1], 64)
		}
		askTotal += size
		price := ""
		if len(asks[i]) >= 1 {
			price = asks[i][0]
		}
		askLevels[len(asks)-1-i] = struct{ price, size, total string }{
			price: price,
			size:  asks[i][1],
			total: formatCompactFloat(askTotal),
		}
	}
	// askLevels[0] = highest ask, askLevels[last] = lowest ask (closest to spread)
	for _, level := range askLevels {
		for col, text := range []string{level.price, level.size, level.total} {
			cell := tview.NewTableCell(ui.obCell("[red]", text)).
				SetSelectable(false).
				SetBackgroundColor(tcell.ColorDefault).
				SetExpansion(1)
			ui.orderBookTable.SetCell(row, col, cell)
		}
		row++
	}

	// Spread row
	spread := ""
	spreadPct := ""
	if len(asks) > 0 && len(bids) > 0 {
		bestAsk, errA := strconv.ParseFloat(asks[0][0], 64)
		bestBid, errB := strconv.ParseFloat(bids[0][0], 64)
		if errA == nil && errB == nil && bestAsk > 0 {
			diff := bestAsk - bestBid
			pct := diff / bestAsk * 100
			spread = fmt.Sprintf("Spread: %s (%.4f%%)", formatCompactFloat(diff), pct)
			_ = spreadPct
		}
	}
	if spread == "" {
		spread = "Spread: -"
	}
	spreadCell := tview.NewTableCell(ui.obCell("[gray]", spread)).
		SetSelectable(false).
		SetBackgroundColor(tcell.ColorDefault).
		SetExpansion(3)
	ui.orderBookTable.SetCell(row, 0, spreadCell)
	row++

	// Bids: top→bottom (best bid first), cumulative total
	var bidTotal float64
	for _, level := range bids {
		size := 0.0
		if len(level) >= 2 {
			size, _ = strconv.ParseFloat(level[1], 64)
		}
		bidTotal += size
		price := ""
		sz := ""
		if len(level) >= 1 {
			price = level[0]
		}
		if len(level) >= 2 {
			sz = level[1]
		}
		totalStr := formatCompactFloat(bidTotal)
		for col, text := range []string{price, sz, totalStr} {
			cell := tview.NewTableCell(ui.obCell("[green]", text)).
				SetSelectable(false).
				SetBackgroundColor(tcell.ColorDefault).
				SetExpansion(1)
			ui.orderBookTable.SetCell(row, col, cell)
		}
		row++
	}

	// BIDS label at bottom
	bidsFooterCell := tview.NewTableCell(ui.obCell("[::b][yellow]", "BIDS")).
		SetSelectable(false).
		SetBackgroundColor(tcell.ColorDefault).
		SetExpansion(1)
	ui.orderBookTable.SetCell(row, 0, bidsFooterCell)
}

func (ui *uiModel) renderOrderBookError() {
	ui.orderBookTable.Clear()
	cell := tview.NewTableCell(ui.obCell("[red]", "fetch error")).
		SetSelectable(false).
		SetBackgroundColor(tcell.ColorDefault).
		SetExpansion(1)
	ui.orderBookTable.SetCell(0, 0, cell)
}

func (ui *uiModel) obCell(colorTag, text string) string {
	if ui.cfg.NoColor {
		return text
	}
	return colorTag + escapeTView(text) + "[-]"
}
