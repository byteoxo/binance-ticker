# BFT

[中文文档](./README.zh-CN.md)

`bft` is a terminal-based Binance USD-M futures viewer written in Go. It focuses on realtime price monitoring and a live 1-hour candlestick chart, rendered with a `tview/tcell` TUI.

## Preview

<img src="./assets/screenhot.png" alt="BFT Screenshot" width="760">

## Features

- Realtime USD-M futures quotes over Binance WebSocket streams
- Live 1-hour candlestick chart with keyboard switching between configured symbols
- Stable TUI built with `tview/tcell`
- Green/red price movement highlighting
- Help overlay with shortcut reference
- TOML-based configuration only

## Configuration

The application reads config files only. It does not accept runtime CLI flags.

It looks for config files in this order:

1. `./config.toml`
2. `~/.config/binance-futures-ticker/config.toml`

If no config file is found, or any required field is missing, the program exits with an error.

An example config is included here: [config.example.toml](./config.example.toml)

## Run

```bash
cd /Users/acaibird/Developer/tmp/binance-futures-ticker
go run .
```

Build the binary:

```bash
go build -o bft .
./bft
```

## Config Fields

- `symbols`: futures symbols to subscribe to, for example `['ETHUSDT', 'BTCUSDT']`
- `chart_symbol`: symbol used by the 1-hour chart on startup
- `chart_limit`: number of 1-hour candles to render
- `timeout`: HTTP/WebSocket timeout, for example `8s`
- `retry_delay`: reconnect delay after WebSocket disconnect, for example `2s`
- `tz`: display timezone, for example `Asia/Shanghai`
- `rest_base`: Binance REST API base URL
- `ws_base`: Binance WebSocket base URL
- `no_color`: disable TUI colors

## Shortcuts

- `/` or `h`: open/close help
- `Up`: previous chart symbol
- `Down`: next chart symbol
- `q`: quit
- `Ctrl+C`: quit

## Example Config

```toml
symbols = ["ETHUSDT", "BTCUSDT", "SOLUSDT"]
chart_symbol = "ETHUSDT"
chart_limit = 48
timeout = "8s"
tz = "Asia/Shanghai"
rest_base = "https://fapi.binance.com"
ws_base = "wss://fstream.binance.com"
no_color = false
retry_delay = "2s"
```
