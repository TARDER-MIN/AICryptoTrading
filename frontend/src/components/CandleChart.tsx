import { useEffect, useRef } from "react";
import {
  ColorType,
  createChart,
  type IChartApi,
  type ISeriesApi,
  type SeriesMarker,
  type Time,
  type UTCTimestamp,
} from "lightweight-charts";
import type { AISignal, Candle } from "../types";
import { normalizeCandles } from "../utils/candles";

interface Props {
  candles: Candle[];
  signals?: AISignal[];
}

const toTime = (ts: string) => Math.floor(new Date(ts).getTime() / 1000) as UTCTimestamp;

// lightweight-charts defaults every series to 2-decimal price formatting.
// That's fine for BTCUSDT (~$79000) but flattens anything priced under ~$1
// (DOGEUSDT ~$0.09, ADAUSDT ~$0.22, 1000PEPEUSDT ~$0.0037) to visually
// identical candles, since price movement within a cent is invisible at 2
// decimals - looks like "the chart isn't moving" even when data is fine.
// Pick enough decimals to show real movement, scaled to the price magnitude.
function precisionForPrice(price: number): number {
  if (!Number.isFinite(price) || price <= 0) return 2;
  if (price >= 100) return 2;
  if (price >= 1) return 4;
  if (price >= 0.01) return 5;
  if (price >= 0.0001) return 6;
  return 8;
}

// Infers the candle interval (seconds) from the data itself, so the FVG
// zone overlay (see below) can extend a sensible number of bars forward
// regardless of the actual KLINE_INTERVAL the backend is configured for.
function inferIntervalSeconds(candles: Candle[]): number {
  if (candles.length < 2) return 5 * 60;
  const a = new Date(candles[candles.length - 2].ts).getTime();
  const b = new Date(candles[candles.length - 1].ts).getTime();
  const diff = Math.round((b - a) / 1000);
  return diff > 0 ? diff : 5 * 60;
}

