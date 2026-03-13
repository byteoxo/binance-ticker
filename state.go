package main

import (
	"math"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

type rowState struct {
	Symbol       string
	Price        string
	PriceValue   float64
	ExchangeTime int64
	LocalTime    time.Time
	PrevValue    float64
	HasPrev      bool
	Change       int
	Delta        float64
	DeltaPct     float64
	Updates      int
	Status       string
	// 24h stats
	Change24h    float64
	ChangePct24h float64
	High24h      float64
	Low24h       float64
	Volume24h    float64
	// price history for sparkline (last N closes)
	PriceHistory []float64
}

type klineCandle struct {
	Symbol     string
	OpenTime   int64
	CloseTime  int64
	Open       string
	High       string
	Low        string
	Close      string
	OpenValue  float64
	HighValue  float64
	LowValue   float64
	CloseValue float64
	Volume     float64
	Closed     bool
}

type priceTicker struct {
	Symbol       string `json:"symbol"`
	Price        string `json:"price"`
	Time         int64  `json:"time"`
	Change24h    float64
	ChangePct24h float64
	High24h      float64
	Low24h       float64
	Volume24h    float64
}

type positionRiskResponse struct {
	Symbol           string `json:"symbol"`
	PositionAmt      string `json:"positionAmt"`
	EntryPrice       string `json:"entryPrice"`
	MarkPrice        string `json:"markPrice"`
	UnrealizedProfit string `json:"unRealizedProfit"`
	LiquidationPrice string `json:"liquidationPrice"`
	MarginType       string `json:"marginType"`
	PositionSide     string `json:"positionSide"`
	Leverage         string `json:"leverage"`
	UpdateTime       int64  `json:"updateTime"`
}

type positionState struct {
	Symbol           string
	Side             string
	Size             float64
	EntryPrice       float64
	MarkPrice        float64
	UnrealizedPnL    float64
	LiquidationPrice float64
	MarginType       string
	Leverage         string
	UpdateTime       int64
}

type positionUpdate struct {
	Symbol        string
	Side          string
	Size          float64
	EntryPrice    float64
	UnrealizedPnL float64
	MarginType    string
	UpdateTime    int64
	Remove        bool
}

type spotBalance struct {
	Asset          string
	Free           float64
	Locked         float64
	Total          float64
	QuoteValue     float64
	QuoteValueText string
	PriceSymbol    string
	PriceValue     float64
	LocalUpdate    time.Time
}

type spotAccountResponse struct {
	Balances []spotBalancePayload `json:"balances"`
}

type spotBalancePayload struct {
	Asset  string `json:"asset"`
	Free   string `json:"free"`
	Locked string `json:"locked"`
}

type panelMode string

const (
	panelFutures panelMode = "futures"
	panelSpot    panelMode = "spot"
)

type fundingRate struct {
	Symbol          string
	MarkPrice       float64
	IndexPrice      float64
	LastFundingRate float64
	NextFundingTime int64
}

type appState struct {
	mu                    sync.RWMutex
	rows                  map[string]rowState
	spotRows              map[string]rowState
	futuresChart          []klineCandle
	spotChart             []klineCandle
	positions             []positionState
	spotBalances          []spotBalance
	fundingRates          map[string]fundingRate
	futuresChartSymbol    string
	spotChartSymbol       string
	chartInterval         string
	panel                 panelMode
	modalMessage          string
	startedAt             time.Time
	lastError             string
	spotError             string
	accountError          string
	spotAccountError      string
	lastUpdate            time.Time
	spotLastUpdate        time.Time
	accountLastUpdate     time.Time
	spotAccountLastUpdate time.Time
	accountEnabled        bool
}

func newAppState(cfg config) *appState {
	rows := make(map[string]rowState, len(cfg.Symbols))
	for _, symbol := range cfg.Symbols {
		rows[symbol] = rowState{Symbol: symbol, Status: "waiting"}
	}
	spotRows := make(map[string]rowState, len(cfg.SpotSymbols))
	spotTickers := spotSymbolsToTickers(cfg.SpotSymbols)
	for _, symbol := range spotTickers {
		spotRows[symbol] = rowState{Symbol: symbol, Status: "waiting"}
	}
	panel := panelMode(cfg.DefaultPanel)
	if panel == "" {
		panel = panelFutures
	}

	futuresChartSymbol := ""
	if len(cfg.Symbols) > 0 {
		futuresChartSymbol = cfg.ChartSymbol
		if futuresChartSymbol == "" {
			futuresChartSymbol = cfg.Symbols[0]
		}
	}
	spotChartSymbol := ""
	if len(spotTickers) > 0 {
		spotChartSymbol = spotTickers[0]
	}

	return &appState{
		rows:               rows,
		spotRows:           spotRows,
		fundingRates:       make(map[string]fundingRate),
		startedAt:          time.Now(),
		accountEnabled:     cfg.hasAccountAuth(),
		panel:              panel,
		futuresChartSymbol: futuresChartSymbol,
		spotChartSymbol:    spotChartSymbol,
		chartInterval:      defaultChartInterval,
	}
}

func (s *appState) setChart(panel panelMode, symbol string, candles []klineCandle) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if panel == panelSpot {
		s.spotChartSymbol = symbol
		s.spotChart = append([]klineCandle(nil), candles...)
	} else {
		s.futuresChartSymbol = symbol
		s.futuresChart = append([]klineCandle(nil), candles...)
	}
	if !s.lastUpdate.IsZero() {
		return
	}
	s.lastUpdate = time.Now()
}

