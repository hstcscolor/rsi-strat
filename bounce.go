package main

import (
	"fmt"
	"log"
	"time"
)

// BounceConfig 反弹策略配置
type BounceConfig struct {
	Symbol          string
	StartBalance    float64
	FeeRate         float64
	Leverage        float64
	// 下跌检测
	DropLookback    int     // 检测下跌的 K 线数量
	DropThreshold   float64 // 下跌阈值（如 0.015 = 1.5%）
	// 入场
	RSIOversold     float64 // RSI 超卖阈值
	RSIEntry        float64 // RSI 反弹入场阈值
	// 建仓
	FirstBatchSize  float64 // 第1份仓位（10%）
	OtherBatchSize  float64 // 其他份仓位（15%）
	BatchInterval   int64   // 加仓间隔（秒）
	MaxBatches      int     // 最大批次（7份）
	// 出场
	BounceTarget    float64 // 反弹目标比例（0.25 = 25%）
	ProfitThreshold float64 // 分批止盈触发（0.70 = 70%）
	StartExitTime   int64   // 开始减仓时间（秒）
	ExitInterval    int64   // 减仓间隔（秒）
	ExitPercent     float64 // 每次减仓比例（0.20 = 20%）
	MaxHoldTime     int64   // 最大持仓时间（秒）
	RSIExit         float64 // RSI 止损阈值
}

// DefaultBounceConfig 默认配置（平衡版）
var DefaultBounceConfig = BounceConfig{
	Symbol:          "BTCUSDT",
	StartBalance:    10000,
	FeeRate:         0.0004,
	Leverage:        5,
	DropLookback:    45,
	DropThreshold:   0.012,  // 1.2%
	RSIOversold:     32,
	RSIEntry:        38,
	FirstBatchSize:  0.12,
	OtherBatchSize:  0.13,
	BatchInterval:   180,
	MaxBatches:      7,
	BounceTarget:    0.25,
	ProfitThreshold: 0.50,
	StartExitTime:   600,
	ExitInterval:    180,
	ExitPercent:     0.25,
	MaxHoldTime:     2700,   // 45分钟
	RSIExit:         32,
}

// BouncePosition 反弹策略仓位
type BouncePosition struct {
	side           string
	entryTime      int64
	lowPrice       float64  // 低点价格
	highPrice      float64  // 下跌前高点
	targetPrice    float64  // 目标价 = low + (high-low) × 25%
	entries        []BounceEntry
	totalAmt       float64
	avgPrice       float64
	lastBatchTime  int64    // 上次加仓时间
	batchCount     int      // 当前批次
	startExitTime  int64    // 开始减仓时间
	exitCount      int      // 减仓次数
}

// BounceEntry 入场记录
type BounceEntry struct {
	entryTime  int64
	entryPrice float64
	amount     float64
	batch      int
}

// BounceTrade 交易记录
type BounceTrade struct {
	EntryTime  int64
	ExitTime   int64
	Side       string
	EntryPrice float64
	ExitPrice  float64
	Amount     float64
	PnL        float64
	Fee        float64
	Reason     string
}

// BounceResult 回测结果
type BounceResult struct {
	TotalTrades  int
	WinTrades    int
	LoseTrades   int
	TotalPnL     float64
	TotalFees    float64
	WinRate      float64
	ProfitFactor float64
	MaxDrawdown  float64
	Trades       []BounceTrade
	BalanceCurve []float64
}

