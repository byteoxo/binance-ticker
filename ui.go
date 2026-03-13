package main

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/rivo/tview"
)

type uiModel struct {
	app              *tview.Application
	pages            *tview.Pages
	header           *tview.TextView
	status           *tview.TextView
	table            *tview.Table
	positions        *tview.Table
	chart            *tview.TextView
	footer           *tview.TextView
	help             tview.Primitive
	cfg              config
	loc              *time.Location
	state            *appState
	changeChart      func(int)
	changeInterval   func()
	helpOpen         bool
	orderBook        tview.Primitive
	orderBookFrame   *tview.Frame
	orderBookTable   *tview.Table
	orderBookOpen    bool
	orderBookCancel  context.CancelFunc
	orderBookSymbol  string
	orderBookBaseURL string
}

func newUI(cfg config, loc *time.Location, state *appState, changeChart func(int), changeInterval func()) *uiModel {
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
	positions := tview.NewTable().SetBorders(false).SetSelectable(false, false).SetFixed(1, 0)
	chart := tview.NewTextView().SetDynamicColors(true)
	footer := tview.NewTextView().SetDynamicColors(true)
	helpTable := tview.NewTable().SetBorders(false).SetSelectable(false, false)
	helpTable.SetBackgroundColor(tcell.ColorDefault)
	helpTable.SetCell(0, 0, tview.NewTableCell("KEY").SetAttributes(tcell.AttrBold).SetTextColor(tcell.ColorYellow).SetSelectable(false))
	helpTable.SetCell(0, 1, tview.NewTableCell("      ").SetSelectable(false))
	helpTable.SetCell(0, 2, tview.NewTableCell("ACTION").SetAttributes(tcell.AttrBold).SetTextColor(tcell.ColorYellow).SetSelectable(false))

	helpRows := [][2]string{
		{"/ or h", "Open or close help"},
		{"Tab", "Switch futures / spot panel"},
		{"Up / Left", "Previous chart symbol"},
		{"Down / Right", "Next chart symbol"},
		{"i", "Cycle chart interval (1h→2h→4h→1d→3d)"},
		{"o", "Open order book for current symbol"},
		{"q", "Quit"},
		{"Ctrl+C", "Quit"},
		{"Esc", "Close help / modal / order book"},
	}
	for i, row := range helpRows {
		helpTable.SetCell(i+1, 0, tview.NewTableCell(row[0]).SetSelectable(false))
		helpTable.SetCell(i+1, 1, tview.NewTableCell("      ").SetSelectable(false))
		helpTable.SetCell(i+1, 2, tview.NewTableCell(row[1]).SetSelectable(false))
	}

	helpTitle := tview.NewTextView().SetDynamicColors(true)
	helpTitle.SetBackgroundColor(tcell.ColorDefault)
	helpTitle.SetText("[::b]Shortcuts[-]")
	helpHint := tview.NewTextView().SetDynamicColors(true)
	helpHint.SetBackgroundColor(tcell.ColorDefault)
	helpHint.SetText("Esc / Enter / h / / to close")
	helpSpacerTop := tview.NewTextView()
	helpSpacerTop.SetBackgroundColor(tcell.ColorDefault)
	helpSpacerBottom := tview.NewTextView()
	helpSpacerBottom.SetBackgroundColor(tcell.ColorDefault)
	helpContent := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(helpTitle, 1, 0, false).
		AddItem(helpSpacerTop, 1, 0, false).
		AddItem(helpTable, 0, 1, false).
		AddItem(helpSpacerBottom, 1, 0, false).
		AddItem(helpHint, 1, 0, false)
	helpFrame := tview.NewFrame(helpContent)
	helpFrame.SetBorders(1, 1, 1, 1, 2, 2)
	helpFrame.SetBorder(true)
	helpFrame.SetTitle("Help")
	helpFrame.SetBackgroundColor(tcell.ColorDefault)
	help := tview.NewFlex().
		AddItem(nil, 0, 1, false).
		AddItem(tview.NewFlex().SetDirection(tview.FlexRow).
			AddItem(nil, 0, 1, false).
			AddItem(helpFrame, 14, 0, false).
			AddItem(nil, 0, 1, false), 64, 0, true).
		AddItem(nil, 0, 1, false)

	header.SetBackgroundColor(tcell.ColorDefault)
	status.SetBackgroundColor(tcell.ColorDefault)
	table.SetBackgroundColor(tcell.ColorDefault)
	positions.SetBackgroundColor(tcell.ColorDefault)
	chart.SetBackgroundColor(tcell.ColorDefault)
	footer.SetBackgroundColor(tcell.ColorDefault)
	header.SetBorder(true).SetTitle("Overview")
	status.SetBorder(true).SetTitle("Status")
	table.SetBorder(true).SetTitle("Contracts")
	positions.SetBorder(true).SetTitle("Positions")
	chart.SetBorder(true).SetTitle("1H Chart")
	footer.SetBorder(true)
	footer.SetText("/ or h help | Tab switch panel | Arrows switch chart | i interval | o order book | q / Ctrl+C quit")

	ob, obFrame, obTable := buildOrderBookUI()

	ui := &uiModel{app: app, header: header, status: status, table: table, positions: positions, chart: chart, footer: footer, help: help, cfg: cfg, loc: loc, state: state, changeChart: changeChart, changeInterval: changeInterval, orderBook: ob, orderBookFrame: obFrame, orderBookTable: obTable}
	ui.refresh()
	ui.pages = tview.NewPages().
		AddPage("main", ui.layout(), true, true).
		AddPage("help", help, true, false).
		AddPage("orderbook", ob, true, false)

	app.SetInputCapture(func(event *tcell.EventKey) *tcell.EventKey {
		_, _, _, _, _, _, _, _, _, _, _, _, _, _, _, _, _, _, modalMessage := ui.state.snapshot()
		if modalMessage != "" {
			switch event.Key() {
			case tcell.KeyEsc, tcell.KeyEnter:
				ui.state.clearModal()
				ui.refreshNow()
				return nil
			}
			switch event.Rune() {
			case 'q', 'Q', 'h', 'H', '/':
				ui.state.clearModal()
				ui.refreshNow()
				return nil
			}
			return nil
		}
		if ui.helpOpen {
			switch event.Key() {
			case tcell.KeyEsc, tcell.KeyEnter:
				ui.hideHelp()
				return nil
			case tcell.KeyTAB:
				ui.hideHelp()
				ui.togglePanel()
				return nil
			}
			switch event.Rune() {
			case 'h', 'H', '/', 'q', 'Q':
				ui.hideHelp()
				return nil
			}
			return nil
		}
		if ui.orderBookOpen {
			switch event.Key() {
			case tcell.KeyEsc:
				ui.hideOrderBook()
				return nil
			case tcell.KeyUp, tcell.KeyLeft:
				if ui.changeChart != nil {
					ui.changeChart(-1)
				}
				ui.restartOrderBook()
				return nil
			case tcell.KeyDown, tcell.KeyRight:
				if ui.changeChart != nil {
					ui.changeChart(1)
				}
				ui.restartOrderBook()
				return nil
			}
			switch event.Rune() {
			case 'o', 'O':
				ui.hideOrderBook()
				return nil
			case 'r', 'R':
				go ui.doOrderBookFetch(context.Background(), ui.orderBookBaseURL, ui.orderBookSymbol)
				return nil
			}
			return nil
		}

		switch event.Key() {
		case tcell.KeyCtrlC:
			app.Stop()
			return nil
		case tcell.KeyUp, tcell.KeyLeft:
			if ui.changeChart != nil {
				ui.changeChart(-1)
			}
			return nil
		case tcell.KeyDown, tcell.KeyRight:
			if ui.changeChart != nil {
				ui.changeChart(1)
			}
			return nil
		case tcell.KeyTAB:
			ui.togglePanel()
			return nil
		}
		switch event.Rune() {
		case 'h', 'H', '/':
			ui.showHelp()
			return nil
		case 'i', 'I':
			if ui.changeInterval != nil {
				go func() {
					ui.changeInterval()
					ui.requestDraw()
				}()
			}
			return nil
		case 'o', 'O':
			ui.showOrderBook()
			return nil
		case 'q', 'Q':
			app.Stop()
			return nil
		}
		return event
	})

	return ui
}