func (s *appState) applyChartCandle(panel panelMode, candle klineCandle, limit int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit <= 0 {
		limit = defaultChartLimit
	}

	chart := s.futuresChart
	if panel == panelSpot {
		chart = s.spotChart
	}

	if len(chart) > 0 && chart[len(chart)-1].OpenTime == candle.OpenTime {
		chart[len(chart)-1] = candle
	} else {
		chart = append(chart, candle)
		if len(chart) > limit {
			chart = append([]klineCandle(nil), chart[len(chart)-limit:]...)
		}
	}

	if panel == panelSpot {
		s.spotChart = chart
	} else {
		s.futuresChart = chart
	}
	s.lastUpdate = time.Now()
}

func (s *appState) applyTickers(tickers []priceTicker) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ticker := range tickers {
		s.applyTickerLocked(ticker)
	}
	s.lastUpdate = time.Now()
}

func (s *appState) applyTicker(ticker priceTicker) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.applyTickerLocked(ticker)
	s.lastUpdate = time.Now()
}

func (s *appState) applyTickerLocked(ticker priceTicker) {
	current, tracked := s.rows[ticker.Symbol]
	if !tracked {
		if value, err := strconv.ParseFloat(ticker.Price, 64); err == nil {
			s.refreshPositionMarketValueLocked(ticker.Symbol, value)
		}
		return
	}
	current.Symbol = ticker.Symbol
	current.Price = ticker.Price
	current.ExchangeTime = ticker.Time
	current.LocalTime = time.Now()
	current.Status = "ok"
	current.Updates++

	// 24h stats
	if ticker.High24h > 0 {
		current.Change24h = ticker.Change24h
		current.ChangePct24h = ticker.ChangePct24h
		current.High24h = ticker.High24h
		current.Low24h = ticker.Low24h
		current.Volume24h = ticker.Volume24h
	}

	value, err := strconv.ParseFloat(ticker.Price, 64)
	if err == nil {
		if current.Updates > 1 || current.PriceValue != 0 {
			current.PrevValue = current.PriceValue
			current.HasPrev = true
		}
		current.PriceValue = value
		if current.HasPrev {
			current.Delta = value - current.PrevValue
			if current.PrevValue != 0 {
				current.DeltaPct = current.Delta / current.PrevValue * 100
			}
			current.Change = compareFloat(value, current.PrevValue)
		} else {
			current.Change = 0
			current.Delta = 0
			current.DeltaPct = 0
		}
		// maintain price history for sparkline (cap at sparklineHistory)
		current.PriceHistory = append(current.PriceHistory, value)
		if len(current.PriceHistory) > sparklineHistory {
			current.PriceHistory = current.PriceHistory[len(current.PriceHistory)-sparklineHistory:]
		}
		s.refreshPositionMarketValueLocked(ticker.Symbol, value)
	}

	s.rows[ticker.Symbol] = current
}

