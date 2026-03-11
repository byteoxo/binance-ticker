# Binance Futures Ticker

一个 Go 命令行工具，用于查看币安 U 本位合约价格，支持轮询和 WebSocket 实时订阅两种模式，界面基于 `tview/tcell`。

## 功能

- 支持多个合约同时显示
- 支持自定义请求间隔
- 支持 WebSocket 实时订阅，延迟更低
- 基于 `tview/tcell` 的稳定 TUI 表格界面，不会一直滚屏
- 价格上涨显示绿色，下跌显示红色，持平显示灰色
- 显示当前时间、交易所时间、最近更新时间
- 支持自定义时区，默认 `Asia/Shanghai`

## 运行

```bash
cd /Users/acaibird/Developer/tmp/binance-futures-ticker
go run .
```

默认行为：

- 模式：`ws`
- 合约：`ETHUSDT`
- 轮询间隔：`3s`（仅 `poll` 模式使用）

## 常用示例

轮询模式，每秒刷新一次：

```bash
go run . -mode poll -symbols ETHUSDT,BTCUSDT,SOLUSDT -interval 1s
```

只请求一次后退出：

```bash
go run . -once
```

WebSocket 实时订阅：

```bash
go run . -mode ws -symbols ETHUSDT,BTCUSDT,SOLUSDT
```

按 `q` 或 `Ctrl+C` 退出 TUI。

关闭颜色输出：

```bash
go run . -mode ws -symbols ETHUSDT,BTCUSDT -no-color
```

编译后运行：

```bash
go build -o ticker .
./ticker -mode ws -symbols ETHUSDT,BTCUSDT
```

## 参数

- `-symbols`：逗号分隔的合约列表，默认 `ETHUSDT`
- `-mode`：数据模式，`poll` 或 `ws`，默认 `ws`
- `-interval`：轮询模式请求间隔，默认 `3s`
- `-timeout`：HTTP 请求超时 / WebSocket 连接超时，默认 `8s`
- `-retry-delay`：WebSocket 断线重连等待时间，默认 `2s`
- `-tz`：终端输出时区，默认 `Asia/Shanghai`
- `-base-url`：REST API 地址，默认 `https://fapi.binance.com`
- `-ws-base-url`：WebSocket 地址，默认 `wss://fstream.binance.com`
- `-once`：轮询模式下只请求一次然后退出
- `-no-color`：禁用 TUI 颜色输出

## 界面说明

- `PRICE`：当前价格
- `DELTA`：相对上一次更新的变化值和变化百分比
- `EXCHANGE_TIME`：交易所返回时间
- `LOCAL_UPDATE`：本地收到更新的时间

## 说明

- 多合约轮询模式下，程序会并发请求每个 symbol，避免币安接口批量查询返回全市场数据的问题。
- WebSocket 模式使用组合流订阅多个 `@ticker` 流，并带自动重连。
- `-once` 会走非交互文本输出，便于脚本调用；其余模式使用 TUI。
