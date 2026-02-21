package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/hstcscolor/wex/binance"
)

// Config 配置
type Config struct {
	ApiKey    string `json:"api_key"`
	SecretKey string `json:"secret_key"`
	Symbol    string `json:"symbol"`
	// 策略参数（多空分开）
	RSI_PERIOD           int     `json:"rsi_period"`
	RSI_OVERSOLD_LONG    float64 `json:"rsi_oversold_long"`
	RSI_ENTRY_LONG       float64 `json:"rsi_entry_long"`
	RSI_OVERBOUGHT_SHORT float64 `json:"rsi_overbought_short"`
	RSI_ENTRY_SHORT      float64 `json:"rsi_entry_short"`
	EMA_FAST             int     `json:"ema_fast"`
	EMA_SLOW             int     `json:"ema_slow"`
	VOL_RATIO_THRESHOLD  float64 `json:"vol_ratio_threshold"`
	// 交易参数
	PositionSize float64 `json:"position_size"`
	Leverage     int     `json:"leverage"`
	// 运行参数
	DryRun bool `json:"dry_run"`
}

// DefaultConfig 默认配置（短线投机，5倍杠杆）
var defaultConfig = Config{
	Symbol:               "BTCUSDT",
	RSI_PERIOD:           14,
	RSI_OVERSOLD_LONG:    45,
	RSI_ENTRY_LONG:       50,
	RSI_OVERBOUGHT_SHORT: 55,
	RSI_ENTRY_SHORT:      50,
	EMA_FAST:             7,
	EMA_SLOW:             20,
	VOL_RATIO_THRESHOLD:  1.5,
	PositionSize:         0.5,
	Leverage:             5,
	DryRun:               true,
}

// LoadConfig 加载配置
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	config := defaultConfig
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, err
	}

	return &config, nil
}

// SaveConfig 保存配置
func SaveConfig(path string, config *Config) error {
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0644)
}

// Strategy 策略实例
type Strategy struct {
	config  *Config
	client  *binance.BinFuture
	klines  []Kline
	running bool
}

// NewStrategy 创建策略实例
func NewStrategy(config *Config) (*Strategy, error) {
	s := &Strategy{
		config: config,
	}

	// 如果有 API Key，初始化客户端
	if config.ApiKey != "" && config.SecretKey != "" {
		s.client = binance.NewBinFutureFromKey(config.ApiKey, config.SecretKey)
		if s.client == nil {
			return nil, fmt.Errorf("failed to create binance client")
		}
	}

	return s, nil
}

// fetchKlines 获取 K 线数据
func (s *Strategy) fetchKlines() error {
	if s.client == nil {
		return fmt.Errorf("client not initialized")
	}

	// 获取最近 100 根 5m K 线
	klines, err := s.client.FutureKline(s.config.Symbol, "5m", 0, 0, 100)
	if err != nil {
		return err
	}

	s.klines = nil
	for _, k := range klines {
		s.klines = append(s.klines, Kline{
			Timestamp: k.Timestamp,
			Open:      k.Open,
			High:      k.High,
			Low:       k.Low,
			Close:     k.Close,
			Volume:    k.Amount,
		})
	}

	return nil
}

// executeSignal 执行交易信号
func (s *Strategy) executeSignal(signal Signal) error {
	if s.client == nil || s.config.DryRun {
		log.Printf("[DRY-RUN] Signal: %v", signal)
		return nil
	}

	// 获取当前价格
	ticker, err := s.client.FutureTicker(s.config.Symbol)
	if err != nil {
		return err
	}

	// 获取账户余额
	account, err := s.client.FutureGetAccount()
	if err != nil {
		return err
	}

	asset, err := account.GetAsset("USDT")
	if err != nil {
		return err
	}

	// 计算仓位大小
	balance := 0.0
	if asset != nil {
		balance = float64(0)
		// 解析余额字符串
	}

	notional := balance * s.config.PositionSize
	amount := notional / ticker.Price

	switch signal {
	case SignalLong:
		log.Printf("开多仓: %.4f @ %.2f", amount, ticker.Price)
		_, err = s.client.FutureOpenLongMarket(s.config.Symbol, notional)
	case SignalShort:
		log.Printf("开空仓: %.4f @ %.2f", amount, ticker.Price)
		_, err = s.client.FutureOpenShortMarket(s.config.Symbol, notional)
	case SignalCloseLong:
		log.Printf("平多仓")
		// 需要查询当前持仓
	case SignalCloseShort:
		log.Printf("平空仓")
		// 需要查询当前持仓
	}

	return err
}