func getChartSymbolForPanel(state *appState, panel panelMode) string {
	state.mu.RLock()
	defer state.mu.RUnlock()
	if panel == panelSpot {
		return state.spotChartSymbol
	}
	return state.futuresChartSymbol
}

func (ui *uiModel) togglePanel() {
	_, _, _, _, _, _, _, _, _, _, _, _, _, _, _, _, _, panel, _ := ui.state.snapshot()
	switch panel {
	case panelFutures:
		if len(ui.cfg.SpotSymbols) == 0 {
			ui.state.setModal("Spot panel is not configured")
			ui.refreshNow()
			return
		}
		ui.state.setPanel(panelSpot)
		if chartSymbol := getChartSymbolForPanel(ui.state, panelSpot); chartSymbol != "" {
			_ = loadChartHistoryForSymbol(context.Background(), &http.Client{Timeout: ui.cfg.Timeout}, defaultSpotRESTBaseURL, panelSpot, chartSymbol, ui.cfg.ChartLimit, ui.state)
		}
	case panelSpot:
		if len(ui.cfg.Symbols) == 0 {
			ui.state.setModal("Futures panel is not configured")
			ui.refreshNow()
			return
		}
		ui.state.setPanel(panelFutures)
		if chartSymbol := getChartSymbolForPanel(ui.state, panelFutures); chartSymbol != "" {
			_ = loadChartHistoryForSymbol(context.Background(), &http.Client{Timeout: ui.cfg.Timeout}, ui.cfg.RESTBase, panelFutures, chartSymbol, ui.cfg.ChartLimit, ui.state)
		}
	default:
		if len(ui.cfg.Symbols) > 0 {
			ui.state.setPanel(panelFutures)
		} else if len(ui.cfg.SpotSymbols) > 0 {
			ui.state.setPanel(panelSpot)
		}
	}
	ui.refreshNow()
}

