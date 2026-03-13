# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build & Run

```bash
# Build
go build -o binance-ticker .

# Run (requires config.toml in CWD or ~/.config/binance-ticker/config.toml)
./binance-ticker

# Lint
go vet ./...

# Tests (no test files currently; run with)
go test ./...
```

Releases are produced via GoReleaser triggered by pushing a `v*` tag. The CI workflow (`.github/workflows/release.yml`) builds cross-platform binaries and updates the Homebrew formula automatically.

## Configuration

The app reads TOML config only — no CLI flags. Config is searched at `./config.toml` then `~/.config/binance-ticker/config.toml`. Copy `config.example.toml` to get started. All fields in `config.go:required` slice must be present or the program exits.

## Architecture Overview

Single `package main`. No internal packages. All state flows through one central `appState` struct.

### Data Flow

```
Binance WebSocket ──► market_ws.go ──► appState (state.go)
Binance REST API  ──► api.go       ──► appState
                                         │
                                         ▼
                                      ui.go (tview TUI)
                                      chart.go + indicators.go
```

### Key Files

| File | Role |
|------|------|
| `main.go` | Entry point, goroutine wiring, `changeChart`/`changeInterval` callbacks, all app-level constants |
| `state.go` | `appState` struct + all mutating methods; `snapshot()` is the single read path for the UI |
| `ui.go` | `tview` layout, keyboard bindings, table rendering, order book overlay, help panel |
| `market_ws.go` | Futures WebSocket loop with auto-reconnect; `errResubscribe` sentinel for symbol/interval changes |
| `api.go` | REST calls: klines, positions, spot balances, funding rates, open interest, long/short ratio |
| `userdata.go` | User data stream (listen key keepalive, position + balance updates via WebSocket) |
| `chart.go` | Candlestick rendering to a `strings.Builder`; calls `buildIndicatorLine` and volume bars |
| `indicators.go` | EMA, RSI, Bollinger Bands, MACD — pure functions over `[]klineCandle` |
| `sparkline.go` | Unicode sparkline (`▁▂▃▄▅▆▇█`) from a `[]float64` price history |
| `orderbook.go` | REST-polled order book overlay (20 levels) |
| `config.go` | TOML parsing, validation, `config` struct |
| `types.go` | JSON payload types for WebSocket messages |
| `symbol.go` | Symbol normalisation helpers (`spotSymbolsToTickers`, `spotSymbolToTicker`) |
| `format.go` | Display formatting helpers (`formatCompactFloat`, `formatCompactNumber`, etc.) |

### `appState` and `snapshot()`

`appState` is guarded by `sync.RWMutex`. The UI reads state exclusively via `snapshot()`, which returns 19 values as value copies (no pointers into the map). Every call site must destructure all 19 values — use `_` for unused ones. Adding a new return value requires updating every call site in `main.go`, `ui.go`, `config.go`, and `orderbook.go`.

### WebSocket Resubscription Pattern

`consumeWS` records a `baselineSymbols` string (symbols + chart symbol + interval) at connection time. A 1-second ticker compares the current values; if changed, it returns `errResubscribe`. `runWSLoop` catches this sentinel and immediately reconnects with the new parameters — no sleep delay.

### Dual Panel (Futures / Spot)

- Futures: `cfg.WSBase` (default `wss://fstream.binance.com`), REST `cfg.RESTBase`
- Spot: hardcoded `defaultSpotWSBaseURL` / `defaultSpotRESTBaseURL`
- Chart state is kept separately as `futuresChart`/`spotChart` and `futuresChartSymbol`/`spotChartSymbol`; `snapshot()` returns the active panel's values

### Chart Intervals

Supported: `1h 2h 4h 1d 3d` (slice in `main.go:chartIntervals`). Interval is stored in `appState.chartInterval`. On change: both charts are set to `nil`, history is reloaded via REST, and WS resubscribes to the new `@kline_{interval}` stream.

### Signed REST Requests (Account)

`buildSignedURL` in `api.go` signs requests with HMAC-SHA256 using `cfg.APISecret`, appending the `signature` query param. The `X-MBX-APIKEY` header carries `cfg.APIKey`. These are only used when `cfg.hasAccountAuth()` is true.
