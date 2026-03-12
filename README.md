# Binance Ticker

[中文文档](./README.zh-CN.md)

`binance-ticker` is a terminal-based Binance market viewer written in Go. It focuses on realtime futures and spot monitoring with live 1-hour candlestick charts, rendered with a `tview/tcell` TUI.

## Preview

<img src="./assets/screenhot.png" alt="Binance Ticker Screenshot" width="760">

## Features

- Realtime Binance USD-M futures and spot quotes over WebSocket streams
- Live 1-hour candlestick charts for both futures and spot, with keyboard symbol switching
- Stable TUI built with `tview/tcell`
- Green/red price movement highlighting
- Help overlay with shortcut reference
- TOML-based configuration only

## Configuration

The application reads config files only. It does not accept runtime CLI flags.

It looks for config files in this order:

1. `./config.toml`
2. `~/.config/binance-ticker/config.toml`

If no config file is found, or any required field is missing, the program exits with an error.

An example config is included here: [config.example.toml](./config.example.toml)

## Installation

### Homebrew (macOS / Linux)

```bash
brew tap byteoxo/tap
brew install binance-ticker
```

After installation, both `bt` and `binance-ticker` are available:

```bash
bt
```

### Pre-built binaries

Download the latest release for your platform from the [Releases](https://github.com/byteoxo/binance-ticker/releases) page.

### Build from source

```bash
git clone https://github.com/byteoxo/binance-ticker.git
cd binance-ticker
go build -o binance-ticker .
./binance-ticker
```

## Config Fields

- `symbols`: futures symbols to subscribe to, for example `['ETHUSDT', 'BTCUSDT']`
- `spot_symbols`: configured spot assets, for example `['ZKC', 'BARD']`
- `chart_symbol`: startup futures chart symbol
- `chart_limit`: number of 1-hour candles to render
- `default_panel`: `futures` or `spot`
- `timeout`: HTTP/WebSocket timeout, for example `8s`
- `retry_delay`: reconnect delay after WebSocket disconnect, for example `2s`
- `tz`: display timezone, for example `Asia/Shanghai`
- `rest_base`: Binance REST API base URL
- `ws_base`: Binance WebSocket base URL
- `no_color`: disable TUI colors

## Shortcuts

- `/` or `h`: open/close help
- `Up` / `Left`: previous chart symbol
- `Down` / `Right`: next chart symbol
- `q`: quit
- `Ctrl+C`: quit

## Example Config

```toml
symbols = ["ETHUSDT", "BTCUSDT", "SOLUSDT"]
chart_symbol = "ETHUSDT"
chart_limit = 8
timeout = "8s"
tz = "Asia/Shanghai"
rest_base = "https://fapi.binance.com"
ws_base = "wss://fstream.binance.com"
no_color = false
retry_delay = "2s"
```
