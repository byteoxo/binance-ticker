package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

const (
	// Binance Futures
	defaultWSBaseURL        = "wss://fstream.binance.com"
	defaultRESTBaseURL      = "https://fapi.binance.com"
	futuresKlinePath        = "/fapi/v1/klines"
	positionRiskPath        = "/fapi/v3/positionRisk"
	listenKeyPath           = "/fapi/v1/listenKey"
	futuresDepthPath        = "/fapi/v1/depth"
	// Binance Spot
	defaultSpotWSBaseURL    = "wss://stream.binance.com:9443"
	defaultSpotRESTBaseURL  = "https://api.binance.com"
	spotKlinePath           = "/api/v3/klines"
	spotAccountPath         = "/api/v3/account"
	defaultSpotWSAPIBaseURL = "wss://ws-api.binance.com:443/ws-api/v3"
	spotDepthPath           = "/api/v3/depth"
	// Gate.io Futures
	defaultGateWSBaseURL      = "wss://fx-ws.gateio.ws/v4/ws/usdt"
	defaultGateRESTBaseURL    = "https://fx-api.gateio.ws"
	// Gate.io Spot
	defaultGateSpotWSBaseURL  = "wss://api.gateio.ws/ws/v4/"
	defaultGateSpotRESTBaseURL = "https://api.gateio.ws"

	defaultTimeout            = 8 * time.Second
	userDataKeepaliveInterval = 50 * time.Minute
	uiRefreshInterval         = time.Second
	defaultChartLimit         = 48
	defaultChartHeight        = 12
	chartCandleWidth          = 1
	chartCandleGap            = 1
	chartStride               = chartCandleWidth + chartCandleGap
	bullColorTag              = "#00c853"
	bearColorTag              = "#e53935"
	neutralColorTag           = "#9aa0a6"
	orderBookLimit            = 20
	orderBookRefreshInterval  = time.Second
	defaultChartInterval      = "1h"
	sparklineHistory          = 20
	fundingRateRefreshInterval = 60 * time.Second
	marketStatsRefreshInterval = 30 * time.Second
	defaultVolumeHeight       = 4
)

var chartIntervals = []string{"1h", "2h", "4h", "1d", "3d"}

func spotRESTBaseURL(cfg config) string {
	if cfg.isGate() {
		return defaultGateSpotRESTBaseURL
	}
	return defaultSpotRESTBaseURL
}

func spotWSBaseURL(cfg config) string {
	if cfg.isGate() {
		return defaultGateSpotWSBaseURL
	}
	return defaultSpotWSBaseURL
}

func main() {
	log.SetFlags(0)
	log.SetPrefix("")

	cfg := mustLoadConfig()
	loc := mustLoadLocation(cfg.TZ)
	client := &http.Client{Timeout: cfg.Timeout}
	state := newAppState(cfg)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, client, cfg, loc, state); err != nil {
		log.Fatalf("fatal: %v", err)
	}
}

