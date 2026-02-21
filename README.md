# RSI Strategy

基于 RSI、EMA、成交量的 Binance 永续合约量化策略。

## 策略逻辑

**入场信号（做多）：**
1. RSI 从超卖区反弹（之前 < 30，现在 >= 35）
2. 价格突破 EMA20（收盘价 > EMA，且创出新高）
3. 成交量放大 50% 以上

**入场信号（做空）：**
1. RSI 从超买区回落（之前 > 70，现在 <= 65）
2. 价格跌破 EMA20（收盘价 < EMA，且创出新低）
3. 成交量放大 50% 以上

**出场信号：**
- 多头：RSI 跌破 50 中性线
- 空头：RSI 突破 50 中性线

## 安装

```bash
go build -o rsi-strat .
```

## 使用

### 1. 回测

```bash
# 使用 binance-klines 数据回测
./rsi-strat -mode backtest -symbol BTCUSDT -db ../binance-klines/klines.db
```

输出示例：

```
========== 回测结果 ==========
总交易次数: 6
盈利次数: 2
亏损次数: 4
胜率: 33.33%
总盈亏: $28.35
总手续费: $14.43
盈亏比: 1.68
最大回撤: 0.28%
================================
```

### 2. 实盘运行

编辑 `config.json`，填入 API Key：

```json
{
  "api_key": "your-api-key",
  "secret_key": "your-secret-key",
  "symbol": "BTCUSDT",
  "rsi_period": 14,
  "rsi_oversold": 30,
  "rsi_overbought": 70,
  "rsi_entry": 35,
  "ema_period": 20,
  "vol_ratio_threshold": 1.5,
  "position_size": 0.1,
  "leverage": 1,
  "dry_run": true
}
```

运行：

```bash
./rsi-strat -mode run
```

## 参数说明

| 参数 | 默认值 | 说明 |
|------|--------|------|
| `rsi_period` | 14 | RSI 周期 |
| `rsi_oversold` | 30 | RSI 超卖阈值 |
| `rsi_overbought` | 70 | RSI 超买阈值 |
| `rsi_entry` | 35 | RSI 入场阈值（确认反转） |
| `ema_period` | 20 | EMA 周期（确认趋势） |
| `vol_ratio_threshold` | 1.5 | 成交量倍数阈值 |
| `position_size` | 0.1 | 仓位比例 (10%) |
| `leverage` | 1 | 杠杆倍数 |
| `dry_run` | true | 模拟运行模式 |

## 依赖

- [wex](https://github.com/hstcscolor/wex) - 交易所接口封装
- 数据来自 `binance-klines` SQLite 数据库

## 风险提示

- 量化交易存在风险，请谨慎使用
- 建议先在模拟环境测试
- 实盘请做好风控和仓位管理
