package main

import (
	"math"
)

// K线数据
type Kline struct {
	Timestamp int64
	Open      float64
	High      float64
	Low       float64
	Close     float64
	Volume    float64
}

// CalculateRSI 计算 RSI 指标
// period: RSI 周期，通常为 14
func CalculateRSI(klines []Kline, period int) []float64 {
	if len(klines) < period+1 {
		return nil
	}

	rsi := make([]float64, len(klines))

	// 计算价格变化
	changes := make([]float64, len(klines)-1)
	for i := 1; i < len(klines); i++ {
		changes[i-1] = klines[i].Close - klines[i-1].Close
	}

	// 计算 RSI
	for i := period; i < len(klines); i++ {
		var gains, losses float64
		for j := i - period; j < i; j++ {
			if changes[j] > 0 {
				gains += changes[j]
			} else {
				losses += math.Abs(changes[j])
			}
		}

		avgGain := gains / float64(period)
		avgLoss := losses / float64(period)

		if avgLoss == 0 {
			rsi[i] = 100
		} else {
			rs := avgGain / avgLoss
			rsi[i] = 100 - (100 / (1 + rs))
		}
	}

	return rsi
}

// CalculateVolatility 计算波动率（收益率标准差）
// period: 计算周期
// annualize: 是否年化（乘以 sqrt(365*24*12) 对于 5m 周期）
func CalculateVolatility(klines []Kline, period int, annualize bool) []float64 {
	if len(klines) < period+1 {
		return nil
	}

	volatility := make([]float64, len(klines))

	// 计算对数收益率
	returns := make([]float64, len(klines)-1)
	for i := 1; i < len(klines); i++ {
		returns[i-1] = math.Log(klines[i].Close / klines[i-1].Close)
	}

	// 计算滚动标准差
	for i := period; i < len(klines); i++ {
		mean := 0.0
		for j := i - period; j < i; j++ {
			mean += returns[j]
		}
		mean /= float64(period)

		variance := 0.0
		for j := i - period; j < i; j++ {
			variance += math.Pow(returns[j]-mean, 2)
		}
		variance /= float64(period)

		volatility[i] = math.Sqrt(variance)
		if annualize {
			// 5分钟周期，一年约 105120 根 K 线
			volatility[i] *= math.Sqrt(105120)
		}
	}

	return volatility
}

// CalculateVolumeMA 计算成交量移动平均
func CalculateVolumeMA(klines []Kline, period int) []float64 {
	if len(klines) < period {
		return nil
	}

	ma := make([]float64, len(klines))

	for i := period - 1; i < len(klines); i++ {
		var sum float64
		for j := i - period + 1; j <= i; j++ {
			sum += klines[j].Volume
		}
		ma[i] = sum / float64(period)
	}

	return ma
}

// VolumeRatio 计算当前成交量与均量的比值
func VolumeRatio(klines []Kline, period int) []float64 {
	ma := CalculateVolumeMA(klines, period)
	if ma == nil {
		return nil
	}

	ratio := make([]float64, len(klines))
	for i := 0; i < len(klines); i++ {
		if ma[i] > 0 {
			ratio[i] = klines[i].Volume / ma[i]
		}
	}

	return ratio
}

// CalculateEMA 计算 EMA
func CalculateEMA(klines []Kline, period int) []float64 {
	if len(klines) < period {
		return nil
	}

	ema := make([]float64, len(klines))
	multiplier := 2.0 / float64(period+1)

	// 第一个 EMA 用 SMA 初始化
	var sum float64
	for i := 0; i < period; i++ {
		sum += klines[i].Close
	}
	ema[period-1] = sum / float64(period)

	// 后续用 EMA 公式
	for i := period; i < len(klines); i++ {
		ema[i] = (klines[i].Close-ema[i-1])*multiplier + ema[i-1]
	}

	return ema
}

// Signal 表示交易信号
type Signal int

const (
	SignalNone Signal = iota
	SignalLong
	SignalShort
	SignalCloseLong
	SignalCloseShort
)

