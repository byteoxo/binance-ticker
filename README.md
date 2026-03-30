# Crypto Ticker

[中文文档](./README.zh-CN.md)

`crypto-ticker` is a terminal-based crypto market viewer written in Go. It supports realtime futures and spot monitoring with live candlestick charts, rendered with a `tview/tcell` TUI. Both **Binance** and **Gate.io** are supported.

## Preview

<img src="./assets/screenhot.png" alt="Crypto Ticker Screenshot" width="760">

## Features

- Realtime quotes over WebSocket — supports **Binance** and **Gate.io** (futures + spot)
- Live candlestick charts (1h / 2h / 4h / 1d / 3d) with keyboard symbol and interval switching
- Volume bars, EMA20/50, RSI14, Bollinger Bands, and MACD shown below each chart
- 24h price change %, Open Interest (with ▲/▼ change), Long/Short Ratio, and Funding Rate per symbol
- Sparkline trend column in the price table
- Order book overlay (20 levels, live refresh)
- Stable TUI built with `tview/tcell`
- Green/red price movement highlighting
- Help overlay with shortcut reference
- TOML-based configuration only

## Shortcuts

| Key | Action |
|-----|--------|
| `/` or `h` | Open / close help |
| `Tab` | Switch futures / spot panel |
| `Up` / `Left` | Previous chart symbol |
| `Down` / `Right` | Next chart symbol |
| `i` | Cycle chart interval (1h → 2h → 4h → 1d → 3d) |
| `o` | Open order book for current symbol |
| `Esc` | Close help / modal / order book |
| `q` / `Ctrl+C` | Quit |

## Installation

### Homebrew (macOS / Linux)

```bash
brew tap byteoxo/tap
brew install crypto-ticker
```

### Pre-built binaries

Download the latest release for your platform from the [Releases](https://github.com/byteoxo/crypto-ticker/releases) page.

### Build from source

```bash
git clone https://github.com/byteoxo/crypto-ticker.git
cd crypto-ticker
go build -o crypto-ticker .
./crypto-ticker
```

## Configuration

The application reads config files only. It does not accept runtime CLI flags.

It looks for config files in this order:

1. `./config.toml`
2. `~/.config/crypto-ticker/config.toml`

If no config file is found, or any required field is missing, the program exits with an error.

Example configs are included:
- Binance: [config.example.toml](./config.example.toml)
- Gate.io: [config.gate.example.toml](./config.gate.example.toml)

## Config Fields

| Field | Description |
|-------|-------------|
| `exchange` | `binance` (default) or `gate` |
| `symbols` | Futures symbols to subscribe to, e.g. `["ETHUSDT", "BTCUSDT"]` (Binance) or `["BTC_USDT", "ETH_USDT"]` (Gate.io) |
| `spot_symbols` | Spot assets to display |
| `chart_symbol` | Default futures chart symbol on startup |
| `chart_limit` | Number of candles to render |
| `default_panel` | `futures` or `spot` |
| `timeout` | HTTP/WebSocket timeout, e.g. `8s` |
| `retry_delay` | Reconnect delay after WebSocket disconnect, e.g. `2s` |
| `tz` | Display timezone, e.g. `Asia/Shanghai` |
| `rest_base` | *(Optional)* REST API base URL — defaults to exchange default |
| `ws_base` | *(Optional)* WebSocket base URL — defaults to exchange default |
| `no_color` | Disable TUI colors |
| `api_key` | *(Optional)* API key — enables account features |
| `api_secret` | *(Optional)* API secret — required with `api_key` |

## Exchange Support

### Binance (default)

```toml
exchange = "binance"
symbols  = ["ETHUSDT", "BTCUSDT", "SOLUSDT"]
```

Defaults applied automatically:
- REST: `https://fapi.binance.com`
- WS: `wss://fstream.binance.com`

### Gate.io

Gate.io uses underscore-separated symbol names (`BTC_USDT` instead of `BTCUSDT`).

```toml
exchange = "gate"
symbols  = ["BTC_USDT", "ETH_USDT", "SOL_USDT"]
```

Defaults applied automatically:
- Futures REST: `https://fx-api.gateio.ws`
- Futures WS: `wss://fx-ws.gateio.ws/v4/ws/usdt`
- Spot REST: `https://api.gateio.ws`
- Spot WS: `wss://api.gateio.ws/ws/v4/`

## API Key Setup (Optional)

An API key is **not required** to view market data. It is only needed to display your **futures positions** and **spot balances**.

### Setting keys via environment variables (recommended)

Environment variables take precedence over config file values.

| Exchange | Key variable | Secret variable |
|----------|-------------|----------------|
| Binance | `BINANCE_API_KEY` | `BINANCE_API_SECRET` |
| Gate.io | `GATE_API_KEY` | `GATE_API_SECRET` |

```bash
export BINANCE_API_KEY=your_key
export BINANCE_API_SECRET=your_secret
```

or for Gate.io:

```bash
export GATE_API_KEY=your_key
export GATE_API_SECRET=your_secret
```

### Setting keys via config file

```toml
api_key    = "your_api_key_here"
api_secret = "your_api_secret_here"
```

> ⚠️ Keep your `config.toml` private. Never commit it to a public repository.

### Binance — creating an API key

1. Log in to [binance.com](https://www.binance.com) and go to **API Management** → **Create API**.
2. Choose **System-generated** (HMAC). Save the Secret Key immediately — it is shown only once.
3. Enable **Read Info** only. Do not enable trading or withdrawal permissions.

> Full walkthrough: [How to Create API Keys on Binance](https://www.binance.com/en/support/faq/how-to-create-api-keys-on-binance-360002502072)

## Example Config (Binance)

```toml
exchange      = "binance"
symbols       = ["ETHUSDT", "BTCUSDT", "SOLUSDT"]
spot_symbols  = ["ZKC", "BARD"]
chart_symbol  = "ETHUSDT"
chart_limit   = 48
default_panel = "futures"
timeout       = "8s"
tz            = "Asia/Shanghai"
no_color      = false
retry_delay   = "2s"
api_key       = ""
api_secret    = ""
```

## Example Config (Gate.io)

```toml
exchange      = "gate"
symbols       = ["BTC_USDT", "ETH_USDT", "SOL_USDT"]
spot_symbols  = ["BTC_USDT", "ETH_USDT"]
chart_symbol  = "BTC_USDT"
chart_limit   = 48
default_panel = "futures"
timeout       = "8s"
tz            = "Asia/Shanghai"
no_color      = false
retry_delay   = "2s"
api_key       = ""
api_secret    = ""
```

## Star History

<a href="https://www.star-history.com/?repos=crypto-ticker%2Fcrypto-ticker&type=date&legend=top-left">
 <picture>
   <source media="(prefers-color-scheme: dark)" srcset="https://api.star-history.com/image?repos=crypto-ticker/crypto-ticker&type=date&theme=dark&legend=top-left" />
   <source media="(prefers-color-scheme: light)" srcset="https://api.star-history.com/image?repos=crypto-ticker/crypto-ticker&type=date&legend=top-left" />
   <img alt="Star History Chart" src="https://api.star-history.com/image?repos=crypto-ticker/crypto-ticker&type=date&legend=top-left" />
 </picture>
</a>
