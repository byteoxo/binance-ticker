# Binance Ticker

[中文文档](./README.zh-CN.md)

`binance-ticker` is a terminal-based Binance market viewer written in Go. It focuses on realtime futures and spot monitoring with live candlestick charts, rendered with a `tview/tcell` TUI.

## Preview

<img src="./assets/screenhot.png" alt="Binance Ticker Screenshot" width="760">

## Features

- Realtime Binance USD-M futures and spot quotes over WebSocket streams
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

## Configuration

The application reads config files only. It does not accept runtime CLI flags.

It looks for config files in this order:

1. `./config.toml`
2. `~/.config/binance-ticker/config.toml`

If no config file is found, or any required field is missing, the program exits with an error.

An example config is included here: [config.example.toml](./config.example.toml)

## Config Fields

| Field | Description |
|-------|-------------|
| `symbols` | Futures symbols to subscribe to, e.g. `["ETHUSDT", "BTCUSDT"]` |
| `spot_symbols` | Spot assets to display, e.g. `["ZKC", "BARD"]` |
| `chart_symbol` | Default futures chart symbol on startup |
| `chart_limit` | Number of candles to render |
| `default_panel` | `futures` or `spot` |
| `timeout` | HTTP/WebSocket timeout, e.g. `8s` |
| `retry_delay` | Reconnect delay after WebSocket disconnect, e.g. `2s` |
| `tz` | Display timezone, e.g. `Asia/Shanghai` |
| `rest_base` | Binance REST API base URL |
| `ws_base` | Binance WebSocket base URL |
| `no_color` | Disable TUI colors |
| `api_key` | *(Optional)* Binance API key — enables account features |
| `api_secret` | *(Optional)* Binance API secret — required with `api_key` |

## API Key Setup (Optional)

An API key is **not required** to view market data. It is only needed to display your **futures positions** and **spot balances**.

### Step 1 — Create an API key on Binance

1. Log in to [binance.com](https://www.binance.com) and click your profile icon → **Account**.
2. Go to **API Management** → **Create API**.
3. Choose **System-generated** (HMAC) for simplicity. You will receive an **API Key** and a **Secret Key** — save the Secret Key immediately, it is shown only once.

> Full walkthrough: [How to Create API Keys on Binance](https://www.binance.com/en/support/faq/how-to-create-api-keys-on-binance-360002502072)

### Step 2 — Set permissions

This tool is **read-only**. When configuring your key:

- ✅ Enable **Read Info** (required)
- ❌ Do **not** enable trading, withdrawal, or transfer permissions

### Step 3 — Restrict by IP (recommended)

Binance strongly recommends adding IP restrictions to your API key. If your IP is unrestricted, the key can only have **Read** permission (which is all this tool needs).

> Security note from Binance: Binance strongly recommends against enabling permissions beyond reading without defining IP access restrictions.

### Step 4 — Add to config

```toml
api_key    = "your_api_key_here"
api_secret = "your_api_secret_here"
```

> ⚠️ Keep your `config.toml` private. Never commit it to a public repository.

### Binance API documentation

- [How to Create API Keys on Binance](https://www.binance.com/en/support/faq/how-to-create-api-keys-on-binance-360002502072)
- [API Key Types (HMAC / Ed25519 / RSA)](https://developers.binance.com/docs/binance-spot-api-docs/faqs/api_key_types)
- [How to Generate an Ed25519 Key Pair](https://www.binance.com/en/support/faq/how-to-generate-an-ed25519-key-pair-to-send-api-requests-on-binance-6b9a63f1e3384cf48a2eedb82767a69a)

## Example Config

```toml
symbols       = ["ETHUSDT", "BTCUSDT", "SOLUSDT"]
spot_symbols  = ["ZKC", "BARD"]
chart_symbol  = "ETHUSDT"
chart_limit   = 48
default_panel = "futures"
timeout       = "8s"
tz            = "Asia/Shanghai"
rest_base     = "https://fapi.binance.com"
ws_base       = "wss://fstream.binance.com"
no_color      = false
retry_delay   = "2s"
api_key       = ""
api_secret    = ""
```
