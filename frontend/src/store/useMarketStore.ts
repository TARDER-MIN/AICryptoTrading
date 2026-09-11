import { create } from "zustand";
import type { AISignal, Candle } from "../types";
import { normalizeCandles } from "../utils/candles";

interface MarketState {
  selectedSymbol: string;
  setSelectedSymbol: (symbol: string) => void;

  candles: Record<string, Candle[]>;
  setCandles: (symbol: string, candles: Candle[]) => void;
  upsertCandle: (candle: Candle) => void;

  latestSignal: Record<string, AISignal>;

  // Full per-symbol signal history (newest first), used to draw buy/sell
  // markers on the chart - both manually-requested and auto-triggered
  // (internal/autotrader.Trader.OnCandleClose) signals land here the same way.
  signals: Record<string, AISignal[]>;
  setSignals: (symbol: string, signals: AISignal[]) => void;
  upsertSignal: (signal: AISignal) => void;
}

export const useMarketStore = create<MarketState>((set) => ({
  selectedSymbol: "BTC-USDT",
  setSelectedSymbol: (symbol) => set({ selectedSymbol: symbol }),

  candles: {},
  setCandles: (symbol, candles) =>
    set((state) => ({ candles: { ...state.candles, [symbol]: normalizeCandles(candles) } })),
  upsertCandle: (candle) =>
    set((state) => {
      const list = state.candles[candle.symbol] ?? [];
      const next = normalizeCandles([...list, candle]);
      return { candles: { ...state.candles, [candle.symbol]: next } };
    }),

  latestSignal: {},

  signals: {},
  setSignals: (symbol, signals) =>
    set((state) => ({
      signals: { ...state.signals, [symbol]: signals },
      latestSignal: signals[0] ? { ...state.latestSignal, [symbol]: signals[0] } : state.latestSignal,
    })),
  upsertSignal: (signal) =>
    set((state) => {
      const list = state.signals[signal.symbol] ?? [];
      const next = list.some((s) => s.id === signal.id) ? list : [signal, ...list];
      return {
        signals: { ...state.signals, [signal.symbol]: next },
        latestSignal: { ...state.latestSignal, [signal.symbol]: signal },
      };
    }),
}));
