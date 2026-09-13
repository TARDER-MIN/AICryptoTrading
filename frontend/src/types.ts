// Mirrors backend/internal/models. Keep field names/casing in sync with the
// Go JSON tags.

export interface Candle {
  symbol: string;
  ts: string;
  open: number;
  high: number;
  low: number;
  close: number;
  volume: number;
}

export type SignalAction = "BUY" | "SELL" | "HOLD";

export interface AISignal {
  id: number;
  symbol: string;
  candle_ts?: string;
  action: SignalAction;
  confidence: number;
  entry_hint?: number;
  stop_loss?: number;
  take_profit?: number;
  funding_rate?: number;
  // The Silver Bullet setup (liquidity sweep + Fair Value Gap) this signal
  // evaluated - used to draw sweep/FVG markers on the chart.
  sweep_ts?: string;
  sweep_price?: number;
  fvg_ts?: string;
  fvg_low?: number;
  fvg_high?: number;
  // ICT-2026 upgrade: Optimal Trade Entry zone, Breaker Block (Unicorn
  // Model) confluence zone, and SMT divergence confirmation against a
  // correlated anchor symbol.
  ote_low?: number;
  ote_high?: number;
  breaker_low?: number;
  breaker_high?: number;
  smt_anchor_symbol?: string;
  smt_confirmed?: boolean;
  rationale: string;
  model: string;
  created_at: string;
}

export type PositionSide = "long" | "short";

export interface Position {
  symbol: string;
  side: PositionSide;
  qty: number;
  entry_price: number;
  mark_price: number;
  unrealized_pnl: number;
  leverage: number;
  liquidation_price: number;
  margin_type: string;
  updated_at: string;
}

export interface AccountSummary {
  wallet_balance_usd: number;
  available_balance_usd: number;
  total_unrealized_pnl: number;
  updated_at: string;
}

export type OrderSide = "BUY" | "SELL";
// Order statuses cover both regular orders and algo (STOP_MARKET/
// TAKE_PROFIT_MARKET) orders - algo orders additionally use "TRIGGERED"
// as an in-between state before FILLED.
export type OrderStatus = "NEW" | "PARTIALLY_FILLED" | "TRIGGERED" | "FILLED" | "CANCELED" | "EXPIRED" | "REJECTED";
export type OrderSource = "manual" | "auto";

export interface Order {
  id: number;
  binance_order_id?: number;
  // Set instead of binance_order_id for a stop-loss/take-profit order
  // (BingX's Algo Order service - a separate ID space).
  algo_id?: number;
  client_order_id?: string;
  symbol: string;
  side: OrderSide;
  order_type: string;
  reduce_only: boolean;
  qty: number;
  notional_usd: number;
  leverage: number;
  status: OrderStatus;
  source: OrderSource;
  filled_price?: number;
  filled_at?: string;
  submitted_at: string;
  updated_at: string;
}

export type AutoTradeDecision = "executed" | "skipped" | "hold" | "error";

export interface AutoTradeLogEntry {
  id: number;
  symbol: string;
  ts: string;
  rule_action: SignalAction;
  rule_reason: string;
  ai_signal_id?: number;
  decision: AutoTradeDecision;
  skip_reason?: string;
  order_id?: number;
}

export interface Settings {
  autotrade_enabled: boolean;
  leverage: number;
  // Fixed margin committed per order; notional = margin_usd * leverage
  // (no separate notional ceiling - scales with leverage by design).
  margin_usd: number;
  // Read-only convenience value (margin_usd * leverage), computed server-side.
  effective_notional_usd: number;
  margin_type: string;
  updated_at: string;
}

export interface WatchlistItem {
  id: number;
  symbol: string;
  enabled: boolean;
  added_at: string;
  price?: number;
  min_notional_usd?: number;
  step_size?: number;
  max_qty_at_cap?: number;
  tradable_at_cap: boolean;
  funding_rate?: number;
}

export interface WSMessage<T = unknown> {
  type: "candle" | "signal" | "order" | "position" | "balance" | "autotrade_log" | "funding" | "settings";
  data: T;
}

// --- Silver Bullet strategy / backtest optimization (ICT 2026 upgrade) ---
// No time-of-day session gating (e.g. ICT's classic NY 10-11am/2-3pm
// windows) - by explicit user choice, since BingX perpetuals trade 24/7.
// A setup is evaluated whenever it occurs: sweep -> displacement-quality FVG
// -> Optimal Trade Entry (OTE) retracement -> Breaker Block (Unicorn Model)
// confluence -> SMT divergence confirmation against a correlated anchor.