func compareFloat(a, b float64) int {
	if math.Abs(a-b) < 1e-12 {
		return 0
	}
	if a > b {
		return 1
	}
	return -1
}

func calculatePositionPnL(position positionState) float64 {
	if position.MarkPrice == 0 || position.EntryPrice == 0 || position.Size == 0 {
		return position.UnrealizedPnL
	}

	switch strings.ToUpper(strings.TrimSpace(position.Side)) {
	case "LONG":
		return (position.MarkPrice - position.EntryPrice) * position.Size
	case "SHORT":
		return (position.EntryPrice - position.MarkPrice) * position.Size
	default:
		return position.UnrealizedPnL
	}
}

func (s *appState) setPanel(panel panelMode) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.panel = panel
}

func (s *appState) setModal(message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.modalMessage = message
}

func (s *appState) clearModal() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.modalMessage = ""
}

func (s *appState) setError(message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastError = message
}

func (s *appState) clearError() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastError = ""
}

func (s *appState) setSpotError(message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.spotError = message
}

func (s *appState) clearSpotError() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.spotError = ""
}

func (s *appState) setSpotRows(symbols []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.spotRows = make(map[string]rowState, len(symbols))
	for _, symbol := range symbols {
		s.spotRows[symbol] = rowState{Symbol: symbol, Status: "waiting"}
	}
}

func (s *appState) applySpotTicker(ticker priceTicker) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, tracked := s.spotRows[ticker.Symbol]
	if !tracked {
		return
	}
	current.Symbol = ticker.Symbol
	current.Price = ticker.Price
	current.ExchangeTime = ticker.Time
	current.LocalTime = time.Now()
	current.Status = "ok"
	current.Updates++
	// 24h stats
	if ticker.High24h > 0 {
		current.Change24h = ticker.Change24h
		current.ChangePct24h = ticker.ChangePct24h
		current.High24h = ticker.High24h
		current.Low24h = ticker.Low24h
		current.Volume24h = ticker.Volume24h
	}
	value, err := strconv.ParseFloat(ticker.Price, 64)
	if err == nil {
		if current.Updates > 1 || current.PriceValue != 0 {
			current.PrevValue = current.PriceValue
			current.HasPrev = true
		}
		current.PriceValue = value
		if current.HasPrev {
			current.Delta = value - current.PrevValue
			if current.PrevValue != 0 {
				current.DeltaPct = current.Delta / current.PrevValue * 100
			}
			current.Change = compareFloat(value, current.PrevValue)
		} else {
			current.Change = 0
			current.Delta = 0
			current.DeltaPct = 0
		}
		current.PriceHistory = append(current.PriceHistory, value)
		if len(current.PriceHistory) > sparklineHistory {
			current.PriceHistory = current.PriceHistory[len(current.PriceHistory)-sparklineHistory:]
		}
		for i := range s.spotBalances {
			if s.spotBalances[i].PriceSymbol == ticker.Symbol {
				s.spotBalances[i].PriceValue = value
				s.spotBalances[i].QuoteValue = s.spotBalances[i].Total * value
				s.spotBalances[i].QuoteValueText = formatCompactFloat(s.spotBalances[i].QuoteValue)
				s.spotBalances[i].LocalUpdate = time.Now()
			}
		}
	}
	s.spotRows[ticker.Symbol] = current
	s.spotLastUpdate = time.Now()
}

func (s *appState) setSpotBalances(balances []spotBalance) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.spotBalances = append([]spotBalance(nil), balances...)
	for i := range s.spotBalances {
		if row, ok := s.spotRows[s.spotBalances[i].PriceSymbol]; ok && row.PriceValue > 0 {
			s.spotBalances[i].PriceValue = row.PriceValue
			s.spotBalances[i].QuoteValue = s.spotBalances[i].Total * row.PriceValue
			s.spotBalances[i].QuoteValueText = formatCompactFloat(s.spotBalances[i].QuoteValue)
		}
	}
	s.spotAccountLastUpdate = time.Now()
}

