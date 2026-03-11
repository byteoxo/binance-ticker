# Binance Futures Ticker

一个 Go 命令行工具，用于查看币安 U 本位合约价格和 1 小时 K 线图，界面基于 `tview/tcell`，配置通过 TOML 文件加载。

## 功能

- 支持多个合约同时显示
- 支持 WebSocket 实时订阅，延迟更低
- 基于 `tview/tcell` 的稳定 TUI 表格界面，不会一直滚屏
- 价格上涨显示绿色，下跌显示红色，持平显示灰色
- 显示当前时间、交易所时间、最近更新时间
- 支持生成并实时刷新 1 小时 K 线图
- 支持自定义时区，默认 `Asia/Shanghai`

## 配置文件

程序只读取配置文件，不再接受命令行参数。

默认按以下顺序查找：

1. `./config.toml`
2. `~/.config/binance-futures-ticker/config.toml`

如果没有配置文件，或者缺少任何必填字段，程序会直接报错退出。

当前目录已经提供了一个可用的示例配置：[config.toml](/Users/acaibird/Developer/tmp/binance-futures-ticker/config.toml)。

## 运行

```bash
cd /Users/acaibird/Developer/tmp/binance-futures-ticker
go run .
```

按 `q` 或 `Ctrl+C` 退出 TUI。

编译后运行：

```bash
go build -o bft .
./bft
```

## 配置字段

- `symbols`：合约列表，例如 `['ETHUSDT', 'BTCUSDT']`
- `chart_symbol`：1 小时 K 线图使用的 symbol
- `chart_limit`：1 小时 K 线数量
- `timeout`：HTTP 请求超时 / WebSocket 连接超时，例如 `8s`
- `retry_delay`：WebSocket 断线重连等待时间，例如 `2s`
- `tz`：终端输出时区，例如 `Asia/Shanghai`
- `rest_base`：REST API 地址
- `ws_base`：WebSocket 地址
- `no_color`：是否禁用 TUI 颜色

## 界面说明

- `PRICE`：当前价格
- `DELTA`：相对上一次更新的变化值和变化百分比
- `EXCHANGE_TIME`：交易所返回时间
- `LOCAL_UPDATE`：本地收到更新的时间
- 右侧 `1H Chart`：最近若干根 1 小时 K 线，先拉历史，再随 WebSocket 实时更新

## 说明

- WebSocket 模式使用组合流订阅多个 `@ticker` 流，并带自动重连。

## 示例配置

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
