import type {
  AccountSummary,
  AISignal,
  AutoTradeLogEntry,
  Candle,
  OptimizeReport,
  Order,
  OrderSide,
  Position,
  Settings,
  StrategyParamsResponse,
  WatchlistAIResult,
  WatchlistItem,
} from "../types";

const BASE_URL = `${import.meta.env.VITE_API_BASE_URL ?? "http://localhost:8280"}/api`;

async function request<T>(path: string, init?: RequestInit): Promise<T> {
  const res = await fetch(`${BASE_URL}${path}`, {
    ...init,
    headers: {
      "Content-Type": "application/json",
      ...init?.headers,
    },
  });
  if (!res.ok) {
    const body = await res.text();
    throw new Error(`${res.status} ${res.statusText}: ${body}`);
  }
  return res.json() as Promise<T>;
}

export const api = {
  getCandles: (symbol: string, limit = 200) =>
    request<Candle[]>(`/market/candles?symbol=${encodeURIComponent(symbol)}&limit=${limit}`),
  getFunding: (symbol: string) =>
    request<{ symbol: string; mark_price: number; funding_rate: number; next_funding_time: number | string }>(
      `/market/funding?symbol=${encodeURIComponent(symbol)}`,
    ),

  getWatchlist: () => request<WatchlistItem[]>("/watchlist"),
  addWatchlist: (symbol: string) =>
    request<{ ok: boolean }>("/watchlist", { method: "POST", body: JSON.stringify({ symbol }) }),
  removeWatchlist: (symbol: string) =>
    request<{ ok: boolean }>(`/watchlist/${encodeURIComponent(symbol)}`, { method: "DELETE" }),

  // Replaces the live watchlist with Claude's daily pick from the
  // liquidity/cap-feasible candidate pool - can take a while (bulk market
  // data fetch + AI call), give it a generous client-side timeout.
  refreshWatchlistAI: () => request<WatchlistAIResult>("/watchlist/ai-refresh", { method: "POST" }),
  getLatestWatchlistAI: () => request<WatchlistAIResult>("/watchlist/ai-picks/latest"),

  getBalance: () => request<AccountSummary>("/account/balance"),
  getPositions: () => request<Position[]>("/account/positions"),
  getOrders: (limit = 100) => request<Order[]>(`/account/orders?limit=${limit}`),

  placeOrder: (symbol: string, side: OrderSide) =>
    request<Order>("/orders", { method: "POST", body: JSON.stringify({ symbol, side }) }),
  flatten: (symbol: string) =>
    request<Order>("/orders/flatten", { method: "POST", body: JSON.stringify({ symbol }) }),

  generateSignal: (symbol: string) =>
    request<{ configured: boolean; signal?: AISignal; skipped_reason?: string }>("/ai/signal", {
      method: "POST",
      body: JSON.stringify({ symbol }),
    }),
  getSignals: (symbol: string, limit = 30) =>
    request<AISignal[]>(`/ai/signals?symbol=${encodeURIComponent(symbol)}&limit=${limit}`),

  getAutoTradeLog: (symbol?: string, limit = 100) =>
    request<AutoTradeLogEntry[]>(
      `/autotrade/log?limit=${limit}${symbol ? `&symbol=${encodeURIComponent(symbol)}` : ""}`,
    ),

  getSettings: () => request<Settings>("/settings"),
  updateSettings: (input: Partial<Pick<Settings, "autotrade_enabled" | "leverage" | "margin_usd" | "margin_type">>) =>
    request<Settings>("/settings", { method: "POST", body: JSON.stringify(input) }),

  getStrategyParams: () => request<StrategyParamsResponse>("/strategy/params"),
  // Runs a full backtest + grid-search optimization against live-pulled
  // history (180 days across a 20-symbol crypto-only liquidity pool) - can
  // take a while (deep history fetch + hundreds of backtest runs per symbol).
  optimizeStrategy: () => request<OptimizeReport>("/strategy/optimize", { method: "POST" }),
  // Uses the same symbol universe/end date as the latest BingX run, but a
  // full calendar year of public Binance USDT-perpetual proxy history. This
  // research-only route can never change active parameters.
  optimizeLongStrategy: () => request<OptimizeReport>("/strategy/optimize-long", { method: "POST" }),
};