func (s *appState) applySpotBalanceUpdates(updates []spotBalanceUpdate) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(updates) == 0 {
		s.spotAccountLastUpdate = time.Now()
		return
	}

	indexByAsset := make(map[string]int, len(s.spotBalances))
	for i, balance := range s.spotBalances {
		indexByAsset[balance.Asset] = i
	}

	for _, update := range updates {
		if update.Asset == "" {
			continue
		}

		total := update.Free + update.Locked
		if idx, ok := indexByAsset[update.Asset]; ok {
			if update.Remove || total <= 0 {
				s.spotBalances = append(s.spotBalances[:idx], s.spotBalances[idx+1:]...)
				indexByAsset = make(map[string]int, len(s.spotBalances))
				for i, balance := range s.spotBalances {
					indexByAsset[balance.Asset] = i
				}
				continue
			}

			balance := s.spotBalances[idx]
			balance.Free = update.Free
			balance.Locked = update.Locked
			balance.Total = total
			balance.LocalUpdate = time.Now()
			if row, ok := s.spotRows[balance.PriceSymbol]; ok && row.PriceValue > 0 {
				balance.PriceValue = row.PriceValue
				balance.QuoteValue = total * row.PriceValue
				balance.QuoteValueText = formatCompactFloat(balance.QuoteValue)
			} else {
				balance.PriceValue = 0
				balance.QuoteValue = 0
				balance.QuoteValueText = "-"
			}
			s.spotBalances[idx] = balance
			continue
		}

		if update.Remove || total <= 0 {
			continue
		}

		balance := spotBalance{
			Asset:          update.Asset,
			Free:           update.Free,
			Locked:         update.Locked,
			Total:          total,
			PriceSymbol:    spotSymbolToTicker(update.Asset),
			QuoteValueText: "-",
			LocalUpdate:    time.Now(),
		}
		if row, ok := s.spotRows[balance.PriceSymbol]; ok && row.PriceValue > 0 {
			balance.PriceValue = row.PriceValue
			balance.QuoteValue = total * row.PriceValue
			balance.QuoteValueText = formatCompactFloat(balance.QuoteValue)
		}
		s.spotBalances = append(s.spotBalances, balance)
		indexByAsset[balance.Asset] = len(s.spotBalances) - 1
	}

	sort.Slice(s.spotBalances, func(i, j int) bool {
		return s.spotBalances[i].Asset < s.spotBalances[j].Asset
	})
	s.spotAccountLastUpdate = time.Now()
}

func (s *appState) setSpotAccountError(message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.spotAccountError = message
}

func (s *appState) clearSpotAccountError() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.spotAccountError = ""
}

func (s *appState) setPositions(positions []positionState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.positions = append([]positionState(nil), positions...)
	for i := range s.positions {
		if ticker, ok := s.rows[s.positions[i].Symbol]; ok && ticker.PriceValue > 0 {
			s.positions[i].MarkPrice = ticker.PriceValue
			s.positions[i].UnrealizedPnL = calculatePositionPnL(s.positions[i])
		}
	}
	s.accountLastUpdate = time.Now()
}

func (s *appState) refreshPositionMarketValueLocked(symbol string, markPrice float64) {
	for i := range s.positions {
		if s.positions[i].Symbol != symbol {
			continue
		}
		s.positions[i].MarkPrice = markPrice
		s.positions[i].UnrealizedPnL = calculatePositionPnL(s.positions[i])
	}
}

