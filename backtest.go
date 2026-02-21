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

// DefaultBacktestConfig 默认回测配置（超短线）
var DefaultBacktestConfig = BacktestConfig{
	Symbol:       "BTCUSDT",
	StartBalance: 10000,
	FeeRate:      0.0004,
	Leverage:     5,
	PositionSize: 0.3,  // 第一批 30%
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

// Position 持仓信息（支持分批建仓）
type Position struct {
	side       string
	entries    []PositionEntry // 多个入场点
	totalAmt   float64         // 总持仓量
	avgPrice   float64         // 平均入场价
}

// PositionEntry 单次入场记录
type PositionEntry struct {
	entryTime  int64
	entryPrice float64
	amount     float64
	batch      int // 第几批
}

// RunBacktest 执行回测（超短线 1分钟级别）
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
	emaFast := CalculateEMA(klines, strategyConfig.EMA_FAST)
	emaSlow := CalculateEMA(klines, strategyConfig.EMA_SLOW)
	volRatio := VolumeRatio(klines, strategyConfig.RSI_PERIOD)

	balance := config.StartBalance
	var position *Position
	maxBalance := balance

	// 超短线参数
	firstBatchSize  := 0.30  // 第一批 30%
	secondBatchSize := 0.30  // 第二批 30%

	for i := 20; i < n; i++ {
		k := klines[i]

		currentRSI := rsi[i]
		prevRSI := rsi[i-1]
		currentEMAFast := emaFast[i]
		currentEMASlow := emaSlow[i]
		prevEMAFast := emaFast[i-1]
		prevEMASlow := emaSlow[i-1]
		currentVolRatio := volRatio[i]

		// 趋势判断
		uptrend := currentEMAFast > currentEMASlow
		downtrend := currentEMAFast < currentEMASlow

		volumeOK := currentVolRatio >= strategyConfig.VOL_RATIO_THRESHOLD

		// 计算前5根K线最高/最低价
		high5 := klines[i-1].High
		low5 := klines[i-1].Low
		for j := 2; j <= 5 && i-j >= 0; j++ {
			if klines[i-j].High > high5 {
				high5 = klines[i-j].High
			}
			if klines[i-j].Low < low5 {
				low5 = klines[i-j].Low
			}
		}

		// ========== 出场逻辑（超短线快进快出）==========
		if position != nil {
			shouldCloseAll := false

			// 计算盈亏
			pnlPercent := (k.Close - position.avgPrice) / position.avgPrice
			if position.side == "SHORT" {
				pnlPercent = -pnlPercent
			}

			// 分批止盈
			if pnlPercent >= 0.015 {
				// 盈利 1.5% → 全平
				shouldCloseAll = true
			}

			// 止损
			if pnlPercent <= -0.005 {
				// 亏损 0.5% → 全平止损
				shouldCloseAll = true
			}

			// EMA 反转
			crossDown := prevEMAFast > prevEMASlow && currentEMAFast <= currentEMASlow
			crossUp := prevEMAFast < prevEMASlow && currentEMAFast >= currentEMASlow
			if position.side == "LONG" && crossDown {
				shouldCloseAll = true
			} else if position.side == "SHORT" && crossUp {
				shouldCloseAll = true
			}

			// 执行平仓
			if shouldCloseAll && len(position.entries) > 0 {
				for _, entry := range position.entries {
					trade := Trade{
						EntryTime:  entry.entryTime,
						ExitTime:   k.Timestamp,
						Side:       position.side,
						EntryPrice: entry.entryPrice,
						ExitPrice:  k.Close,
						Amount:     entry.amount,
					}
					if position.side == "LONG" {
						trade.PnL = (k.Close - entry.entryPrice) * entry.amount
					} else {
						trade.PnL = (entry.entryPrice - k.Close) * entry.amount
					}
					trade.Fee = (entry.entryPrice + k.Close) * entry.amount * config.FeeRate
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
				}
				position = nil
			} else if pnlPercent >= 0.008 && len(position.entries) > 1 {
				// 盈利 0.8% → 平掉第一批（部分止盈）
				var newEntries []PositionEntry
				for _, entry := range position.entries {
					if entry.batch == 1 {
						// 平掉第一批
						trade := Trade{
							EntryTime:  entry.entryTime,
							ExitTime:   k.Timestamp,
							Side:       position.side,
							EntryPrice: entry.entryPrice,
							ExitPrice:  k.Close,
							Amount:     entry.amount,
						}
						if position.side == "LONG" {
							trade.PnL = (k.Close - entry.entryPrice) * entry.amount
						} else {
							trade.PnL = (entry.entryPrice - k.Close) * entry.amount
						}
						trade.Fee = (entry.entryPrice + k.Close) * entry.amount * config.FeeRate
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
					} else {
						newEntries = append(newEntries, entry)
					}
				}
				if len(newEntries) == 0 {
					position = nil
				} else {
					position.entries = newEntries
					position.totalAmt = 0
					for _, e := range newEntries {
						position.totalAmt += e.amount
					}
				}
			}
		}

		// ========== 建仓逻辑 ==========
		currentPositionPct := 0.0
		if position != nil {
			currentPositionPct = position.totalAmt * k.Close / balance
		}

		// --- 做多：反弹追趋势 ---
		if (position == nil || position.side == "LONG") && uptrend {
			// 第一批：RSI 超卖反弹 + 突破前高
			rsiBull := prevRSI < strategyConfig.RSI_OVERSOLD_LONG && currentRSI >= strategyConfig.RSI_ENTRY_LONG
			breakoutUp := k.Close > high5
			if rsiBull && breakoutUp && volumeOK && currentPositionPct < firstBatchSize {
				if position == nil {
					position = &Position{side: "LONG"}
				}
				notional := balance * firstBatchSize
				amount := notional / k.Close
				position.entries = append(position.entries, PositionEntry{
					entryTime:  k.Timestamp,
					entryPrice: k.Close,
					amount:     amount,
					batch:      1,
				})
				position.totalAmt += amount
				position.avgPrice = (position.avgPrice*(position.totalAmt-amount) + k.Close*amount) / position.totalAmt
				balance -= k.Close * amount * config.FeeRate
			}

			// 第二批：盈利 +0.3% 加仓
			if position != nil && len(position.entries) == 1 {
				pnlPercent := (k.Close - position.avgPrice) / position.avgPrice
				if pnlPercent >= 0.003 && currentPositionPct < firstBatchSize + secondBatchSize {
					notional := balance * secondBatchSize
					amount := notional / k.Close
					position.entries = append(position.entries, PositionEntry{
						entryTime:  k.Timestamp,
						entryPrice: k.Close,
						amount:     amount,
						batch:      2,
					})
					position.totalAmt += amount
					position.avgPrice = (position.avgPrice*(position.totalAmt-amount) + k.Close*amount) / position.totalAmt
					balance -= k.Close * amount * config.FeeRate
				}
			}
		}

		// --- 做空：回落追趋势 ---
		if (position == nil || position.side == "SHORT") && downtrend {
			// 第一批：RSI 超买回落 + 跌破前低
			rsiBear := prevRSI > strategyConfig.RSI_OVERBOUGHT_SHORT && currentRSI <= strategyConfig.RSI_ENTRY_SHORT
			breakoutDown := k.Close < low5
			if rsiBear && breakoutDown && volumeOK && currentPositionPct < firstBatchSize {
				if position == nil {
					position = &Position{side: "SHORT"}
				}
				notional := balance * firstBatchSize
				amount := notional / k.Close
				position.entries = append(position.entries, PositionEntry{
					entryTime:  k.Timestamp,
					entryPrice: k.Close,
					amount:     amount,
					batch:      1,
				})
				position.totalAmt += amount
				position.avgPrice = (position.avgPrice*(position.totalAmt-amount) + k.Close*amount) / position.totalAmt
				balance -= k.Close * amount * config.FeeRate
			}

			// 第二批：盈利 +0.3% 加仓
			if position != nil && len(position.entries) == 1 {
				pnlPercent := (position.avgPrice - k.Close) / position.avgPrice
				if pnlPercent >= 0.003 && currentPositionPct < firstBatchSize + secondBatchSize {
					notional := balance * secondBatchSize
					amount := notional / k.Close
					position.entries = append(position.entries, PositionEntry{
						entryTime:  k.Timestamp,
						entryPrice: k.Close,
						amount:     amount,
						batch:      2,
					})
					position.totalAmt += amount
					position.avgPrice = (position.avgPrice*(position.totalAmt-amount) + k.Close*amount) / position.totalAmt
					balance -= k.Close * amount * config.FeeRate
				}
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
	klines, err := loadKlinesFromDB(dbPath, symbol, startTime, endTime)
	if err != nil {
		log.Fatalf("加载数据失败: %v", err)
	}
	log.Printf("加载 %d 根 1m K 线（超短线模式）", len(klines))

	if len(klines) < 100 {
		log.Fatalf("数据不足，至少需要 100 根 K 线")
	}

	// 直接用 1 分钟 K 线，不重采样
	config := DefaultBacktestConfig
	config.Symbol = symbol

	strategyConfig := DefaultConfig

	result := RunBacktest(klines, config, strategyConfig)
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

// OptimizeResult 优化结果
type OptimizeResult struct {
	Config    StrategyConfig
	TotalPnL  float64
	WinRate   float64
	Trades    int
	ProfitFactor float64
}

// RunOptimize 参数优化（多空分开）
func RunOptimize(klines []Kline, config BacktestConfig) {
	fmt.Println("\n========== 参数优化 ==========")
	fmt.Println("遍历参数空间...")

	var results []OptimizeResult

	// 参数范围
	oversoldLongRange := []float64{35, 40, 45}
	entryLongRange := []float64{45, 50, 55}
	overboughtShortRange := []float64{55, 60, 65}
	entryShortRange := []float64{45, 50, 55}
	volRatioRange := []float64{1.0, 1.5, 2.0}
	emaFastRange := []int{5, 7, 10}
	emaSlowRange := []int{14, 20, 30}

	total := len(oversoldLongRange) * len(entryLongRange) * len(overboughtShortRange) * len(entryShortRange) * len(volRatioRange) * len(emaFastRange) * len(emaSlowRange)
	count := 0

	for _, oversoldLong := range oversoldLongRange {
		for _, entryLong := range entryLongRange {
			for _, overboughtShort := range overboughtShortRange {
				for _, entryShort := range entryShortRange {
					for _, volRatio := range volRatioRange {
						for _, emaFast := range emaFastRange {
							for _, emaSlow := range emaSlowRange {
								// 跳过不合理的参数组合
								if oversoldLong >= entryLong || overboughtShort <= entryShort || emaFast >= emaSlow {
									continue
								}

								strategyConfig := StrategyConfig{
									RSI_PERIOD:           14,
									RSI_OVERSOLD_LONG:    oversoldLong,
									RSI_ENTRY_LONG:       entryLong,
									RSI_OVERBOUGHT_SHORT: overboughtShort,
									RSI_ENTRY_SHORT:      entryShort,
									EMA_FAST:             emaFast,
									EMA_SLOW:             emaSlow,
									VOL_RATIO_THRESHOLD:  volRatio,
								}

								result := RunBacktest(klines, config, strategyConfig)

								results = append(results, OptimizeResult{
									Config:     strategyConfig,
									TotalPnL:   result.TotalPnL,
									WinRate:    result.WinRate,
									Trades:     result.TotalTrades,
									ProfitFactor: result.ProfitFactor,
								})

								count++
								if count%200 == 0 {
									fmt.Printf("进度: %d/%d\n", count, total)
								}
							}
						}
					}
				}
			}
		}
	}

	// 按盈亏排序
	sortResults(results)

	// 打印 Top 10
	fmt.Println("\n========== Top 10 参数组合 ==========")
	fmt.Println("排名 | 总盈亏 | 胜率 | 交易次数 | 盈亏比 | 参数")
	fmt.Println("-----|--------|------|----------|--------|------")
	for i, r := range results[:10] {
		fmt.Printf("%d | $%.2f | %.1f%% | %d | %.2f | long: %.0f->%.0f short: %.0f->%.0f vol=%.1f ema=%d/%d\n",
			i+1, r.TotalPnL, r.WinRate*100, r.Trades, r.ProfitFactor,
			r.Config.RSI_OVERSOLD_LONG, r.Config.RSI_ENTRY_LONG,
			r.Config.RSI_OVERBOUGHT_SHORT, r.Config.RSI_ENTRY_SHORT,
			r.Config.VOL_RATIO_THRESHOLD, r.Config.EMA_FAST, r.Config.EMA_SLOW)
	}
}

func sortResults(results []OptimizeResult) {
	// 按总盈亏降序排序
	for i := 0; i < len(results); i++ {
		for j := i + 1; j < len(results); j++ {
			if results[j].TotalPnL > results[i].TotalPnL {
				results[i], results[j] = results[j], results[i]
			}
		}
	}
}

// runOptimizeCmd 执行优化命令
func runOptimizeCmd(dbPath, symbol string, startTime, endTime int64) {
	log.Printf("加载 K 线数据: %s", symbol)
	klines, err := loadKlinesFromDB(dbPath, symbol, startTime, endTime)
	if err != nil {
		log.Fatalf("加载数据失败: %v", err)
	}
	log.Printf("加载 %d 根 1m K 线（超短线模式）", len(klines))

	if len(klines) < 100 {
		log.Fatalf("数据不足")
	}

	config := DefaultBacktestConfig
	config.Symbol = symbol

	RunOptimize(klines, config)
}
