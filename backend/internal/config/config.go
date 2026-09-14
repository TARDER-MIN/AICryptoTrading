// Package config loads runtime configuration from environment variables.
package config

import (
	"os"
	"strconv"
)

type Config struct {
	ServerPort      string
	DatabaseURL     string
	APIKey          string
	AnthropicAPIKey string
	AnthropicModel  string

	BingXAPIKey            string
	BingXAPISecret         string
	BingXRESTBaseURL       string
	BingXWSBaseURL         string
	BinanceResearchBaseURL string
	KlineInterval          string
	LeverageDefault        int
	MarginUSD              float64
	MarginType             string
	AutotradeEnabledInit   bool
	ListenKeyKeepaliveMin  int
	MaxAutoOrdersPerDay    int

	// Backtest costs are percentage points per executed side. The live
	// strategy enters at market and exits through market-trigger orders, so
	// the optimizer treats both legs as taker fills and adds an explicit
	// slippage estimate. These affect backtests only, never live order size.
	BacktestTakerFeePct float64
	BacktestSlippagePct float64

	// WatchlistAIRefreshHourUTC is the hour (0-23, UTC) the daily AI
	// watchlist-selection scheduler fires. No weekday skip - crypto
	// perpetuals trade every day.
	WatchlistAIRefreshHourUTC int

	// AISignalSystemPrompt/AIWatchlistSystemPrompt are the system-level
	// instructions for the two Claude calls (internal/ai.GenerateSignal /
	// SelectDailyWatchlist) - overridable via AI_SIGNAL_SYSTEM_PROMPT /
	// AI_WATCHLIST_SYSTEM_PROMPT in .env (multi-line heredoc syntax, see
	// .env.example) so prompt wording can be tuned without a rebuild.
	// Default to the values below when unset.
	AISignalSystemPrompt    string
	AIWatchlistSystemPrompt string
}

func Load() Config {
	return Config{
		ServerPort:      getenv("SERVER_PORT", "8280"),
		DatabaseURL:     getenv("DATABASE_URL", "postgres://cryptotrading:cryptotrading@localhost:55532/cryptotrading?sslmode=disable"),
		APIKey:          os.Getenv("API_KEY"),
		AnthropicAPIKey: os.Getenv("ANTHROPIC_API_KEY"),
		AnthropicModel:  getenv("ANTHROPIC_MODEL", "claude-sonnet-5"),

		BingXAPIKey:            os.Getenv("BINGX_API_KEY"),
		BingXAPISecret:         os.Getenv("BINGX_API_SECRET"),
		BingXRESTBaseURL:       getenv("BINGX_REST_BASE_URL", "https://open-api.bingx.com"),
		BingXWSBaseURL:         getenv("BINGX_WS_BASE_URL", "wss://open-api-swap.bingx.com/swap-market"),
		BinanceResearchBaseURL: getenv("BINANCE_RESEARCH_BASE_URL", "https://fapi.binance.com"),
		KlineInterval:          getenv("KLINE_INTERVAL", "5m"),
		LeverageDefault:        getenvInt("LEVERAGE_DEFAULT", 3),
		MarginUSD:              getenvFloat("MARGIN_USD", 5.0),
		MarginType:             getenv("MARGIN_TYPE", "ISOLATED"),
		AutotradeEnabledInit:   getenvBool("AUTOTRADE_ENABLED_DEFAULT", false),
		ListenKeyKeepaliveMin:  getenvInt("LISTEN_KEY_KEEPALIVE_MINUTES", 30),
		MaxAutoOrdersPerDay:    getenvInt("MAX_AUTO_ORDERS_PER_DAY", 20),
		BacktestTakerFeePct:    getenvNonNegativeFloat("BACKTEST_TAKER_FEE_PCT", 0.05),
		BacktestSlippagePct:    getenvNonNegativeFloat("BACKTEST_SLIPPAGE_PCT", 0.02),

		WatchlistAIRefreshHourUTC: getenvInt("WATCHLIST_AI_REFRESH_HOUR_UTC", 0),

		AISignalSystemPrompt:    getenv("AI_SIGNAL_SYSTEM_PROMPT", defaultAISignalSystemPrompt),
		AIWatchlistSystemPrompt: getenv("AI_WATCHLIST_SYSTEM_PROMPT", defaultAIWatchlistSystemPrompt),
	}
}

// defaultAISignalSystemPrompt is internal/ai.GenerateSignal's system prompt
// when AI_SIGNAL_SYSTEM_PROMPT isn't set in .env. Deliberately short (low
// token cost per call) and framework-agnostic - it doesn't name or explain
// ICT/Silver Bullet, just reviews whatever setup the rule engine detected.
const defaultAISignalSystemPrompt = "你是加密貨幣永續合約短線交易訊號審核助手。下方是規則引擎偵測到、機械條件都已符合的位置型HTF 3+1候選：H4結構與溢折價區域、H1外部流動性掃掠收回、M5確認擺動CHOCH與同位移Fresh FVG/OB首次回踩，以及1:1.5目標路徑都已通過。掃上方只找空、掃下方只找多。請判斷值不值得下單；有疑慮就HOLD，不得反向或改價。entry_hint/stop_loss/take_profit必須照抄規則價格、禁止填0。若有上次訊號或持倉一併判斷。務必呼叫emit_trade_signal工具。"

// defaultAIWatchlistSystemPrompt is internal/ai.SelectDailyWatchlist's
// system prompt when AI_WATCHLIST_SYSTEM_PROMPT isn't set in .env.
// Deliberately short (low token cost per call).
const defaultAIWatchlistSystemPrompt = "你是BingX永續合約選幣助手。請從候選清單中選出指定數量、recent_signal_count（近期規則實際偵測到訊號的次數）最高的合約，次要參考24h成交金額與資金費率。只能選清單內的合約，每檔給簡短理由並指出至少一項風險。務必呼叫工具回傳結果。"

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getenvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func getenvFloat(key string, fallback float64) float64 {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.ParseFloat(v, 64); err == nil {
			return n
		}
	}
	return fallback
}

func getenvNonNegativeFloat(key string, fallback float64) float64 {
	value := getenvFloat(key, fallback)
	if value < 0 {
		return fallback
	}
	return value
}

func getenvBool(key string, fallback bool) bool {
	if v := os.Getenv(key); v != "" {
		if b, err := strconv.ParseBool(v); err == nil {
			return b
		}
	}
	return fallback
}