// Standard crypto/US convention: green = up, red = down.
export function CandleChart({ candles, signals = [] }: Props) {
  const containerRef = useRef<HTMLDivElement>(null);
  const chartRef = useRef<IChartApi | null>(null);
  const seriesRef = useRef<ISeriesApi<"Candlestick"> | null>(null);
  const volumeRef = useRef<ISeriesApi<"Histogram"> | null>(null);
  // Current FVG/OB zones (plus legacy OTE data on old signals) are drawn as
  // pairs of short horizontal-ish
  // line segments (top/bottom of the zone) rather than a filled box -
  // lightweight-charts v4 (the version this project uses) has no native
  // rectangle/box primitive, and two bounding lines is a reasonable
  // low-risk approximation without a library version bump. Tracked here so
  // old ones get cleaned up (chart.removeSeries) whenever signals change,
  // instead of silently accumulating orphaned series.
  const zoneSeriesRef = useRef<ISeriesApi<"Line">[]>([]);

  useEffect(() => {
    if (!containerRef.current) return;
    const chart = createChart(containerRef.current, {
      layout: { background: { type: ColorType.Solid, color: "transparent" }, textColor: "#cbd5e1" },
      grid: { vertLines: { color: "#242832" }, horzLines: { color: "#242832" } },
      width: containerRef.current.clientWidth,
      height: 420,
      timeScale: { timeVisible: true, secondsVisible: false },
    });
    const series = chart.addCandlestickSeries({
      upColor: "#30a46c",
      downColor: "#e5484d",
      borderVisible: false,
      wickUpColor: "#30a46c",
      wickDownColor: "#e5484d",
    });
    const volume = chart.addHistogramSeries({
      priceFormat: { type: "volume" },
      priceScaleId: "",
    });
    volume.priceScale().applyOptions({ scaleMargins: { top: 0.85, bottom: 0 } });

    chartRef.current = chart;
    seriesRef.current = series;
    volumeRef.current = volume;

    const onResize = () => {
      if (containerRef.current) chart.applyOptions({ width: containerRef.current.clientWidth });
    };
    window.addEventListener("resize", onResize);
    return () => {
      window.removeEventListener("resize", onResize);
      chart.remove();
    };
  }, []);

  useEffect(() => {
    if (!seriesRef.current || !volumeRef.current) return;

    // setData requires strictly ascending, unique timestamps.
    const orderedCandles = normalizeCandles(candles);

    const lastClose = orderedCandles[orderedCandles.length - 1]?.close;
    if (lastClose != null) {
      const precision = precisionForPrice(lastClose);
      seriesRef.current.applyOptions({
        priceFormat: { type: "price", precision, minMove: Math.pow(10, -precision) },
      });
    }

    seriesRef.current.setData(
      orderedCandles.map((c) => ({ time: toTime(c.ts), open: c.open, high: c.high, low: c.low, close: c.close })),
    );
    volumeRef.current.setData(
      orderedCandles.map((c) => ({
        time: toTime(c.ts),
        value: c.volume,
        color: c.close >= c.open ? "rgba(48,164,108,0.5)" : "rgba(229,72,77,0.5)",
      })),
    );
  }, [candles]);

  // Markers: sweep points, FVG-confirmation points, and BUY/SELL AI
  // decisions (auto or manual) - HOLD is intentionally not marked, only
  // actionable/structural points clutter the chart usefully.
  useEffect(() => {
    if (!seriesRef.current) return;

    const markers: SeriesMarker<Time>[] = [];
    for (const s of signals) {
      if (s.sweep_ts) {
        markers.push({
          time: toTime(s.sweep_ts),
          position: "belowBar",
          shape: "circle",
          color: "#a855f7",
          id: `sweep-${s.id}`,
          text: "SWEEP",
        });
      }
      if (s.fvg_ts) {
        markers.push({
          time: toTime(s.fvg_ts),
          position: "aboveBar",
          shape: "square",
          color: "#eab308",
          id: `fvg-${s.id}`,
          text: "FVG",
        });
      }
      if (s.action !== "HOLD" && s.candle_ts) {
        const smtSuffix = s.smt_confirmed == null ? "" : s.smt_confirmed ? " SMT✓" : " SMT✗";
        markers.push({
          time: toTime(s.candle_ts),
          position: s.action === "BUY" ? "belowBar" : "aboveBar",
          shape: s.action === "BUY" ? "arrowUp" : "arrowDown",
          color: s.action === "BUY" ? "#3b82f6" : "#f59e0b",
          id: String(s.id),
          text: `${s.action} ${(s.confidence * 100).toFixed(0)}%${smtSuffix}`,
        });
      }
    }
    markers.sort((a, b) => (a.time as number) - (b.time as number));
    seriesRef.current.setMarkers(markers);
  }, [signals]);

  // Zone overlays: FVG (yellow), legacy OTE (blue), and the current M5 order
  // block stored under the backward-compatible breaker fields (purple).
  // segments bounding the zone, spanning forward from its anchor time by
  // ~15 bars (or until data ends).
  useEffect(() => {
    if (!chartRef.current) return;
    const chart = chartRef.current;

    for (const s of zoneSeriesRef.current) {
      chart.removeSeries(s);
    }
    zoneSeriesRef.current = [];

    const intervalSec = inferIntervalSeconds(candles);
    const spanSec = intervalSec * 15;

    const addZone = (startTime: UTCTimestamp, low: number, high: number, color: string) => {
      const endTime = (startTime + spanSec) as UTCTimestamp;
      const lineOpts = {
        color,
        lineWidth: 1 as const,
        lineStyle: 2 as const, // dashed
        priceLineVisible: false,
        lastValueVisible: false,
        crosshairMarkerVisible: false,
      };
      const top = chart.addLineSeries(lineOpts);
      top.setData([
        { time: startTime, value: high },
        { time: endTime, value: high },
      ]);
      const bottom = chart.addLineSeries(lineOpts);
      bottom.setData([
        { time: startTime, value: low },
        { time: endTime, value: low },
      ]);
      zoneSeriesRef.current.push(top, bottom);
    };

    for (const s of signals) {
      if (s.fvg_ts != null && s.fvg_low != null && s.fvg_high != null) {
        addZone(toTime(s.fvg_ts), s.fvg_low, s.fvg_high, "rgba(234,179,8,0.6)");
      }
      // Zones are anchored at the FVG time, when the setup first exists.
      if (s.fvg_ts != null && s.ote_low != null && s.ote_high != null) {
        addZone(toTime(s.fvg_ts), s.ote_low, s.ote_high, "rgba(59,130,246,0.6)");
      }
      if (s.fvg_ts != null && s.breaker_low != null && s.breaker_high != null) {
        addZone(toTime(s.fvg_ts), s.breaker_low, s.breaker_high, "rgba(168,85,247,0.6)");
      }
    }
  }, [signals, candles]);

  return <div ref={containerRef} style={{ width: "100%" }} />;
}
