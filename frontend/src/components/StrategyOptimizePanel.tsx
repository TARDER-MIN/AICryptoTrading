import { useEffect, useState } from "react";
import { api } from "../api/client";
import { useI18n } from "../i18n/I18nContext";
import type { BacktestBreakdown, BacktestMetrics, OptimizeReport, SBParams } from "../types";

const CURRENT_STRATEGY_VERSION = "htf_external_liquidity_v2";

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

function BreakdownTable({ rows, labelHeader }: { rows: BacktestBreakdown[]; labelHeader: string }) {
  const { t } = useI18n();
  if (!rows.length) return <p className="muted small">{t("strategyOptimize.noDiagnosticTrades")}</p>;
  const displayLabel = (label: string) => {
    if (label === "BUY") return t("strategyOptimize.sideBuy");
    if (label === "SELL") return t("strategyOptimize.sideSell");
    return label;
  };
  return (
    <div className="table-scroll diagnostic-table">
      <table>
        <thead>
          <tr>
            <th>{labelHeader}</th>
            <th>{t("strategyOptimize.colTrades")}</th>
            <th>{t("strategyOptimize.colExitMix")}</th>
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
          {rows.map(({ label, metrics: m }) => (
            <tr key={label}>
              <td>{displayLabel(label)}</td>
              <td>{m.total_trades}</td>
              <td>
                {t("strategyOptimize.exitMix", {
                  target: m.target_exits ?? 0,
                  stop: m.stop_exits ?? 0,
                  end: m.end_of_data_exits ?? 0,
                })}
              </td>
              <td>{m.win_rate.toFixed(1)}%</td>
              <td className={m.gross_return_pct >= 0 ? "buy" : "sell"}>{m.gross_return_pct.toFixed(2)}%</td>
              <td>{m.total_trading_cost_pct.toFixed(2)}%</td>
              <td className={m.total_return_pct >= 0 ? "buy" : "sell"}>{m.total_return_pct.toFixed(2)}%</td>
              <td>{m.profit_factor.toFixed(2)}</td>
              <td>{m.sharpe.toFixed(2)}</td>
              <td>{m.max_drawdown_pct.toFixed(2)}%</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}

// Manually-triggered backtest + grid-search optimization (internal/backtest.Optimize)
// for the HTF 3+1 strategy's parameters - see internal/autotrader for
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
  const [longReport, setLongReport] = useState<OptimizeReport | null>(null);
  const [loading, setLoading] = useState(false);
  const [longLoading, setLongLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [longError, setLongError] = useState<string | null>(null);

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
      htfSweepAtr: p.min_htf_sweep_atr.toFixed(2),
      htfReclaimAtr: p.min_htf_reclaim_atr.toFixed(2),
      dispPct: (p.min_displacement_body_pct * 100).toFixed(0),
      dispAtr: p.min_displacement_atr.toFixed(1),
      dispVolume: p.min_displacement_volume.toFixed(1),
      choch: p.choch_lookback,
      retest: p.max_bars_for_retest,
      ob: p.order_block_lookback,
      costMultiple: p.min_target_cost_multiple.toFixed(0),
      rejection: p.require_rejection ? t("strategyOptimize.required") : t("strategyOptimize.notRequired"),
    });

  const reload = () => {
    api
      .getStrategyParams()
      .then((res) => {
        setCurrent({
          params: res.params,
          train: res.train_metrics,
          validation: res.validation_metrics,
          test: res.test_metrics,
          optimizedAt: res.optimized_at,
        });
        if (res.last_report) setReport(res.last_report);
        if (res.last_long_report) setLongReport(res.last_long_report);
      })
      .catch(() => undefined);
  };

  const runLongOptimize = async () => {
    setLongLoading(true);
    setLongError(null);
    try {
      const res = await api.optimizeLongStrategy();
      setLongReport(res);
      reload();
    } catch (e) {
      setLongError(String(e));
    } finally {
      setLongLoading(false);
    }
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

  const dateTime = (value: string) => new Date(value).toLocaleString();
  const shortParams = (p: SBParams) =>
    t("strategyOptimize.walkForwardParams", {
      swing: p.swing_lookback,
      fvgPct: p.min_fvg_size_pct,
      sweepBars: p.max_bars_for_sweep,
      stopBuf: p.stop_buffer_pct,
    });

  const blockerText = (code: string) => {
    switch (code) {
      case "insufficient_history": return t("strategyOptimize.blockerInsufficientHistory");
      case "no_robust_candidate": return t("strategyOptimize.blockerNoRobustCandidate");
      case "walk_forward_pass_rate": return t("strategyOptimize.blockerWalkForwardPassRate");
      case "walk_forward_trades": return t("strategyOptimize.blockerWalkForwardTrades");
      case "walk_forward_net": return t("strategyOptimize.blockerWalkForwardNet");
      case "walk_forward_profit_factor": return t("strategyOptimize.blockerWalkForwardProfitFactor");
      case "walk_forward_sharpe": return t("strategyOptimize.blockerWalkForwardSharpe");
      case "walk_forward_drawdown": return t("strategyOptimize.blockerWalkForwardDrawdown");
      case "final_trades": return t("strategyOptimize.blockerFinalTrades");
      case "final_net": return t("strategyOptimize.blockerFinalNet");
      case "final_profit_factor": return t("strategyOptimize.blockerFinalProfitFactor");
      case "final_sharpe": return t("strategyOptimize.blockerFinalSharpe");
      case "final_drawdown": return t("strategyOptimize.blockerFinalDrawdown");
      case "final_buy_insufficient": return t("strategyOptimize.blockerFinalBuyInsufficient");
      case "final_buy_unprofitable": return t("strategyOptimize.blockerFinalBuyUnprofitable");
      case "final_sell_insufficient": return t("strategyOptimize.blockerFinalSellInsufficient");
      case "final_sell_unprofitable": return t("strategyOptimize.blockerFinalSellUnprofitable");
      default: return code;
    }
  };
  const robustSelection = report?.robust_selection;
  const applyBlockers = report?.apply_blockers ?? [];
  const reportIsCurrent = report?.strategy_version === CURRENT_STRATEGY_VERSION;
  const longReportIsCurrent = longReport?.strategy_version === CURRENT_STRATEGY_VERSION;

  return (
    <div className="panel">
      <h3>{t("strategyOptimize.title")}</h3>
      <p className="muted small">{t("strategyOptimize.description")}</p>
      <div className="button-row">
        <button disabled={loading || longLoading} onClick={runOptimize}>
          {loading ? t("strategyOptimize.running") : t("strategyOptimize.run")}
        </button>
        <button disabled={loading || longLoading} onClick={runLongOptimize}>
          {longLoading ? t("strategyOptimize.longRunning") : t("strategyOptimize.longRun")}
        </button>
      </div>
      {error && <p className="error">{error}</p>}
      {longError && <p className="error">{longError}</p>}

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
          {!reportIsCurrent && (
            <div className="optimization-decision blocked">
              <h4>{t("strategyOptimize.outdatedReportTitle")}</h4>
              <p className="small">{t("strategyOptimize.outdatedReportHelp")}</p>
            </div>
          )}
          {reportIsCurrent && typeof report.params_applied === "boolean" && (
          <div className={`optimization-decision ${report.params_applied ? "passed" : "blocked"}`}>
            <h4>
              {report.params_applied
                ? t("strategyOptimize.appliedTitle")
                : t("strategyOptimize.blockedTitle")}
            </h4>
            <p className="small">
              {report.params_applied
                ? t("strategyOptimize.appliedHelp")
                : t("strategyOptimize.blockedHelp")}
            </p>
            <p className="muted small">
              {t("strategyOptimize.proposedParams", { params: tunedParamsSummary(report.best_params) })}
            </p>
            {!report.params_applied && applyBlockers.length > 0 && (
              <ul className="small">
                {applyBlockers.map((code) => <li key={code}>{blockerText(code)}</li>)}
              </ul>
            )}
          </div>
          )}

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
            {report.reused_previous_sample
              ? t("strategyOptimize.sameSampleReused")
              : t("strategyOptimize.freshSampleSelected")}
            {(report.symbols_unavailable?.length ?? 0) > 0 && (
              <><br />{t("strategyOptimize.recentUnavailable", { symbols: (report.symbols_unavailable ?? []).join(", ") })}</>
            )}
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

          {robustSelection && robustSelection.total_folds > 0 && (
            <div className="backtest-diagnostics">
              <h4>{t("strategyOptimize.robustSelectionTitle")}</h4>
              <p className="muted small">{t("strategyOptimize.robustSelectionHelp")}</p>
              <p className="small">
                {t("strategyOptimize.robustSelectionSummary", {
                  candidates: robustSelection.candidates_passed,
                  passed: robustSelection.passed_folds,
                  total: robustSelection.total_folds,
                  trades: robustSelection.aggregate_metrics.total_trades,
                  net: robustSelection.aggregate_metrics.total_return_pct.toFixed(2),
                  pf: robustSelection.aggregate_metrics.profit_factor.toFixed(2),
                  sharpe: robustSelection.aggregate_metrics.sharpe.toFixed(2),
                  dd: robustSelection.aggregate_metrics.max_drawdown_pct.toFixed(2),
                })}
              </p>
              {robustSelection.used_fallback && (
                <p className="error small">{t("strategyOptimize.robustFallbackWarning")}</p>
              )}
              <div className="table-scroll diagnostic-table">
                <table>
                  <thead>
                    <tr>
                      <th>{t("strategyOptimize.colFold")}</th>
                      <th>{t("strategyOptimize.colTestRange")}</th>
                      <th>{t("strategyOptimize.colTrades")}</th>
                      <th>{t("strategyOptimize.colNetReturn")}</th>
                      <th>{t("strategyOptimize.colProfitFactor")}</th>
                      <th>{t("strategyOptimize.colSharpe")}</th>
                      <th>{t("strategyOptimize.colDrawdown")}</th>
                      <th>{t("strategyOptimize.colVerdict")}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {robustSelection.folds.map((fold) => (
                      <tr key={fold.index}>
                        <td>{fold.index}</td>
                        <td>{dateTime(fold.test_start)} – {dateTime(fold.test_end)}</td>
                        <td>{fold.test_metrics.total_trades}</td>
                        <td className={fold.test_metrics.total_return_pct >= 0 ? "buy" : "sell"}>
                          {fold.test_metrics.total_return_pct.toFixed(2)}%
                        </td>
                        <td>{fold.test_metrics.profit_factor.toFixed(2)}</td>
                        <td>{fold.test_metrics.sharpe.toFixed(2)}</td>
                        <td>{fold.test_metrics.max_drawdown_pct.toFixed(2)}%</td>
                        <td className={fold.passed ? "buy" : "sell"}>
                          {fold.passed ? t("strategyOptimize.foldPass") : t("strategyOptimize.foldFail")}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
              <details open>
                <summary>{t("strategyOptimize.robustBySideTitle")}</summary>
                <BreakdownTable
                  rows={robustSelection.by_side}
                  labelHeader={t("strategyOptimize.colSide")}
                />
              </details>
              <p className="muted small">{t("strategyOptimize.robustSelectionThreshold")}</p>
            </div>
          )}

          {report.final_test_diagnostics && (
            <div className="backtest-diagnostics">
              <h4>{t("strategyOptimize.finalDiagnosticsTitle")}</h4>
              <p className="muted small">
                {t("strategyOptimize.finalDiagnosticsSummary", {
                  total: report.test_metrics.total_trades,
                  completed:
                    report.test_metrics.total_trades - (report.test_metrics.end_of_data_exits ?? 0),
                  end: report.test_metrics.end_of_data_exits ?? 0,
                  net: report.final_test_diagnostics.completed_only_metrics.total_return_pct.toFixed(2),
                  pf: report.final_test_diagnostics.completed_only_metrics.profit_factor.toFixed(2),
                })}
              </p>

              <details open>
                <summary>{t("strategyOptimize.bySideTitle")}</summary>
                <BreakdownTable
                  rows={report.final_test_diagnostics.by_side}
                  labelHeader={t("strategyOptimize.colSide")}
                />
              </details>
              <details>
                <summary>{t("strategyOptimize.bySymbolTitle")}</summary>
                <BreakdownTable
                  rows={report.final_test_diagnostics.by_symbol}
                  labelHeader={t("strategyOptimize.colSymbol")}
                />
              </details>
              <details>
                <summary>{t("strategyOptimize.byDayTitle")}</summary>
                <BreakdownTable
                  rows={report.final_test_diagnostics.by_day}
                  labelHeader={t("strategyOptimize.colDateUTC")}
                />
              </details>
            </div>
          )}

          {report.walk_forward?.total_folds > 0 && (
            <div className="backtest-diagnostics">
              <h4>{t("strategyOptimize.walkForwardTitle")}</h4>
              <p className="muted small">{t("strategyOptimize.walkForwardHelp")}</p>
              <p className="small">
                {t("strategyOptimize.walkForwardSummary", {
                  passed: report.walk_forward.passed_folds,
                  total: report.walk_forward.total_folds,
                  trades: report.walk_forward.aggregate_metrics.total_trades,
                  net: report.walk_forward.aggregate_metrics.total_return_pct.toFixed(2),
                  pf: report.walk_forward.aggregate_metrics.profit_factor.toFixed(2),
                  sharpe: report.walk_forward.aggregate_metrics.sharpe.toFixed(2),
                  dd: report.walk_forward.aggregate_metrics.max_drawdown_pct.toFixed(2),
                })}
              </p>
              <div className="table-scroll diagnostic-table">
                <table>
                  <thead>
                    <tr>
                      <th>{t("strategyOptimize.colFold")}</th>
                      <th>{t("strategyOptimize.colSelectionRange")}</th>
                      <th>{t("strategyOptimize.colTestRange")}</th>
                      <th>{t("strategyOptimize.colSelectedParams")}</th>
                      <th>{t("strategyOptimize.colTrades")}</th>
                      <th>{t("strategyOptimize.colNetReturn")}</th>
                      <th>{t("strategyOptimize.colProfitFactor")}</th>
                      <th>{t("strategyOptimize.colSharpe")}</th>
                      <th>{t("strategyOptimize.colDrawdown")}</th>
                      <th>{t("strategyOptimize.colVerdict")}</th>
                    </tr>
                  </thead>
                  <tbody>
                    {report.walk_forward.folds.map((fold) => (
                      <tr key={fold.index}>
                        <td>{fold.index}</td>
                        <td>{dateTime(fold.selection_start)} – {dateTime(fold.selection_end)}</td>
                        <td>{dateTime(fold.test_start)} – {dateTime(fold.test_end)}</td>
                        <td>
                          {shortParams(fold.selected_params)}
                          <span className="muted small">
                            {t("strategyOptimize.candidatesPassedShort", { count: fold.candidates_passed })}
                          </span>
                        </td>
                        <td>{fold.test_metrics.total_trades}</td>
                        <td className={fold.test_metrics.total_return_pct >= 0 ? "buy" : "sell"}>
                          {fold.test_metrics.total_return_pct.toFixed(2)}%
                        </td>
                        <td>{fold.test_metrics.profit_factor.toFixed(2)}</td>
                        <td>{fold.test_metrics.sharpe.toFixed(2)}</td>
                        <td>{fold.test_metrics.max_drawdown_pct.toFixed(2)}%</td>
                        <td className={fold.passed ? "buy" : "sell"}>
                          {fold.used_fallback
                            ? t("strategyOptimize.foldFallback")
                            : fold.passed
                              ? t("strategyOptimize.foldPass")
                              : t("strategyOptimize.foldFail")}
                        </td>
                      </tr>
                    ))}
                  </tbody>
                </table>
              </div>
              <p className="muted small">{t("strategyOptimize.walkForwardThreshold")}</p>
            </div>
          )}
        </div>
      )}

      {longReport && (
        <div className="backtest-diagnostics long-range-report" style={{ marginTop: 16 }}>
          <h4>{t("strategyOptimize.longTitle")}</h4>
          {!longReportIsCurrent && (
            <div className="optimization-decision blocked">
              <h4>{t("strategyOptimize.outdatedLongReportTitle")}</h4>
              <p className="small">{t("strategyOptimize.outdatedLongReportHelp")}</p>
            </div>
          )}
          {longReportIsCurrent && <div className={`optimization-decision ${longReport.robustness_passed ? "passed" : "blocked"}`}>
            <h4>
              {longReport.robustness_passed
                ? t("strategyOptimize.longPassedTitle")
                : t("strategyOptimize.longFailedTitle")}
            </h4>
            <p className="small">
              {longReport.robustness_passed
                ? t("strategyOptimize.longPassedHelp")
                : t("strategyOptimize.longFailedHelp")}
            </p>
            {!longReport.robustness_passed && (longReport.apply_blockers?.length ?? 0) > 0 && (
              <ul className="small">
                {longReport.apply_blockers?.map((code) => <li key={code}>{blockerText(code)}</li>)}
              </ul>
            )}
          </div>}

          <p className="muted small">
            {t("strategyOptimize.longSampleSummary", {
              days: longReport.history_days,
              actualDays: longReport.history_available_days.toFixed(1),
              count: longReport.symbols_tested.length,
              candles: longReport.candles_tested.toLocaleString(),
              start: dateTime(longReport.history_start),
              end: dateTime(longReport.history_end),
            })}
            <br />
            {t("strategyOptimize.longProxyWarning")}
            <br />
            {t("strategyOptimize.longCostSummary", {
              fee: longReport.cost_model.taker_fee_pct_per_side,
              slippage: longReport.cost_model.estimated_slippage_pct_per_side,
              roundTrip: ((longReport.cost_model.taker_fee_pct_per_side + longReport.cost_model.estimated_slippage_pct_per_side) * 2).toFixed(2),
              funding: longReport.cost_model.funding_included
                ? t("strategyOptimize.included")
                : t("strategyOptimize.notIncluded"),
            })}
            {(longReport.symbols_unavailable?.length ?? 0) > 0 && (
              <><br />{t("strategyOptimize.longUnavailable", { symbols: (longReport.symbols_unavailable ?? []).join(", ") })}</>
            )}
            {(longReport.symbols_partial?.length ?? 0) > 0 && (
              <><br />{t("strategyOptimize.longPartial", { symbols: (longReport.symbols_partial ?? []).join(", ") })}</>
            )}
          </p>
          <p className="muted small">
            {t("strategyOptimize.proposedParams", { params: tunedParamsSummary(longReport.best_params) })}
          </p>

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
                <MetricsRow label={t("strategyOptimize.trainPeriod")} m={longReport.train_metrics} />
                <MetricsRow label={t("strategyOptimize.validationPeriod")} m={longReport.validation_metrics} />
                <MetricsRow label={t("strategyOptimize.testPeriod")} m={longReport.test_metrics} />
              </tbody>
            </table>
          </div>

          {longReport.walk_forward?.total_folds > 0 && (
            <p className="small">
              {t("strategyOptimize.walkForwardSummary", {
                passed: longReport.walk_forward.passed_folds,
                total: longReport.walk_forward.total_folds,
                trades: longReport.walk_forward.aggregate_metrics.total_trades,
                net: longReport.walk_forward.aggregate_metrics.total_return_pct.toFixed(2),
                pf: longReport.walk_forward.aggregate_metrics.profit_factor.toFixed(2),
                sharpe: longReport.walk_forward.aggregate_metrics.sharpe.toFixed(2),
                dd: longReport.walk_forward.aggregate_metrics.max_drawdown_pct.toFixed(2),
              })}
            </p>
          )}

          {longReport.final_test_diagnostics && (
            <>
              <details open>
                <summary>{t("strategyOptimize.bySideTitle")}</summary>
                <BreakdownTable
                  rows={longReport.final_test_diagnostics.by_side}
                  labelHeader={t("strategyOptimize.colSide")}
                />
              </details>
              <details>
                <summary>{t("strategyOptimize.bySymbolTitle")}</summary>
                <BreakdownTable
                  rows={longReport.final_test_diagnostics.by_symbol}
                  labelHeader={t("strategyOptimize.colSymbol")}
                />
              </details>
            </>
          )}
        </div>
      )}
    </div>
  );
}