func (ui *uiModel) layout() tview.Primitive {
	left := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(ui.table, 0, 4, false).
		AddItem(ui.positions, 0, 5, false)
	content := tview.NewFlex().SetDirection(tview.FlexRow).
		AddItem(ui.header, 6, 0, false).
		AddItem(ui.status, 5, 0, false).
		AddItem(tview.NewFlex().SetDirection(tview.FlexColumn).AddItem(left, 0, 3, false).AddItem(ui.chart, 0, 2, false), 0, 1, false).
		AddItem(ui.footer, 3, 0, false)
	return content
}

func (ui *uiModel) root() tview.Primitive {
	return ui.pages
}

func (ui *uiModel) showHelp() {
	ui.helpOpen = true
	ui.pages.ShowPage("help")
	ui.app.SetFocus(ui.help)
}

func (ui *uiModel) hideHelp() {
	ui.helpOpen = false
	ui.pages.HidePage("help")
	ui.app.SetFocus(ui.chart)
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

func (ui *uiModel) refreshNow() {
	ui.refresh()
}

func (ui *uiModel) refresh() {
	rows, spotRows, chart, positions, spotBalances, chartSymbol, chartInterval, lastError, spotError, accountError, spotAccountError, startedAt, lastUpdate, spotLastUpdate, accountLastUpdate, spotAccountLastUpdate, accountEnabled, panel, modalMessage := ui.state.snapshot()

	accountMode := "disabled"
	if accountEnabled {
		accountMode = "enabled | stream"
	}
	panelName := string(panel)
	if panelName == "" {
		panelName = string(panelFutures)
	}
	marketStatus := lastError
	marketUpdate := lastUpdate
	accountStatusError := accountError
	accountUpdate := accountLastUpdate
	if panel == panelSpot {
		marketStatus = spotError
		marketUpdate = spotLastUpdate
		accountStatusError = spotAccountError
		accountUpdate = spotAccountLastUpdate
	}

	ui.header.SetText(fmt.Sprintf(
		"panel: %s\nfutures: %s\nspot: %s\nconfig: %s\nnow: %s\nstarted: %s\nmarket update: %s",
		panelName,
		strings.Join(ui.cfg.Symbols, ","),
		strings.Join(ui.cfg.SpotSymbols, ","),
		ui.cfg.ConfigPath,
		formatTime(time.Now(), ui.loc, false),
		formatTime(startedAt, ui.loc, false),
		formatOptionalTime(marketUpdate, ui.loc),
	))

	statusText := "[green]ok[-]"
	if marketStatus != "" {
		statusText = ui.colorize("red", marketStatus)
	}
	accountStatus := ui.accountStatusText(accountEnabled, accountStatusError, accountUpdate)
	transport := fmt.Sprintf("retry delay=%s | futures ws=%s | futures rest=%s | spot ws=%s | spot rest=%s", ui.cfg.RetryDelay, ui.cfg.WSBase, ui.cfg.RESTBase, defaultSpotWSBaseURL, defaultSpotRESTBaseURL)
	ui.status.SetText(fmt.Sprintf("market: %s\naccount: %s\nmode: %s\n%s", statusText, accountStatus, accountMode, transport))

	if panel == panelSpot {
		ui.table.SetTitle("Spot")
		ui.positions.SetTitle("Spot Balances")
		ui.renderSpotTable(spotRows)
		ui.renderSpotBalances(spotBalances, spotAccountError)
		ui.renderChart(chart, chartSymbol, chartInterval)
	} else {
		ui.table.SetTitle("Contracts")
		ui.positions.SetTitle("Positions")
		ui.renderTable(rows)
		ui.renderPositions(positions, accountEnabled, accountError, accountLastUpdate)
		ui.renderChart(chart, chartSymbol, chartInterval)
	}

	if modalMessage != "" {
		ui.chart.SetTitle("Notice")
		ui.chart.SetText(modalMessage + "\n\nPress Esc or Enter to close")
	}
}

func (ui *uiModel) accountStatusText(accountEnabled bool, accountError string, accountLastUpdate time.Time) string {
	if !accountEnabled {
		return ui.colorize("gray", "disabled")
	}
	if accountError != "" {
		return ui.colorize("red", accountError)
	}
	if accountLastUpdate.IsZero() {
		return ui.colorize("yellow", "waiting for initial sync")
	}
	return ui.colorize("green", fmt.Sprintf("ok | last sync %s", formatOptionalTime(accountLastUpdate, ui.loc)))
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

func (ui *uiModel) renderPositions(positions []positionState, accountEnabled bool, accountError string, accountLastUpdate time.Time) {
	ui.positions.Clear()
	headers := []string{"SYMBOL", "SIDE", "SIZE", "ENTRY", "MARK", "UPNL", "LIQ", "MODE"}
	for col, header := range headers {
		cell := tview.NewTableCell(header).
			SetSelectable(false).
			SetAttributes(tcell.AttrBold).
			SetBackgroundColor(tcell.ColorDefault)
		if !ui.cfg.NoColor {
			cell.SetTextColor(tcell.ColorYellow)
		}
		ui.positions.SetCell(0, col, cell)
	}

	if !accountEnabled {
		ui.positions.SetCell(1, 0, tview.NewTableCell("API credentials not configured").SetSelectable(false).SetExpansion(1).SetBackgroundColor(tcell.ColorDefault))
		return
	}

	if accountError != "" && len(positions) == 0 {
		ui.positions.SetCell(1, 0, tview.NewTableCell(ui.colorize("red", accountError)).SetSelectable(false).SetExpansion(1).SetBackgroundColor(tcell.ColorDefault))
		return
	}

	if len(positions) == 0 {
		message := "No open positions"
		if !accountLastUpdate.IsZero() {
			message = fmt.Sprintf("No open positions | last sync %s", formatOptionalTime(accountLastUpdate, ui.loc))
		}
		ui.positions.SetCell(1, 0, tview.NewTableCell(message).SetSelectable(false).SetExpansion(1).SetBackgroundColor(tcell.ColorDefault))
		return
	}

	for i, position := range positions {
		mode := strings.ToLower(position.MarginType)
		if position.Leverage != "" {
			mode = fmt.Sprintf("%s %sx", mode, position.Leverage)
		}

		ui.positions.SetCell(i+1, 0, tview.NewTableCell(position.Symbol).SetSelectable(false).SetBackgroundColor(tcell.ColorDefault))
		ui.positions.SetCell(i+1, 1, tview.NewTableCell(ui.colorBySide(position.Side, position.Side)).SetSelectable(false).SetBackgroundColor(tcell.ColorDefault))
		ui.positions.SetCell(i+1, 2, tview.NewTableCell(formatCompactFloat(position.Size)).SetSelectable(false).SetBackgroundColor(tcell.ColorDefault))
		ui.positions.SetCell(i+1, 3, tview.NewTableCell(formatCompactFloat(position.EntryPrice)).SetSelectable(false).SetBackgroundColor(tcell.ColorDefault))
		ui.positions.SetCell(i+1, 4, tview.NewTableCell(formatCompactFloat(position.MarkPrice)).SetSelectable(false).SetBackgroundColor(tcell.ColorDefault))
		ui.positions.SetCell(i+1, 5, tview.NewTableCell(ui.colorByChange(compareFloat(position.UnrealizedPnL, 0), formatSignedCompactFloat(position.UnrealizedPnL))).SetSelectable(false).SetBackgroundColor(tcell.ColorDefault))
		ui.positions.SetCell(i+1, 6, tview.NewTableCell(formatOptionalCompactFloat(position.LiquidationPrice)).SetSelectable(false).SetBackgroundColor(tcell.ColorDefault))
		ui.positions.SetCell(i+1, 7, tview.NewTableCell(mode).SetSelectable(false).SetBackgroundColor(tcell.ColorDefault))
	}
}

func (ui *uiModel) renderSpotTable(rows []rowState) {
	ui.table.Clear()
	headers := []string{"SYMBOL", "PRICE", "DELTA", "EXCHANGE_TIME", "LOCAL_UPDATE"}
	for col, header := range headers {
		cell := tview.NewTableCell(header).SetSelectable(false).SetAttributes(tcell.AttrBold).SetBackgroundColor(tcell.ColorDefault)
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
		ui.table.SetCell(i+1, 0, tview.NewTableCell(row.Symbol).SetSelectable(false).SetBackgroundColor(tcell.ColorDefault))
		ui.table.SetCell(i+1, 1, tview.NewTableCell(ui.colorByChange(row.Change, price)).SetSelectable(false).SetBackgroundColor(tcell.ColorDefault))
		ui.table.SetCell(i+1, 2, tview.NewTableCell(ui.colorByChange(row.Change, formatDelta(row))).SetSelectable(false).SetBackgroundColor(tcell.ColorDefault))
		ui.table.SetCell(i+1, 3, tview.NewTableCell(formatEpoch(row.ExchangeTime, ui.loc)).SetSelectable(false).SetBackgroundColor(tcell.ColorDefault))
		ui.table.SetCell(i+1, 4, tview.NewTableCell(formatOptionalTime(row.LocalTime, ui.loc)).SetSelectable(false).SetBackgroundColor(tcell.ColorDefault))
	}
}

func (ui *uiModel) renderSpotBalances(balances []spotBalance, spotAccountError string) {
	ui.positions.Clear()
	headers := []string{"ASSET", "FREE", "LOCKED", "TOTAL", "USDT", "PRICE"}
	for col, header := range headers {
		cell := tview.NewTableCell(header).SetSelectable(false).SetAttributes(tcell.AttrBold).SetBackgroundColor(tcell.ColorDefault)
		if !ui.cfg.NoColor {
			cell.SetTextColor(tcell.ColorYellow)
		}
		ui.positions.SetCell(0, col, cell)
	}
	if spotAccountError != "" && len(balances) == 0 {
		ui.positions.SetCell(1, 0, tview.NewTableCell(ui.colorize("red", spotAccountError)).SetSelectable(false).SetExpansion(1).SetBackgroundColor(tcell.ColorDefault))
		return
	}
	if len(balances) == 0 {
		ui.positions.SetCell(1, 0, tview.NewTableCell("No configured spot balances").SetSelectable(false).SetExpansion(1).SetBackgroundColor(tcell.ColorDefault))
		return
	}
	for i, balance := range balances {
		ui.positions.SetCell(i+1, 0, tview.NewTableCell(balance.Asset).SetSelectable(false).SetBackgroundColor(tcell.ColorDefault))
		ui.positions.SetCell(i+1, 1, tview.NewTableCell(formatCompactFloat(balance.Free)).SetSelectable(false).SetBackgroundColor(tcell.ColorDefault))
		ui.positions.SetCell(i+1, 2, tview.NewTableCell(formatCompactFloat(balance.Locked)).SetSelectable(false).SetBackgroundColor(tcell.ColorDefault))
		ui.positions.SetCell(i+1, 3, tview.NewTableCell(formatCompactFloat(balance.Total)).SetSelectable(false).SetBackgroundColor(tcell.ColorDefault))
		ui.positions.SetCell(i+1, 4, tview.NewTableCell(balance.QuoteValueText).SetSelectable(false).SetBackgroundColor(tcell.ColorDefault))
		ui.positions.SetCell(i+1, 5, tview.NewTableCell(formatOptionalCompactFloat(balance.PriceValue)).SetSelectable(false).SetBackgroundColor(tcell.ColorDefault))
	}
}

func buildSpotSummary(balances []spotBalance) string {
	if len(balances) == 0 {
		return "waiting for spot balances..."
	}

	var total float64
	for _, balance := range balances {
		total += balance.QuoteValue
	}

	var b strings.Builder
	b.WriteString(fmt.Sprintf("assets %d\n", len(balances)))
	b.WriteString(fmt.Sprintf("total usdt %s\n\n", formatCompactFloat(total)))
	for _, balance := range balances {
		b.WriteString(fmt.Sprintf("%-6s total %-12s usdt %-12s\n", balance.Asset, formatCompactFloat(balance.Total), balance.QuoteValueText))
	}
	return b.String()
}

func (ui *uiModel) renderChart(candles []klineCandle, symbol, interval string) {
	if symbol == "" {
		symbol = ui.cfg.ChartSymbol
	}
	if interval == "" {
		interval = defaultChartInterval
	}
	ui.chart.SetTitle(fmt.Sprintf("%s Chart - %s", strings.ToUpper(interval), symbol))
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

func (ui *uiModel) colorBySide(side, text string) string {
	if ui.cfg.NoColor {
		return text
	}
	switch strings.ToUpper(strings.TrimSpace(side)) {
	case "LONG":
		return fmt.Sprintf("[%s]%s[-]", bullColorTag, escapeTView(text))
	case "SHORT":
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
	rows, spotRows, chart, positions, spotBalances, chartSymbol, chartInterval, lastError, spotError, accountError, spotAccountError, startedAt, lastUpdate, spotLastUpdate, accountLastUpdate, spotAccountLastUpdate, accountEnabled, panel, _ := state.snapshot()

	fmt.Printf("mode: ws\npanel: %s\nfutures symbols: %s\nspot symbols: %s\nconfig: %s\nstarted: %s\nfutures update: %s\nspot update: %s\n",
		panel,
		strings.Join(cfg.Symbols, ","),
		strings.Join(cfg.SpotSymbols, ","),
		cfg.ConfigPath,
		formatTime(startedAt, loc, false),
		formatOptionalTime(lastUpdate, loc),
		formatOptionalTime(spotLastUpdate, loc),
	)
	if lastError == "" {
		fmt.Println("futures status: ok")
	} else {
		fmt.Printf("futures status: %s\n", lastError)
	}
	if spotError == "" {
		fmt.Println("spot status: ok")
	} else {
		fmt.Printf("spot status: %s\n", spotError)
	}
	if accountEnabled {
		if accountError == "" {
			fmt.Printf("futures account: ok | last sync: %s\n", formatOptionalTime(accountLastUpdate, loc))
		} else {
			fmt.Printf("futures account: %s\n", accountError)
		}
		if spotAccountError == "" {
			fmt.Printf("spot account: ok | last sync: %s\n", formatOptionalTime(spotAccountLastUpdate, loc))
		} else {
			fmt.Printf("spot account: %s\n", spotAccountError)
		}
	} else {
		fmt.Println("account: disabled")
	}
	if len(rows) > 0 {
		fmt.Printf("\n%-14s %-18s %-18s %-26s %-26s\n", "SYMBOL", "PRICE", "DELTA", "EXCHANGE_TIME", "LOCAL_UPDATE")
		for _, row := range rows {
			price := row.Price
			if price == "" {
				price = "-"
			}
			fmt.Printf("%-14s %-18s %-18s %-26s %-26s\n", row.Symbol, price, formatDelta(row), formatEpoch(row.ExchangeTime, loc), formatOptionalTime(row.LocalTime, loc))
		}
	}
	if len(spotRows) > 0 {
		fmt.Println("\nSPOT")
		fmt.Printf("%-14s %-18s %-18s %-26s %-26s\n", "SYMBOL", "PRICE", "DELTA", "EXCHANGE_TIME", "LOCAL_UPDATE")
		for _, row := range spotRows {
			price := row.Price
			if price == "" {
				price = "-"
			}
			fmt.Printf("%-14s %-18s %-18s %-26s %-26s\n", row.Symbol, price, formatDelta(row), formatEpoch(row.ExchangeTime, loc), formatOptionalTime(row.LocalTime, loc))
		}
	}
	if len(positions) > 0 {
		fmt.Println("\nPOSITIONS")
		fmt.Printf("%-12s %-8s %-12s %-12s %-12s %-12s %-12s %-12s\n", "SYMBOL", "SIDE", "SIZE", "ENTRY", "MARK", "UPNL", "LIQ", "MODE")
		for _, position := range positions {
			mode := strings.ToLower(position.MarginType)
			if position.Leverage != "" {
				mode = fmt.Sprintf("%s %sx", mode, position.Leverage)
			}
			fmt.Printf("%-12s %-8s %-12s %-12s %-12s %-12s %-12s %-12s\n", position.Symbol, position.Side, formatCompactFloat(position.Size), formatCompactFloat(position.EntryPrice), formatCompactFloat(position.MarkPrice), formatSignedCompactFloat(position.UnrealizedPnL), formatOptionalCompactFloat(position.LiquidationPrice), mode)
		}
	}
	if len(spotBalances) > 0 {
		fmt.Println("\nSPOT BALANCES")
		fmt.Printf("%-10s %-14s %-14s %-14s %-14s %-14s\n", "ASSET", "FREE", "LOCKED", "TOTAL", "USDT", "PRICE")
		for _, balance := range spotBalances {
			fmt.Printf("%-10s %-14s %-14s %-14s %-14s %-14s\n", balance.Asset, formatCompactFloat(balance.Free), formatCompactFloat(balance.Locked), formatCompactFloat(balance.Total), balance.QuoteValueText, formatOptionalCompactFloat(balance.PriceValue))
		}
	}
	if len(chart) > 0 {
		fmt.Printf("\n%s chart (%s):\n%s\n", strings.ToUpper(chartInterval), chartSymbol, stripTViewTags(buildChartText(chart, true)))
	}
}