// Run 运行策略
func (s *Strategy) Run() error {
	s.running = true
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	// 首次获取数据
	if err := s.fetchKlines(); err != nil {
		return err
	}

	log.Printf("策略启动，监控 %s", s.config.Symbol)

	for {
		select {
		case <-ticker.C:
			if err := s.fetchKlines(); err != nil {
				log.Printf("获取 K 线失败: %v", err)
				continue
			}

			// 生成信号
			strategyConfig := StrategyConfig{
				RSI_PERIOD:           s.config.RSI_PERIOD,
				RSI_OVERSOLD_LONG:    s.config.RSI_OVERSOLD_LONG,
				RSI_ENTRY_LONG:       s.config.RSI_ENTRY_LONG,
				RSI_OVERBOUGHT_SHORT: s.config.RSI_OVERBOUGHT_SHORT,
				RSI_ENTRY_SHORT:      s.config.RSI_ENTRY_SHORT,
				EMA_FAST:             s.config.EMA_FAST,
				EMA_SLOW:             s.config.EMA_SLOW,
				VOL_RATIO_THRESHOLD:  s.config.VOL_RATIO_THRESHOLD,
			}

			signal := GenerateSignal(s.klines, strategyConfig)

			// 执行信号
			if signal != SignalNone {
				log.Printf("信号: %v", signal)
				if err := s.executeSignal(signal); err != nil {
					log.Printf("执行失败: %v", err)
				}
			}

			// 打印当前指标
			if len(s.klines) > 0 {
				rsi := CalculateRSI(s.klines, strategyConfig.RSI_PERIOD)
				vol := CalculateVolatility(s.klines, strategyConfig.RSI_PERIOD, false)
				volRatio := VolumeRatio(s.klines, strategyConfig.RSI_PERIOD)

				lastK := s.klines[len(s.klines)-1]
				var currentRSI, currentVol, currentVolRatio float64
				if rsi != nil {
					currentRSI = rsi[len(rsi)-1]
				}
				if vol != nil {
					currentVol = vol[len(vol)-1]
				}
				if volRatio != nil {
					currentVolRatio = volRatio[len(volRatio)-1]
				}

				log.Printf("[%s] Close: %.2f | RSI: %.1f | Vol: %.4f | VolRatio: %.2f",
					time.Unix(lastK.Timestamp, 0).Format("15:04"),
					lastK.Close,
					currentRSI,
					currentVol,
					currentVolRatio,
				)
			}
		}
	}
}

// Stop 停止策略
func (s *Strategy) Stop() {
	s.running = false
}

func main() {
	// 命令行参数
	mode := flag.String("mode", "run", "运行模式: run, backtest, optimize")
	configPath := flag.String("config", "config.json", "配置文件路径")
	dbPath := flag.String("db", "", "K线数据库路径 (回测模式)")
	symbol := flag.String("symbol", "BTCUSDT", "交易对")
	flag.Parse()

	switch *mode {
	case "run":
		// 加载配置
		config, err := LoadConfig(*configPath)
		if err != nil {
			// 配置文件不存在，使用默认配置
			config = &defaultConfig
			if err := SaveConfig(*configPath, config); err != nil {
				log.Printf("保存默认配置失败: %v", err)
			}
			log.Printf("创建默认配置文件: %s", *configPath)
		}

		config.Symbol = *symbol
		// 实盘运行
		strategy, err := NewStrategy(config)
		if err != nil {
			log.Fatalf("创建策略失败: %v", err)
		}

		// 信号处理
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

		go func() {
			<-sigChan
			log.Println("收到退出信号...")
			strategy.Stop()
			os.Exit(0)
		}()

		if err := strategy.Run(); err != nil {
			log.Fatalf("运行失败: %v", err)
		}

	case "backtest":
		// 回测模式 - 不需要配置文件
		if *dbPath == "" {
			*dbPath = "../binance-klines/klines.db"
		}

		// 回测全部数据
		var startTime, endTime int64
		endTime = 0 // 0 表示不限制
		startTime = 0

		runBacktestCmd(*dbPath, *symbol, startTime, endTime)

	case "optimize":
		// 参数优化
		if *dbPath == "" {
			*dbPath = "../binance-klines/klines.db"
		}

		var startTime, endTime int64
		runOptimizeCmd(*dbPath, *symbol, startTime, endTime)

	default:
		log.Fatalf("未知模式: %s", *mode)
	}
}
