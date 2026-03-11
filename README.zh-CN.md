# BFT

[English README](./README.md)

`bft` 是一个使用 Go 编写的币安 U 本位合约终端查看工具，核心能力是实时行情监控和 1 小时 K 线图展示，界面基于 `tview/tcell`。

## 预览

![BFT 截图](./assets/screenhot.png)

![BFT 演示](./assets/example.gif)

## 功能

- 通过 Binance WebSocket 实时订阅 U 本位合约行情
- 支持实时更新的 1 小时 K 线图，并可用键盘切换图表 symbol
- 基于 `tview/tcell` 的稳定 TUI 界面
- 涨跌使用绿色和红色高亮
- 内置帮助面板，展示快捷键说明
- 仅通过 TOML 配置文件驱动

## 配置文件

程序只读取配置文件，不接受运行时命令行参数。

默认按以下顺序查找配置文件：

1. `./config.toml`
2. `~/.config/binance-futures-ticker/config.toml`

如果没有找到配置文件，或者缺少任何必填字段，程序会直接报错退出。

仓库中提供了示例配置：[config.example.toml](./config.example.toml)

## 运行

```bash
cd /Users/acaibird/Developer/tmp/binance-futures-ticker
go run .
```

编译为二进制：

```bash
go build -o bft .
./bft
```

## 配置字段

- `symbols`：要订阅的合约列表，例如 `['ETHUSDT', 'BTCUSDT']`
- `chart_symbol`：启动时 1 小时 K 线图默认使用的 symbol
- `chart_limit`：图表展示的 1 小时 K 线数量
- `timeout`：HTTP/WebSocket 超时，例如 `8s`
- `retry_delay`：WebSocket 断线后重连等待时间，例如 `2s`
- `tz`：界面显示时区，例如 `Asia/Shanghai`
- `rest_base`：Binance REST API 基地址
- `ws_base`：Binance WebSocket 基地址
- `no_color`：是否禁用 TUI 颜色

## 快捷键

- `/` 或 `h`：打开/关闭帮助面板
- `Up`：切换到上一个图表 symbol
- `Down`：切换到下一个图表 symbol
- `q`：退出
- `Ctrl+C`：退出

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
