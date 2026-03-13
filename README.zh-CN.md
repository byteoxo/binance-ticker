# Binance Ticker

[English README](./README.md)

`binance-ticker` 是一个使用 Go 编写的币安终端查看工具，支持合约与现货实时行情监控以及 K 线图展示，界面基于 `tview/tcell`。

## 预览

<img src="./assets/screenhot.png" alt="Binance Ticker 截图" width="760">

## 功能

- 通过 Binance WebSocket 实时订阅合约与现货行情
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
brew install binance-ticker
```

安装后 `bt` 和 `binance-ticker` 均可使用：

```bash
bt
```

### 下载预编译二进制

从 [Releases](https://github.com/byteoxo/binance-ticker/releases) 页面下载对应平台的压缩包。

### 从源码编译

```bash
git clone https://github.com/byteoxo/binance-ticker.git
cd binance-ticker
go build -o binance-ticker .
./binance-ticker
```

## 配置文件

程序只读取配置文件，不接受运行时命令行参数。

默认按以下顺序查找配置文件：

1. `./config.toml`
2. `~/.config/binance-ticker/config.toml`

如果没有找到配置文件，或者缺少任何必填字段，程序会直接报错退出。

仓库中提供了示例配置：[config.example.toml](./config.example.toml)

## 配置字段

| 字段 | 说明 |
|------|------|
| `symbols` | 订阅的合约列表，例如 `["ETHUSDT", "BTCUSDT"]` |
| `spot_symbols` | 展示的现货资产列表，例如 `["ZKC", "BARD"]` |
| `chart_symbol` | 启动时合约 K 线图默认使用的 symbol |
| `chart_limit` | 图表展示的 K 线数量 |
| `default_panel` | 默认面板：`futures` 或 `spot` |
| `timeout` | HTTP/WebSocket 超时时间，例如 `8s` |
| `retry_delay` | WebSocket 断线后重连等待时间，例如 `2s` |
| `tz` | 界面显示时区，例如 `Asia/Shanghai` |
| `rest_base` | Binance REST API 基地址 |
| `ws_base` | Binance WebSocket 基地址 |
| `no_color` | 是否禁用 TUI 颜色 |
| `api_key` | *（可选）* 币安 API Key — 启用账户功能 |
| `api_secret` | *（可选）* 币安 API Secret — 与 `api_key` 配套使用 |

## API Key 配置（可选）

查看行情**不需要** API Key，仅在需要显示**合约持仓**和**现货余额**时才需要配置。

### 第一步 — 在币安创建 API Key

1. 登录 [binance.com](https://www.binance.com)，点击右上角头像 → **账户**。
2. 进入 **API 管理** → **创建 API**。
3. 选择**系统生成**（HMAC）方式，系统会生成 **API Key** 和 **Secret Key**。Secret Key 只显示一次，请立即保存。

> 完整操作指引（官方）：[如何在币安创建 API Keys](https://www.binance.com/en/support/faq/how-to-create-api-keys-on-binance-360002502072)

### 第二步 — 设置权限

本工具为**只读**，配置 API Key 时：

- ✅ 开启 **读取权限（Read Info）**
- ❌ 不要开启交易、提现、划转等任何写权限

### 第三步 — 绑定 IP（推荐）

币安强烈建议为 API Key 绑定 IP 白名单。若不绑定 IP，该 Key 只能设置为只读权限（本工具所需权限）。

> 官方安全提示：强烈建议不要在未设置 IP 访问限制的情况下开启读取以外的权限。

### 第四步 — 填写到配置文件

```toml
api_key    = "your_api_key_here"
api_secret = "your_api_secret_here"
```

> ⚠️ 请妥善保管 `config.toml`，切勿提交到公开仓库。

### 币安官方 API 文档

- [如何在币安创建 API Keys](https://www.binance.com/en/support/faq/how-to-create-api-keys-on-binance-360002502072)
- [API Key 类型说明（HMAC / Ed25519 / RSA）](https://developers.binance.com/docs/binance-spot-api-docs/faqs/api_key_types)
- [如何生成 Ed25519 密钥对](https://www.binance.com/en/support/faq/how-to-generate-an-ed25519-key-pair-to-send-api-requests-on-binance-6b9a63f1e3384cf48a2eedb82767a69a)

## 示例配置

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