// StrategyConfig 策略参数
type StrategyConfig struct {
	RSI_PERIOD          int     // RSI 周期
	RSI_OVERSOLD        float64 // RSI 超卖阈值
	RSI_OVERBOUGHT      float64 // RSI 超买阈值
	RSI_ENTRY           float64 // RSI 入场阈值
	EMA_PERIOD          int     // EMA 周期
	VOL_RATIO_THRESHOLD float64 // 成交量倍数阈值
}

// DefaultConfig 默认参数
var DefaultConfig = StrategyConfig{
	RSI_PERIOD:          14,
	RSI_OVERSOLD:        30,  // RSI < 30 超卖
	RSI_OVERBOUGHT:      70,  // RSI > 70 超买
	RSI_ENTRY:           35,  // RSI 反弹到 35 可入场
	EMA_PERIOD:          20,  // EMA20 确认趋势
	VOL_RATIO_THRESHOLD: 1.5, // 成交量放大 50%
}

// TrendState 趋势状态
type TrendState int

const (
	TrendNone TrendState = iota
	TrendUp         // 上升趋势
	TrendDown       // 下降趋势
)

// GenerateSignal 生成交易信号 - 反转后的趋势策略
// 逻辑：RSI 超卖反弹 + EMA 确认趋势 + 成交量放大
func GenerateSignal(klines []Kline, config StrategyConfig) Signal {
	n := len(klines)
	if n < config.RSI_PERIOD+2 || n < config.EMA_PERIOD+1 {
		return SignalNone
	}

	rsi := CalculateRSI(klines, config.RSI_PERIOD)
	ema := CalculateEMA(klines, config.EMA_PERIOD)
	volRatio := VolumeRatio(klines, config.RSI_PERIOD)

	if rsi == nil || ema == nil || volRatio == nil {
		return SignalNone
	}

	currentRSI := rsi[n-1]
	prevRSI := rsi[n-2]
	currentClose := klines[n-1].Close
	currentEMA := ema[n-1]
	currentVolRatio := volRatio[n-1]

	// 成交量放大
	volumeOK := currentVolRatio >= config.VOL_RATIO_THRESHOLD

	// === 做多信号 ===
	// 1. RSI 从超卖区反弹（之前 < 30，现在 >= 35）
	// 2. 价格突破 EMA（收盘价 > EMA）
	// 3. 成交量放大
	rsiBull := prevRSI < config.RSI_OVERSOLD && currentRSI >= config.RSI_ENTRY
	emaBull := currentClose > currentEMA && klines[n-1].High > klines[n-2].High

	if rsiBull && emaBull && volumeOK {
		return SignalLong
	}

	// === 做空信号 ===
	// 1. RSI 从超买区回落（之前 > 70，现在 <= 65）
	// 2. 价格跌破 EMA（收盘价 < EMA）
	// 3. 成交量放大
	rsiBear := prevRSI > config.RSI_OVERBOUGHT && currentRSI <= 65
	emaBear := currentClose < currentEMA && klines[n-1].Low < klines[n-2].Low

	if rsiBear && emaBear && volumeOK {
		return SignalShort
	}

	return SignalNone
}

// GenerateExitSignal 生成出场信号 - 趋势衰竭
func GenerateExitSignal(klines []Kline, config StrategyConfig, positionSide string) Signal {
	n := len(klines)
	if n < config.RSI_PERIOD+1 {
		return SignalNone
	}

	rsi := CalculateRSI(klines, config.RSI_PERIOD)
	if rsi == nil {
		return SignalNone
	}

	currentRSI := rsi[n-1]
	prevRSI := rsi[n-2]

	// 多头出场：趋势衰竭
	if positionSide == "LONG" {
		// RSI 从高位回落，跌破 50 中性线
		if prevRSI >= 50 && currentRSI < 50 {
			return SignalCloseLong
		}
	}

	// 空头出场：趋势衰竭
	if positionSide == "SHORT" {
		// RSI 从低位反弹，突破 50 中性线
		if prevRSI <= 50 && currentRSI > 50 {
			return SignalCloseShort
		}
	}

	return SignalNone
}