// RunBounceBacktest 执行反弹策略回测
func RunBounceBacktest(klines []Kline, config BounceConfig) *BounceResult {
	result := &BounceResult{
		BalanceCurve: []float64{config.StartBalance},
	}

	n := len(klines)
	if n < config.DropLookback+20 {
		return result
	}

	// 计算指标
	rsi := CalculateRSI(klines, 14)
	ema5 := CalculateEMA(klines, 5)
	ema13 := CalculateEMA(klines, 13)

	balance := config.StartBalance
	var position *BouncePosition
	maxBalance := balance

	for i := config.DropLookback; i < n; i++ {
		k := klines[i]
		currentRSI := rsi[i]
		prevRSI := rsi[i-1]

		// ========== 检测下跌 ==========
		// 找最近 config.DropLookback 根 K 线的最高价和最低价
		highPrice := klines[i-1].High
		lowPrice := klines[i-1].Low
		for j := 2; j <= config.DropLookback && i-j >= 0; j++ {
			if klines[i-j].High > highPrice {
				highPrice = klines[i-j].High
			}
			if klines[i-j].Low < lowPrice {
				lowPrice = klines[i-j].Low
			}
		}

		// 计算跌幅
		dropPercent := (highPrice - lowPrice) / highPrice
		hasDrop := dropPercent >= config.DropThreshold

		// 趋势判断
		uptrend := ema5[i] > ema13[i]

		// ========== 出场逻辑 ==========
		if position != nil {
			shouldClose := false
			closeReason := ""

			// 1. RSI 止损
			if currentRSI < config.RSIExit {
				shouldClose = true
				closeReason = "RSI止损"
			}

			// 2. 最大持仓时间
			holdTime := k.Timestamp - position.entryTime
			if holdTime >= config.MaxHoldTime {
				shouldClose = true
				closeReason = "最大持仓时间"
			}

			// 3. 分批止盈逻辑
			timeSinceEntry := k.Timestamp - position.entryTime
			currentBounce := (k.Close - position.lowPrice) / (position.highPrice - position.lowPrice)
			
			// 检查是否应该开始分批平仓
			if timeSinceEntry >= config.StartExitTime && currentBounce >= config.ProfitThreshold {
				// 检查是否到达下一个减仓时间点
				timeSinceExitStart := timeSinceEntry - config.StartExitTime
				expectedExitCount := int(timeSinceExitStart/config.ExitInterval) + 1
				
				if expectedExitCount > position.exitCount {
					// 执行减仓
					closePercent := config.ExitPercent
					closeAmt := position.totalAmt * closePercent
					
					// 从最早的仓位开始平
					var newEntries []BounceEntry
					closed := 0.0
					for _, entry := range position.entries {
						if closed < closeAmt && entry.amount > 0 {
							closeThis := entry.amount
							if closed+closeThis > closeAmt {
								closeThis = closeAmt - closed
								// 保留剩余
								newEntries = append(newEntries, BounceEntry{
									entryTime:  entry.entryTime,
									entryPrice: entry.entryPrice,
									amount:     entry.amount - closeThis,
									batch:      entry.batch,
								})
							}
							closed += closeThis

							// 记录交易
							trade := BounceTrade{
								EntryTime:  entry.entryTime,
								ExitTime:   k.Timestamp,
								Side:       position.side,
								EntryPrice: entry.entryPrice,
								ExitPrice:  k.Close,
								Amount:     closeThis,
								Fee:        (entry.entryPrice + k.Close) * closeThis * config.FeeRate,
								Reason:     fmt.Sprintf("分批止盈#%d(%.1f%%)", position.exitCount+1, currentBounce*100),
							}
							if position.side == "LONG" {
								trade.PnL = (k.Close - entry.entryPrice) * closeThis
							} else {
								trade.PnL = (entry.entryPrice - k.Close) * closeThis
							}
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

					position.entries = newEntries
					position.totalAmt = 0
					for _, e := range newEntries {
						position.totalAmt += e.amount
					}
					position.exitCount++

					// 如果仓位已空，清空持仓
					if position.totalAmt < 0.0001 {
						shouldClose = true
						closeReason = "分批止盈完成"
					}
				}
			}

			// 执行全平
			if shouldClose && len(position.entries) > 0 {
				for _, entry := range position.entries {
					if entry.amount <= 0 {
						continue
					}
					trade := BounceTrade{
						EntryTime:  entry.entryTime,
						ExitTime:   k.Timestamp,
						Side:       position.side,
						EntryPrice: entry.entryPrice,
						ExitPrice:  k.Close,
						Amount:     entry.amount,
						Fee:        (entry.entryPrice + k.Close) * entry.amount * config.FeeRate,
						Reason:     closeReason,
					}
					if position.side == "LONG" {
						trade.PnL = (k.Close - entry.entryPrice) * entry.amount
					} else {
						trade.PnL = (entry.entryPrice - k.Close) * entry.amount
					}
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
			}
		}

		// ========== 建仓逻辑 ==========
		if position == nil {
			// 检测入场条件：下跌 + 低点确认 + 反弹确认
			if hasDrop && prevRSI < config.RSIOversold && currentRSI >= config.RSIEntry && uptrend {
				// 计算目标价
				targetPrice := lowPrice + (highPrice-lowPrice)*config.BounceTarget

				// 第1份入场
				notional := balance * config.FirstBatchSize
				amount := notional / k.Close

				position = &BouncePosition{
					side:          "LONG",
					entryTime:     k.Timestamp,
					lowPrice:      lowPrice,
					highPrice:     highPrice,
					targetPrice:   targetPrice,
					entries: []BounceEntry{{
						entryTime:  k.Timestamp,
						entryPrice: k.Close,
						amount:     amount,
						batch:      1,
					}},
					totalAmt:      amount,
					avgPrice:      k.Close,
					lastBatchTime: k.Timestamp,
					batchCount:    1,
				}
				balance -= k.Close * amount * config.FeeRate
			}
		} else {
			// ========== 加仓逻辑 ==========
			if position.batchCount < config.MaxBatches {
				timeSinceLastBatch := k.Timestamp - position.lastBatchTime
				
				// 每 3 分钟检查一次加仓
				if timeSinceLastBatch >= config.BatchInterval {
					// 检查加仓条件：RSI > 入场阈值 且 EMA 上升
					if currentRSI >= config.RSIEntry && uptrend {
						notional := balance * config.OtherBatchSize
						amount := notional / k.Close

						position.entries = append(position.entries, BounceEntry{
							entryTime:  k.Timestamp,
							entryPrice: k.Close,
							amount:     amount,
							batch:      position.batchCount + 1,
						})
						position.totalAmt += amount
						position.avgPrice = (position.avgPrice*(position.totalAmt-amount) + k.Close*amount) / position.totalAmt
						position.lastBatchTime = k.Timestamp
						position.batchCount++
						balance -= k.Close * amount * config.FeeRate
					}
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

// PrintBounceResult 打印反弹策略结果
func PrintBounceResult(result *BounceResult) {
	fmt.Println("\n========== 反弹策略回测结果 ==========")
	fmt.Printf("总交易次数: %d\n", result.TotalTrades)
	fmt.Printf("盈利次数: %d\n", result.WinTrades)
	fmt.Printf("亏损次数: %d\n", result.LoseTrades)
	fmt.Printf("胜率: %.2f%%\n", result.WinRate*100)
	fmt.Printf("总盈亏: $%.2f\n", result.TotalPnL)
	fmt.Printf("总手续费: $%.2f\n", result.TotalFees)
	fmt.Printf("盈亏比: %.2f\n", result.ProfitFactor)
	fmt.Printf("最大回撤: %.2f%%\n", result.MaxDrawdown*100)
	fmt.Println("================================")
}

// runBounceBacktestCmd 执行反弹策略回测命令
func runBounceBacktestCmd(dbPath, symbol string, startTime, endTime int64) {
	log.Printf("加载 K 线数据: %s", symbol)
	klines, err := loadKlinesFromDB(dbPath, symbol, startTime, endTime)
	if err != nil {
		log.Fatalf("加载数据失败: %v", err)
	}
	log.Printf("加载 %d 根 1m K 线（反弹策略）", len(klines))

	if len(klines) < 100 {
		log.Fatalf("数据不足")
	}

	config := DefaultBounceConfig
	config.Symbol = symbol

	result := RunBounceBacktest(klines, config)
	PrintBounceResult(result)

	// 打印最近的交易
	fmt.Println("\n最近 10 笔交易:")
	for i := len(result.Trades) - 1; i >= 0 && i >= len(result.Trades)-10; i-- {
		t := result.Trades[i]
		fmt.Printf("%s | 入场: %.2f | 出场: %.2f | 盈亏: $%.2f | %s\n",
			time.Unix(t.EntryTime, 0).Format("2006-01-02 15:04"),
			t.EntryPrice,
			t.ExitPrice,
			t.PnL,
			t.Reason,
		)
	}
}
