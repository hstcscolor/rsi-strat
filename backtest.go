package main

import (
	"database/sql"
	"fmt"
	"log"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// BacktestConfig 回测配置
type BacktestConfig struct {
	Symbol       string  // 交易对
	StartBalance float64 // 初始资金
	FeeRate      float64 // 手续费率
	Leverage     float64 // 杠杆
	PositionSize float64 // 仓位比例 (0-1)
}

// DefaultBacktestConfig 默认回测配置
var DefaultBacktestConfig = BacktestConfig{
	Symbol:       "BTCUSDT",
	StartBalance: 10000,
	FeeRate:      0.0004, // 0.04%
	Leverage:     1,
	PositionSize: 0.3,
}

// Trade 记录一笔交易
type Trade struct {
	EntryTime int64
	ExitTime  int64
	Side      string // "LONG" or "SHORT"
	EntryPrice float64
	ExitPrice  float64
	Amount     float64
	PnL        float64
	Fee        float64
}

// BacktestResult 回测结果
type BacktestResult struct {
	TotalTrades   int
	WinTrades     int
	LoseTrades    int
	TotalPnL      float64
	TotalFees     float64
	WinRate       float64
	ProfitFactor  float64
	MaxDrawdown   float64
	SharpeRatio   float64
	Trades        []Trade
	BalanceCurve  []float64
}

// loadKlinesFromDB 从 SQLite 加载 K 线数据
func loadKlinesFromDB(dbPath, symbol string, startTime, endTime int64) ([]Kline, error) {
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	// 交易对 ID 映射
	symbolMap := map[string]int{
		"BTCUSDT": 1, "ETHUSDT": 2, "BNBUSDT": 3, "SOLUSDT": 4,
	}

	symbolID, ok := symbolMap[symbol]
	if !ok {
		return nil, fmt.Errorf("unknown symbol: %s", symbol)
	}

	query := `
		SELECT ts, o, h, l, c, v
		FROM klines_futures
		WHERE symbol = ?
	`
	args := []any{symbolID}

	if startTime > 0 {
		query += " AND ts >= ?"
		args = append(args, startTime)
	}
	if endTime > 0 {
		query += " AND ts <= ?"
		args = append(args, endTime)
	}
	query += " ORDER BY ts"

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var klines []Kline
	for rows.Next() {
		var ts int64
		var o, h, l, c, v int64
		if err := rows.Scan(&ts, &o, &h, &l, &c, &v); err != nil {
			return nil, err
		}

		klines = append(klines, Kline{
			Timestamp: ts,
			Open:      float64(o) / 1e8,
			High:      float64(h) / 1e8,
			Low:       float64(l) / 1e8,
			Close:     float64(c) / 1e8,
			Volume:    float64(v) / 1e8,
		})
	}

	return klines, nil
}

// ResampleTo5m 将 1m K 线重采样为 5m
func ResampleTo5m(klines1m []Kline) []Kline {
	if len(klines1m) == 0 {
		return nil
	}

	var klines5m []Kline

	for i := 0; i < len(klines1m); i += 5 {
		end := i + 5
		if end > len(klines1m) {
			end = len(klines1m)
		}

		bucket := klines1m[i:end]
		if len(bucket) == 0 {
			continue
		}

		k5 := Kline{
			Timestamp: bucket[0].Timestamp,
			Open:      bucket[0].Open,
			Close:     bucket[len(bucket)-1].Close,
		}

		for _, k := range bucket {
			if k.High > k5.High {
				k5.High = k.High
			}
			if k.Low < k5.Low {
				k5.Low = k.Low
			}
			k5.Volume += k.Volume
		}

		klines5m = append(klines5m, k5)
	}

	return klines5m
}

// RunBacktest 执行回测（优化版：预先计算所有指标）
func RunBacktest(klines []Kline, config BacktestConfig, strategyConfig StrategyConfig) *BacktestResult {
	result := &BacktestResult{
		BalanceCurve: []float64{config.StartBalance},
	}

	n := len(klines)
	if n < 50 {
		return result
	}

	// 预先计算所有指标
	rsi := CalculateRSI(klines, strategyConfig.RSI_PERIOD)
	ema20 := CalculateEMA(klines, strategyConfig.EMA_PERIOD)
	ema50 := CalculateEMA(klines, 50)  // 加入长期 EMA
	volRatio := VolumeRatio(klines, strategyConfig.RSI_PERIOD)

	balance := config.StartBalance
	var position *struct {
		side       string
		entryTime  int64
		entryPrice float64
		amount     float64
	}
	maxBalance := balance

	for i := 50; i < n; i++ {
		k := klines[i]

		// 直接使用预计算的指标
		currentRSI := rsi[i]
		prevRSI := rsi[i-1]
		currentEMA20 := ema20[i]
		currentEMA50 := ema50[i]
		currentVolRatio := volRatio[i]

		// 判断大趋势
		uptrend := currentEMA20 > currentEMA50
		downtrend := currentEMA20 < currentEMA50

		// 如果有持仓，检查平仓条件
		if position != nil {
			// 平仓条件：RSI 反向穿越
			shouldClose := false
			if position.side == "LONG" && currentRSI < 40 {
				shouldClose = true
			} else if position.side == "SHORT" && currentRSI > 60 {
				shouldClose = true
			}

			if shouldClose {
				trade := Trade{
					EntryTime:  position.entryTime,
					ExitTime:   k.Timestamp,
					Side:       position.side,
					EntryPrice: position.entryPrice,
					ExitPrice:  k.Close,
					Amount:     position.amount,
				}

				if position.side == "LONG" {
					trade.PnL = (k.Close - position.entryPrice) * position.amount
				} else {
					trade.PnL = (position.entryPrice - k.Close) * position.amount
				}

				trade.Fee = (position.entryPrice + k.Close) * position.amount * config.FeeRate
				trade.PnL -= trade.Fee

				balance += trade.PnL
				result.Trades = append(result.Trades, trade)
				result.TotalPnL += trade.PnL
				result.TotalFees += trade.Fee
				result.TotalTrades++

				if trade.PnL > 0 {
					result.WinTrades++
				} else {
					result.LoseTrades++
				}

				position = nil
			}
		}

		// 开仓信号
		if position == nil {
			volumeOK := currentVolRatio >= strategyConfig.VOL_RATIO_THRESHOLD

			// 做多：上升趋势 + RSI 超卖反弹
			rsiBull := prevRSI < strategyConfig.RSI_OVERSOLD && currentRSI >= strategyConfig.RSI_ENTRY
			if rsiBull && uptrend && volumeOK {
				notional := balance * config.PositionSize
				amount := notional / k.Close
				position = &struct {
					side       string
					entryTime  int64
					entryPrice float64
					amount     float64
				}{
					side:       "LONG",
					entryTime:  k.Timestamp,
					entryPrice: k.Close,
					amount:     amount,
				}
				balance -= k.Close * amount * config.FeeRate
			}

			// 做空：下降趋势 + RSI 超买回落
			rsiBear := prevRSI > strategyConfig.RSI_OVERBOUGHT && currentRSI <= 58
			if rsiBear && downtrend && volumeOK {
				notional := balance * config.PositionSize
				amount := notional / k.Close
				position = &struct {
					side       string
					entryTime  int64
					entryPrice float64
					amount     float64
				}{
					side:       "SHORT",
					entryTime:  k.Timestamp,
					entryPrice: k.Close,
					amount:     amount,
				}
				balance -= k.Close * amount * config.FeeRate
			}
		}

		// 更新资金曲线
		result.BalanceCurve = append(result.BalanceCurve, balance)

		// 计算最大回撤
		if balance > maxBalance {
			maxBalance = balance
		}
		drawdown := (maxBalance - balance) / maxBalance
		if drawdown > result.MaxDrawdown {
			result.MaxDrawdown = drawdown
		}
	}

	// 计算统计指标
	if result.TotalTrades > 0 {
		result.WinRate = float64(result.WinTrades) / float64(result.TotalTrades)
	}

	var totalWin, totalLose float64
	for _, t := range result.Trades {
		if t.PnL > 0 {
			totalWin += t.PnL
		} else {
			totalLose += -t.PnL
		}
	}
	if totalLose > 0 {
		result.ProfitFactor = totalWin / totalLose
	}

	return result
}

// PrintResult 打印回测结果
func PrintResult(result *BacktestResult) {
	fmt.Println("\n========== 回测结果 ==========")
	fmt.Printf("总交易次数: %d\n", result.TotalTrades)
	fmt.Printf("盈利次数: %d\n", result.WinTrades)
	fmt.Printf("亏损次数: %d\n", result.LoseTrades)
	fmt.Printf("胜率: %.2f%%\n", result.WinRate*100)
	fmt.Printf("总盈亏: $%.2f\n", result.TotalPnL)
	fmt.Printf("总手续费: $%.2f\n", result.TotalFees)
	fmt.Printf("盈亏比: %.2f\n", result.ProfitFactor)
	fmt.Printf("最大回撤: %.2f%%\n", result.MaxDrawdown*100)

	// 统计多空表现
	var longTrades, longWins int
	var longPnL, shortPnL float64
	var shortTrades, shortWins int
	for _, t := range result.Trades {
		if t.Side == "LONG" {
			longTrades++
			longPnL += t.PnL
			if t.PnL > 0 {
				longWins++
			}
		} else {
			shortTrades++
			shortPnL += t.PnL
			if t.PnL > 0 {
				shortWins++
			}
		}
	}
	fmt.Println("\n--- 多空分开统计 ---")
	fmt.Printf("做多: %d 次, 胜率 %.1f%%, 盈亏 $%.2f\n", longTrades, float64(longWins)/float64(longTrades)*100, longPnL)
	fmt.Printf("做空: %d 次, 胜率 %.1f%%, 盈亏 $%.2f\n", shortTrades, float64(shortWins)/float64(shortTrades)*100, shortPnL)
	fmt.Println("================================")
}

// runBacktestCmd 执行回测命令
func runBacktestCmd(dbPath, symbol string, startTime, endTime int64) {
	log.Printf("加载 K 线数据: %s", symbol)
	klines1m, err := loadKlinesFromDB(dbPath, symbol, startTime, endTime)
	if err != nil {
		log.Fatalf("加载数据失败: %v", err)
	}
	log.Printf("加载 %d 根 1m K 线", len(klines1m))

	// 重采样为 5m
	klines5m := ResampleTo5m(klines1m)
	log.Printf("重采样为 %d 根 5m K 线", len(klines5m))

	if len(klines5m) < 100 {
		log.Fatalf("数据不足，至少需要 100 根 5m K 线")
	}

	// 运行回测
	config := DefaultBacktestConfig
	config.Symbol = symbol

	strategyConfig := DefaultConfig

	result := RunBacktest(klines5m, config, strategyConfig)
	PrintResult(result)

	// 打印最近几笔交易
	fmt.Println("\n最近 10 笔交易:")
	for i := len(result.Trades) - 1; i >= 0 && i >= len(result.Trades)-10; i-- {
		t := result.Trades[i]
		fmt.Printf("%s | %s | 入场: %.2f | 出场: %.2f | 盈亏: $%.2f\n",
			time.Unix(t.EntryTime, 0).Format("2006-01-02 15:04"),
			t.Side,
			t.EntryPrice,
			t.ExitPrice,
			t.PnL,
		)
	}
}