func (s *appState) applyPositionUpdates(updates []positionUpdate) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if len(updates) == 0 {
		s.accountLastUpdate = time.Now()
		return
	}

	indexByKey := make(map[string]int, len(s.positions))
	for i, position := range s.positions {
		indexByKey[position.Symbol+"|"+position.Side] = i
	}

	for _, update := range updates {
		if update.Symbol == "" {
			continue
		}
		key := update.Symbol + "|" + update.Side
		idx, exists := indexByKey[key]
		if update.Remove {
			if exists {
				s.positions = append(s.positions[:idx], s.positions[idx+1:]...)
			} else if update.Side == "" {
				// One-way mode (BOTH): size is 0 so side can't be inferred.
				// Remove all positions with this symbol.
				filtered := s.positions[:0]
				for _, p := range s.positions {
					if p.Symbol != update.Symbol {
						filtered = append(filtered, p)
					}
				}
				s.positions = filtered
			}
			indexByKey = make(map[string]int, len(s.positions))
			for i, position := range s.positions {
				indexByKey[position.Symbol+"|"+position.Side] = i
			}
			continue
		}

		position := positionState{
			Symbol:        update.Symbol,
			Side:          update.Side,
			Size:          update.Size,
			EntryPrice:    update.EntryPrice,
			UnrealizedPnL: update.UnrealizedPnL,
			MarginType:    update.MarginType,
			UpdateTime:    update.UpdateTime,
		}

		if exists {
			current := s.positions[idx]
			position.MarkPrice = current.MarkPrice
			position.LiquidationPrice = current.LiquidationPrice
			position.Leverage = current.Leverage
			if position.MarginType == "" {
				position.MarginType = current.MarginType
			}
			if position.MarkPrice > 0 {
				position.UnrealizedPnL = calculatePositionPnL(position)
			}
			s.positions[idx] = position
		} else {
			s.positions = append(s.positions, position)
			indexByKey[key] = len(s.positions) - 1
		}
	}

	sort.Slice(s.positions, func(i, j int) bool {
		if s.positions[i].Symbol == s.positions[j].Symbol {
			return s.positions[i].Side < s.positions[j].Side
		}
		return s.positions[i].Symbol < s.positions[j].Symbol
	})
	for _, row := range s.positions {
		if ticker, ok := s.rows[row.Symbol]; ok {
			positionMark := row.MarkPrice
			if positionMark == 0 {
				positionMark = ticker.PriceValue
			}
			if positionMark > 0 {
				for i := range s.positions {
					if s.positions[i].Symbol == row.Symbol && s.positions[i].Side == row.Side {
						s.positions[i].MarkPrice = positionMark
					}
				}
			}
		}
	}

	s.accountLastUpdate = time.Now()
}

func (s *appState) setAccountError(message string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.accountError = message
}

func (s *appState) clearAccountError() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.accountError = ""
}

func (s *appState) setFundingRates(rates []fundingRate) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range rates {
		s.fundingRates[r.Symbol] = r
	}
}

func (s *appState) getFundingRate(symbol string) (fundingRate, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.fundingRates[symbol]
	return r, ok
}

func (s *appState) getChartInterval() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.chartInterval == "" {
		return defaultChartInterval
	}
	return s.chartInterval
}

func (s *appState) setChartInterval(interval string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.chartInterval = interval
}

func (s *appState) snapshot() ([]rowState, []rowState, []klineCandle, []positionState, []spotBalance, string, string, string, string, string, string, time.Time, time.Time, time.Time, time.Time, time.Time, bool, panelMode, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	rows := make([]rowState, 0, len(s.rows))
	for _, row := range s.rows {
		rows = append(rows, row)
	}
	sort.Slice(rows, func(i, j int) bool { return rows[i].Symbol < rows[j].Symbol })

	spotRows := make([]rowState, 0, len(s.spotRows))
	for _, row := range s.spotRows {
		spotRows = append(spotRows, row)
	}
	sort.Slice(spotRows, func(i, j int) bool { return spotRows[i].Symbol < spotRows[j].Symbol })

	chart := append([]klineCandle(nil), s.futuresChart...)
	positions := append([]positionState(nil), s.positions...)
	spotBalances := append([]spotBalance(nil), s.spotBalances...)
	chartSymbol := s.futuresChartSymbol
	if s.panel == panelSpot {
		chart = append([]klineCandle(nil), s.spotChart...)
		chartSymbol = s.spotChartSymbol
	}
	chartInterval := s.chartInterval
	if chartInterval == "" {
		chartInterval = defaultChartInterval
	}
	return rows, spotRows, chart, positions, spotBalances, chartSymbol, chartInterval, s.lastError, s.spotError, s.accountError, s.spotAccountError, s.startedAt, s.lastUpdate, s.spotLastUpdate, s.accountLastUpdate, s.spotAccountLastUpdate, s.accountEnabled, s.panel, s.modalMessage
}
