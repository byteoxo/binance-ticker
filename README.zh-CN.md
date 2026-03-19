# Crypto Ticker

[English README](./README.md)

`crypto-ticker` 是一个使用 Go 编写的加密货币终端行情工具，支持合约与现货实时监控以及 K 线图展示，界面基于 `tview/tcell`。同时支持 **Binance** 和 **Gate.io**。

## 预览

<img src="./assets/screenhot.png" alt="Crypto Ticker 截图" width="760">

## 功能

- 通过 WebSocket 实时订阅行情，支持 **Binance** 和 **Gate.io**（合约 + 现货）
- 实时更新的 K 线图（1h / 2h / 4h / 1d / 3d），支持键盘切换 symbol 和周期
- 图表下方显示成交量柱、EMA20/50、RSI14、布林带（BB）和 MACD
- 行情表显示 24h 涨跌幅、未平仓量（含 ▲/▼ 变化）、多空比、资金费率
- 行情表含 Sparkline 趋势列
- 订单簿浮层（20 档深度，实时刷新）
- 基于 `tview/tcell` 的稳定 TUI 界面
- 涨跌绿红高亮
- 内置帮助面板，展示快捷键说明
- 仅通过 TOML 配置文件驱动

## 快捷键

| 按键 | 功能 |
|------|------|
| `/` 或 `h` | 打开 / 关闭帮助面板 |
| `Tab` | 切换合约 / 现货面板 |
| `Up` / `Left` | 切换到上一个图表 symbol |
| `Down` / `Right` | 切换到下一个图表 symbol |
| `i` | 循环切换图表周期（1h → 2h → 4h → 1d → 3d） |
| `o` | 打开当前 symbol 的订单簿 |
| `Esc` | 关闭帮助 / 模态框 / 订单簿 |
| `q` / `Ctrl+C` | 退出 |

## 安装

### Homebrew（macOS / Linux）

```bash
brew tap byteoxo/tap
brew install crypto-ticker
```

### 下载预编译二进制

从 [Releases](https://github.com/byteoxo/crypto-ticker/releases) 页面下载对应平台的压缩包。

### 从源码编译

```bash
git clone https://github.com/byteoxo/crypto-ticker.git
cd crypto-ticker
go build -o crypto-ticker .
./crypto-ticker
```

## 配置文件

程序只读取配置文件，不接受运行时命令行参数。

默认按以下顺序查找配置文件：

1. `./config.toml`
2. `~/.config/crypto-ticker/config.toml`

如果没有找到配置文件，或者缺少任何必填字段，程序会直接报错退出。

仓库中提供了示例配置：
- Binance：[config.example.toml](./config.example.toml)
- Gate.io：[config.gate.example.toml](./config.gate.example.toml)

## 配置字段

| 字段 | 说明 |
|------|------|
| `exchange` | `binance`（默认）或 `gate` |
| `symbols` | 订阅的合约列表，例如 `["ETHUSDT", "BTCUSDT"]`（Binance）或 `["BTC_USDT", "ETH_USDT"]`（Gate.io） |
| `spot_symbols` | 展示的现货资产列表 |
| `chart_symbol` | 启动时合约 K 线图默认使用的 symbol |
| `chart_limit` | 图表展示的 K 线数量 |
| `default_panel` | 默认面板：`futures` 或 `spot` |
| `timeout` | HTTP/WebSocket 超时时间，例如 `8s` |
| `retry_delay` | WebSocket 断线后重连等待时间，例如 `2s` |
| `tz` | 界面显示时区，例如 `Asia/Shanghai` |
| `rest_base` | *（可选）* REST API 基地址 — 不填则使用 exchange 默认值 |
| `ws_base` | *（可选）* WebSocket 基地址 — 不填则使用 exchange 默认值 |
| `no_color` | 是否禁用 TUI 颜色 |
| `api_key` | *（可选）* API Key — 启用账户功能 |
| `api_secret` | *（可选）* API Secret — 与 `api_key` 配套使用 |

## 交易所支持

### Binance（默认）

```toml
exchange = "binance"
symbols  = ["ETHUSDT", "BTCUSDT", "SOLUSDT"]
```

自动应用的默认地址：
- REST：`https://fapi.binance.com`
- WS：`wss://fstream.binance.com`

### Gate.io

Gate.io 使用下划线分隔的交易对格式（`BTC_USDT` 而非 `BTCUSDT`）。

```toml
exchange = "gate"
symbols  = ["BTC_USDT", "ETH_USDT", "SOL_USDT"]
```

自动应用的默认地址：
- 合约 REST：`https://fx-api.gateio.ws`
- 合约 WS：`wss://fx-ws.gateio.ws/v4/ws/usdt`
- 现货 REST：`https://api.gateio.ws`
- 现货 WS：`wss://api.gateio.ws/ws/v4/`

## API Key 配置（可选）

查看行情**不需要** API Key，仅在需要显示**合约持仓**和**现货余额**时才需要配置。

### 通过环境变量设置（推荐）

环境变量的优先级高于配置文件。

| 交易所 | Key 变量 | Secret 变量 |
|--------|---------|------------|
| Binance | `BINANCE_API_KEY` | `BINANCE_API_SECRET` |
| Gate.io | `GATE_API_KEY` | `GATE_API_SECRET` |

```bash
export BINANCE_API_KEY=your_key
export BINANCE_API_SECRET=your_secret
```

或 Gate.io：

```bash
export GATE_API_KEY=your_key
export GATE_API_SECRET=your_secret
```

### 通过配置文件设置

```toml
api_key    = "your_api_key_here"
api_secret = "your_api_secret_here"
```

> ⚠️ 请妥善保管 `config.toml`，切勿提交到公开仓库。

### Binance — 创建 API Key

1. 登录 [binance.com](https://www.binance.com)，进入 **API 管理** → **创建 API**。
2. 选择**系统生成**（HMAC）方式。Secret Key 只显示一次，请立即保存。
3. 仅开启**读取权限（Read Info）**，不要开启交易、提现等写权限。

> 完整操作指引：[如何在币安创建 API Keys](https://www.binance.com/en/support/faq/how-to-create-api-keys-on-binance-360002502072)

## 示例配置（Binance）

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

## 示例配置（Gate.io）

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