func run(ctx context.Context, client *http.Client, cfg config, loc *time.Location, state *appState) error {
	if len(cfg.Symbols) > 0 {
		if err := loadChartHistory(ctx, client, cfg, state); err != nil {
			state.setError(fmt.Sprintf("chart init failed: %v", err))
		}
	}
	if cfg.hasAccountAuth() && len(cfg.Symbols) > 0 && !cfg.isGate() {
		if err := loadInitialPositions(ctx, client, cfg, state); err != nil {
			state.setAccountError(fmt.Sprintf("positions init failed: %v", err))
		}
	}
	spotRESTBase := spotRESTBaseURL(cfg)
	spotWSBase := spotWSBaseURL(cfg)
	_ = spotWSBase

	if cfg.hasSpot() {
		state.setSpotRows(spotSymbolsToTickers(cfg.SpotSymbols))
		if err := loadInitialSpotBalances(ctx, client, cfg, state); err != nil {
			state.setSpotAccountError(fmt.Sprintf("spot balances init failed: %v", err))
		}
		if len(cfg.SpotSymbols) > 0 {
			spotTickers := spotSymbolsToTickers(cfg.SpotSymbols)
			if len(spotTickers) > 0 && (cfg.DefaultPanel == string(panelSpot) || len(cfg.Symbols) == 0) {
				if err := loadChartHistoryForSymbol(ctx, client, spotRESTBase, panelSpot, spotTickers[0], cfg.ChartLimit, state); err != nil {
					state.setSpotError(fmt.Sprintf("spot chart init failed: %v", err))
				}
			}
		}
	}

	getChartSymbol := func() string {
		_, _, _, _, _, chartSymbol, _, _, _, _, _, _, _, _, _, _, _, _, _ := state.snapshot()
		return chartSymbol
	}
	getChartInterval := state.getChartInterval
	setChartSymbol := func(panel panelMode, symbol string) {
		state.mu.Lock()
		defer state.mu.Unlock()
		if panel == panelSpot {
			state.spotChartSymbol = symbol
		} else {
			state.futuresChartSymbol = symbol
		}
	}
	getTickerSymbols := func() []string {
		_, _, _, positions, _, _, _, _, _, _, _, _, _, _, _, _, _, _, _ := state.snapshot()
		combined := make([]string, 0, len(cfg.Symbols)+len(positions))
		combined = append(combined, cfg.Symbols...)
		for _, position := range positions {
			combined = append(combined, position.Symbol)
		}
		return normalizeSymbolList(combined)
	}
	getSpotTickerSymbols := func() []string {
		return spotSymbolsToTickers(cfg.SpotSymbols)
	}

	changeInterval := func() {
		current := state.getChartInterval()
		idx := 0
		for i, iv := range chartIntervals {
			if iv == current {
				idx = i
				break
			}
		}
		next := chartIntervals[(idx+1)%len(chartIntervals)]
		state.setChartInterval(next)
		// Clear both charts so stale candles from old interval are not shown.
		state.mu.Lock()
		state.futuresChart = nil
		state.spotChart = nil
		state.mu.Unlock()
		// Reload history for the active panel's current symbol.
		_, _, _, _, _, _, _, _, _, _, _, _, _, _, _, _, _, panel, _ := state.snapshot()
		switch panel {
		case panelSpot:
			if sym := getChartSymbolForPanel(state, panelSpot); sym != "" {
				if err := loadChartHistoryForSymbol(ctx, client, spotRESTBase, panelSpot, sym, cfg.ChartLimit, state); err != nil {
					state.setSpotError(fmt.Sprintf("chart interval switch failed: %v", err))
				}
			}
		default:
			if sym := getChartSymbolForPanel(state, panelFutures); sym != "" {
				if err := loadChartHistoryForSymbol(ctx, client, cfg.RESTBase, panelFutures, sym, cfg.ChartLimit, state); err != nil {
					state.setError(fmt.Sprintf("chart interval switch failed: %v", err))
				}
			}
		}
	}

	changeChart := func(offset int) {
		_, _, _, _, _, _, _, _, _, _, _, _, _, _, _, _, _, panel, _ := state.snapshot()

		var symbols []string
		var baseURL string
		var waitingMessage string
		var switchMessage string
		var clearErr func()
		var setErr func(string)

		switch panel {
		case panelSpot:
			symbols = spotSymbolsToTickers(cfg.SpotSymbols)
			baseURL = spotRESTBase
			waitingMessage = "Spot panel is not configured"
			switchMessage = "switching spot chart to %s..."
			clearErr = state.clearSpotError
			setErr = state.setSpotError
		default:
			symbols = cfg.Symbols
			baseURL = cfg.RESTBase
			waitingMessage = "Futures is not configured"
			switchMessage = "switching chart to %s..."
			clearErr = state.clearError
			setErr = state.setError
		}

		if len(symbols) == 0 {
			state.setModal(waitingMessage)
			return
		}

		current := getChartSymbol()
		idx := indexOfSymbol(symbols, current)
		if idx < 0 {
			idx = 0
		}
		next := symbols[(idx+offset+len(symbols))%len(symbols)]
		if next == current {
			return
		}

		setChartSymbol(panel, next)
		setErr(fmt.Sprintf(switchMessage, next))
		if err := loadChartHistoryForSymbol(ctx, client, baseURL, panel, next, cfg.ChartLimit, state); err != nil {
			setErr(fmt.Sprintf("chart switch failed: %v", err))
			return
		}
		clearErr()
	}

	ui := newUI(cfg, loc, state, changeChart, changeInterval)
	errCh := make(chan error, 1)

	go func() {
		<-ctx.Done()
		ui.app.QueueUpdateDraw(func() {
			ui.app.Stop()
		})
	}()

	go func() {
		err := runWSLoop(ctx, cfg, state, ui.requestDraw, getChartSymbol, getChartInterval, getTickerSymbols, isSpotTickerSymbolFunc(cfg))
		if err != nil {
			select {
			case errCh <- err:
			default:
			}
		}
	}()

	if len(cfg.Symbols) > 0 {
		if cfg.isGate() {
			go runGateFundingRateLoop(ctx, client, cfg, state, ui.requestDraw)
			go runGateMarketStatsLoop(ctx, client, cfg, state, ui.requestDraw)
		} else {
			go runFundingRateLoop(ctx, client, cfg, state, ui.requestDraw)
			go runMarketStatsLoop(ctx, client, cfg, state, ui.requestDraw)
		}
	}
	if cfg.hasAccountAuth() && len(cfg.Symbols) > 0 && !cfg.isGate() {
		go runUserDataLoop(ctx, client, cfg, state, ui.requestDraw)
	}
	if cfg.hasAccountAuth() && cfg.hasSpot() && !cfg.isGate() {
		go runSpotUserDataLoop(ctx, client, cfg, state, ui.requestDraw)
	}
	if cfg.hasSpot() {
		go func() {
			err := runSpotWSLoop(ctx, cfg, state, ui.requestDraw, getSpotTickerSymbols, spotWSBase)
			if err != nil {
				select {
				case errCh <- err:
				default:
				}
			}
		}()
	}

	go ui.runClock(ctx)

	if err := ui.app.SetRoot(ui.root(), true).Run(); err != nil {
		return err
	}

	select {
	case err := <-errCh:
		return err
	default:
		return nil
	}
}
