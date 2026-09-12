import { useEffect, useState } from "react";
import { api } from "../api/client";
import { useI18n } from "../i18n/I18nContext";
import type { BacktestMetrics, OptimizeReport, SBParams } from "../types";

function MetricsRow({ label, m }: { label: string; m: BacktestMetrics | null | undefined }) {
  const { t } = useI18n();
  if (!m) return null;
  const hasCostBreakdown = Number.isFinite(m.gross_return_pct) && Number.isFinite(m.total_trading_cost_pct);
  const gross = hasCostBreakdown ? m.gross_return_pct : m.total_return_pct;
  const costs = hasCostBreakdown ? m.total_trading_cost_pct : null;
  return (
    <tr>
      <td>{label}</td>
      <td>{m.total_trades}</td>
      <td>{m.win_rate.toFixed(1)}%</td>
      <td className={gross >= 0 ? "buy" : "sell"}>{gross.toFixed(2)}%</td>
      <td>
        {costs === null
          ? "—"
          : t("strategyOptimize.costBreakdown", {
              total: costs.toFixed(2),
              fee: m.fee_cost_pct.toFixed(2),
              slippage: m.slippage_cost_pct.toFixed(2),
              funding: m.funding_cost_pct.toFixed(2),
            })}
      </td>
      <td className={m.total_return_pct >= 0 ? "buy" : "sell"}>{m.total_return_pct.toFixed(2)}%</td>
      <td>{m.profit_factor.toFixed(2)}</td>
      <td>{m.sharpe.toFixed(2)}</td>
      <td>{m.max_drawdown_pct.toFixed(2)}%</td>
    </tr>
  );
}

// Manually-triggered backtest + grid-search optimization (internal/backtest.Optimize)
// for the Silver Bullet strategy's parameters - see internal/autotrader for
// how the live pipeline picks up whatever this saves. One-time tuning per
// the user's explicit choice, not a recurring scheduled job.
export function StrategyOptimizePanel() {
  const { t } = useI18n();
  const [current, setCurrent] = useState<{
    params: SBParams;
    train: BacktestMetrics | null;
    validation: BacktestMetrics | null;
    test: BacktestMetrics | null;
    optimizedAt: string | null;
  } | null>(null);
  const [report, setReport] = useState<OptimizeReport | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const tunedParamsSummary = (p: SBParams) =>
    t("strategyOptimize.tunedParamsSummary", {
      swing: p.swing_lookback,
      fvgPct: p.min_fvg_size_pct,
      sweepBars: p.max_bars_for_sweep,
      stopBuf: p.stop_buffer_pct,
      rr: p.risk_reward_ratio,
    });

  const fixedRulesSummary = (p: SBParams) =>
    t("strategyOptimize.fixedRulesSummary", {
      dispPct: (p.min_displacement_body_pct * 100).toFixed(0),
      oteMin: (p.ote_min_retrace * 100).toFixed(0),
      oteMax: (p.ote_max_retrace * 100).toFixed(0),
      breakerReq: p.require_breaker_confluence ? t("strategyOptimize.required") : t("strategyOptimize.notRequired"),
      smtReq: p.require_smt_divergence ? t("strategyOptimize.required") : t("strategyOptimize.notRequired"),
    });

  const reload = () => {
    api
      .getStrategyParams()
      .then((res) =>
        setCurrent({
          params: res.params,
          train: res.train_metrics,
          validation: res.validation_metrics,
          test: res.test_metrics,
          optimizedAt: res.optimized_at,
        }),
      )
      .catch(() => undefined);
  };
  useEffect(reload, []);

  const runOptimize = async () => {
    setLoading(true);
    setError(null);
    try {
      const res = await api.optimizeStrategy();
      setReport(res);
      reload();
    } catch (e) {
      setError(String(e));
    } finally {
      setLoading(false);
    }
  };

  return (
    <div className="panel">
      <h3>{t("strategyOptimize.title")}</h3>
      <p className="muted small">{t("strategyOptimize.description")}</p>
      <button disabled={loading} onClick={runOptimize}>
        {loading ? t("strategyOptimize.running") : t("strategyOptimize.run")}
      </button>
      {error && <p className="error">{error}</p>}

      {current && (
        <div style={{ marginTop: 12 }}>
          <p className="muted small">
            {t("strategyOptimize.currentParams", {
              optimizedAt: current.optimizedAt
                ? t("strategyOptimize.optimizedAtSuffix", { date: new Date(current.optimizedAt).toLocaleString() })
                : t("strategyOptimize.notOptimizedYet"),
            })}
            <br />
            {tunedParamsSummary(current.params)}
            <br />
            {t("strategyOptimize.fixedRulesLabel", { rules: fixedRulesSummary(current.params) })}
          </p>
          {(current.train || current.validation || current.test) && (
            <div className="table-scroll">
              <table>
                <thead>
                  <tr>
                    <th>{t("strategyOptimize.colPeriod")}</th>
                    <th>{t("strategyOptimize.colTrades")}</th>
                    <th>{t("strategyOptimize.colWinRate")}</th>
                    <th>{t("strategyOptimize.colGrossReturn")}</th>
                    <th>{t("strategyOptimize.colCosts")}</th>
                    <th>{t("strategyOptimize.colNetReturn")}</th>
                    <th>{t("strategyOptimize.colProfitFactor")}</th>
                    <th>{t("strategyOptimize.colSharpe")}</th>
                    <th>{t("strategyOptimize.colDrawdown")}</th>
                  </tr>
                </thead>
                <tbody>
                  <MetricsRow label={t("strategyOptimize.trainPeriod")} m={current.train} />
                  <MetricsRow label={t("strategyOptimize.validationPeriod")} m={current.validation} />
                  <MetricsRow label={t("strategyOptimize.testPeriod")} m={current.test} />
                </tbody>
              </table>
              {!current.test && <p className="muted small">{t("strategyOptimize.legacyMetricsWarning")}</p>}
              <p className="muted small">{t("strategyOptimize.metricsFootnote")}</p>
            </div>
          )}
        </div>
      )}

      {report && (
        <div style={{ marginTop: 12 }}>
          <p className="muted small">
            {t("strategyOptimize.sampleSummary", {
              days: report.history_days,
              actualDays: report.history_available_days.toFixed(1),
              count: report.symbols_tested.length,
              candles: report.candles_tested.toLocaleString(),
              start: new Date(report.history_start).toLocaleString(),
              end: new Date(report.history_end).toLocaleString(),
            })}
            <br />
            {t("strategyOptimize.costSummary", {
              fee: report.cost_model.taker_fee_pct_per_side,
              slippage: report.cost_model.estimated_slippage_pct_per_side,
              roundTrip: ((report.cost_model.taker_fee_pct_per_side + report.cost_model.estimated_slippage_pct_per_side) * 2).toFixed(2),
              feeSource:
                report.cost_model.fee_source === "bingx_account"
                  ? t("strategyOptimize.feeSourceAccount")
                  : t("strategyOptimize.feeSourceFallback"),
              funding:
                report.cost_model.funding_included && report.cost_model.funding_source === "bingx_history"
                  ? t("strategyOptimize.fundingHistoryIncluded")
                  : t("strategyOptimize.notIncluded"),
            })}
            <br />
            {t("strategyOptimize.evaluatedSummary", {
              evaluated: report.candidates_evaluated,
              passed: report.candidates_passed,
              trainEnd: new Date(report.train_end).toLocaleString(),
              testStart: new Date(report.test_start).toLocaleString(),
            })}
          </p>
        </div>
      )}
    </div>
  );
}