export interface SBParams {
  swing_lookback: number;
  min_fvg_size_pct: number;
  max_bars_for_sweep: number;
  stop_buffer_pct: number;
  risk_reward_ratio: number;
  // ICT-2026 fixed structural thresholds (not grid-searched by optimize).
  min_displacement_body_pct: number;
  max_opposing_wick_pct: number;
  ote_min_retrace: number;
  ote_max_retrace: number;
  max_bars_for_ote: number;
  breaker_lookback: number;
  require_breaker_confluence: boolean;
  require_smt_divergence: boolean;
}

export interface BacktestMetrics {
  total_trades: number;
  win_count: number;
  win_rate: number;
  target_exits: number;
  stop_exits: number;
  end_of_data_exits: number;
  gross_return_pct: number;
  fee_cost_pct: number;
  slippage_cost_pct: number;
  funding_cost_pct: number;
  total_trading_cost_pct: number;
  total_return_pct: number;
  sharpe: number;
  max_drawdown_pct: number;
  profit_factor: number;
  taker_fee_pct_per_side: number;
  estimated_slippage_pct_per_side: number;
  funding_included: boolean;
  funding_source: string;
  fee_source: string;
}

export interface StrategyParamsResponse {
  params: SBParams;
  train_metrics: BacktestMetrics | null;
  validation_metrics: BacktestMetrics | null;
  test_metrics: BacktestMetrics | null;
  last_report: OptimizeReport | null;
  optimized_at: string | null;
}

export interface OptimizeCandidate {
  params: SBParams;
  train: BacktestMetrics;
  validation: BacktestMetrics;
}

export interface BacktestBreakdown {
  label: string;
  metrics: BacktestMetrics;
}

export interface FinalTestDiagnostics {
  completed_only_metrics: BacktestMetrics;
  by_symbol: BacktestBreakdown[];
  by_side: BacktestBreakdown[];
  by_day: BacktestBreakdown[];
}

export interface WalkForwardFold {
  index: number;
  selection_start: string;
  selection_end: string;
  test_start: string;
  test_end: string;
  selected_params: SBParams;
  candidates_passed: number;
  used_fallback: boolean;
  selection_metrics: BacktestMetrics;
  test_metrics: BacktestMetrics;
  passed: boolean;
}

export interface WalkForwardReport {
  folds: WalkForwardFold[];
  aggregate_metrics: BacktestMetrics;
  passed_folds: number;
  total_folds: number;
}

export interface RobustSelectionFold {
  index: number;
  test_start: string;
  test_end: string;
  test_metrics: BacktestMetrics;
  passed: boolean;
}

export interface RobustSelectionReport {
  selected_params: SBParams;
  folds: RobustSelectionFold[];
  aggregate_metrics: BacktestMetrics;
  by_side: BacktestBreakdown[];
  passed_folds: number;
  total_folds: number;
  candidates_passed: number;
  used_fallback: boolean;
}

export interface OptimizeReport {
  best_params: SBParams;
  train_metrics: BacktestMetrics;
  validation_metrics: BacktestMetrics;
  test_metrics: BacktestMetrics;
  cost_model: {
    taker_fee_pct_per_side: number;
    estimated_slippage_pct_per_side: number;
    funding_included: boolean;
    funding_source: string;
    fee_source: string;
  };
  top_candidates: OptimizeCandidate[];
  candidates_evaluated: number;
  candidates_passed: number;
  train_end: string;
  test_start: string;
  history_days: number;
  history_available_days: number;
  symbols_tested: string[];
  history_start: string;
  history_end: string;
  candles_tested: number;
  final_test_diagnostics: FinalTestDiagnostics;
  robust_selection?: RobustSelectionReport;
  walk_forward: WalkForwardReport;
  params_applied?: boolean;
  apply_blockers?: string[];
}

export interface FundingUpdate {
  symbol: string;
  mark_price: number;
  funding_rate: number;
  next_funding_time: string | number;
}

// --- AI daily watchlist selection (internal/watchlistai) ---
export interface WatchlistAIPick {
  rank: number;
  symbol: string;
  rationale: string;
  price: number;
  change_pct_24h: number;
  quote_volume_24h: number;
  // How many Silver Bullet trades the currently-live ICT-2026 rules
  // actually produced for this symbol over the recent lookback window -
  // the primary reason it was picked (see internal/watchlistai).
  recent_signal_count: number;
}

export interface WatchlistAIResult {
  run_date: string | null;
  candidate_count?: number;
  picks: WatchlistAIPick[];
  created_at?: string;
}
