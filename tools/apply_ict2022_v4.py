from pathlib import Path

p = Path("backend/internal/strategy/silverbullet.go")
s = p.read_text(encoding="utf-8")

if 'StrategyVersion = "ict2022_sweep_mss_fvg_v4"' in s:
    print("ICT2022 V4 already applied")
    raise SystemExit(0)


def rep(old: str, new: str, label: str) -> None:
    global s
    if old not in s:
        raise SystemExit(f"missing expected block: {label}")
    s = s.replace(old, new, 1)


rep(
    'StrategyVersion = "htf_location_3plus1_v3"',
    'StrategyVersion = "ict2022_sweep_mss_fvg_v4"',
    "strategy version",
)

rep(
    '''\tHTFStructureLookback      int     `json:"htf_structure_lookback"`       // completed H4 bars used for range/structure context
\tHTFPivotStrength          int     `json:"htf_pivot_strength"`           // completed H4 bars required on each side of a pivot
\tHTFZoneATRMultiple        float64 `json:"htf_zone_atr_multiple"`        // maximum distance from H4 SNR edge, in H4 ATR
\tCHOCHPivotStrength        int     `json:"choch_pivot_strength"`         // completed M5 bars required on each side of a pivot
\tTargetBarrierBufferATR    float64 `json:"target_barrier_buffer_atr"`    // clearance kept before opposing H4/H1 structure
''',
    '''\tHTFStructureLookback      int     `json:"htf_structure_lookback"`       // completed H4 bars used for range/structure context
\tHTFPivotStrength          int     `json:"htf_pivot_strength"`           // completed H4 bars required on each side of a pivot
\tHTFZoneATRMultiple        float64 `json:"htf_zone_atr_multiple"`        // maximum distance from H4 SNR edge, in H4 ATR
\tCHOCHPivotStrength        int     `json:"choch_pivot_strength"`         // completed M5 bars required on each side of a pivot
\tTargetBarrierBufferATR    float64 `json:"target_barrier_buffer_atr"`    // clearance kept before opposing H4/H1 structure
\tRelaxHTFContext           bool    `json:"relax_htf_context"`            // V4 research: H4 trend/location is context, not an entry veto
''',
    "RelaxHTFContext field",
)

rep(
    '''\t\tSwingLookback:   6,
\t\tMinFVGSizePct:   0.1,
\t\tMaxBarsForSweep: 4,
''',
    '''\t\tSwingLookback:   6,
\t\tMinFVGSizePct:   0.05,
\t\tMaxBarsForSweep: 6,
''',
    "default sweep/fvg",
)

rep(
    '''\t\tHTFStructureLookback:   12,
\t\tHTFPivotStrength:       2,
\t\tHTFZoneATRMultiple:     0.75,
\t\tCHOCHPivotStrength:     2,
\t\tTargetBarrierBufferATR: 0.10,
''',
    '''\t\tHTFStructureLookback:   12,
\t\tHTFPivotStrength:       1,
\t\tHTFZoneATRMultiple:     1.25,
\t\tCHOCHPivotStrength:     1,
\t\tTargetBarrierBufferATR: 0.05,
\t\tRelaxHTFContext:        true,
''',
    "default htf context",
)

rep(
    '''\t\tMinHTFSweepATR:            0.05,
\t\tMinHTFReclaimATR:          0.10,
\t\tMinDisplacementBodyPct:    0.6,
\t\tMaxOpposingWickPct:        0.25,
\t\tMinDisplacementATR:        0.8,
\t\tMinDisplacementVolume:     1.2,
\t\tDisplacementATRLookback:   14,
\t\tDisplacementVolLookback:   20,
\t\tCHOCHLookback:             12,
\t\tMaxBarsForRetest:          6,
''',
    '''\t\tMinHTFSweepATR:            0.03,
\t\tMinHTFReclaimATR:          0.03,
\t\tMinDisplacementBodyPct:    0.50,
\t\tMaxOpposingWickPct:        0.35,
\t\tMinDisplacementATR:        0.50,
\t\tMinDisplacementVolume:     1.00,
\t\tDisplacementATRLookback:   14,
\t\tDisplacementVolLookback:   20,
\t\tCHOCHLookback:             16,
\t\tMaxBarsForRetest:          12,
''',
    "default displacement/retest",
)
rep(
    "\t\tMinTargetCostMultiple:     5,",
    "\t\tMinTargetCostMultiple:     3,",
    "default cost multiple",
)

old_norm = '''func NormalizeParams(params SBParams) SBParams {
\tbackfillHTF3Plus1Defaults(&params)
\td := DefaultSBParams()
\t// These are fixed quality floors, not optimizer knobs. Persisted legacy
\t// JSON may only make them stricter, never weaken the current strategy.
\tparams.HTFStructureLookback = max(params.HTFStructureLookback, 4)
\tparams.HTFPivotStrength = max(params.HTFPivotStrength, d.HTFPivotStrength)
\tparams.HTFZoneATRMultiple = math.Min(params.HTFZoneATRMultiple, d.HTFZoneATRMultiple)
\tparams.CHOCHPivotStrength = max(params.CHOCHPivotStrength, d.CHOCHPivotStrength)
\tparams.CHOCHLookback = max(params.CHOCHLookback, params.CHOCHPivotStrength*2+1)
\tparams.TargetBarrierBufferATR = math.Max(params.TargetBarrierBufferATR, d.TargetBarrierBufferATR)
\tparams.MinHTFSweepATR = math.Max(params.MinHTFSweepATR, d.MinHTFSweepATR)
\tparams.MinHTFReclaimATR = math.Max(params.MinHTFReclaimATR, d.MinHTFReclaimATR)
\tparams.MinDisplacementBodyPct = math.Max(params.MinDisplacementBodyPct, d.MinDisplacementBodyPct)
\tparams.MaxOpposingWickPct = math.Min(params.MaxOpposingWickPct, d.MaxOpposingWickPct)
\tparams.MinDisplacementATR = math.Max(params.MinDisplacementATR, d.MinDisplacementATR)
\tparams.MinDisplacementVolume = math.Max(params.MinDisplacementVolume, d.MinDisplacementVolume)
\tparams.DisplacementATRLookback = max(params.DisplacementATRLookback, d.DisplacementATRLookback)
\tparams.DisplacementVolLookback = max(params.DisplacementVolLookback, d.DisplacementVolLookback)
\tparams.RequireRejection = true
\tparams.MinTargetCostMultiple = math.Max(params.MinTargetCostMultiple, d.MinTargetCostMultiple)
\tparams.RiskRewardRatio = fixedRiskRewardRatio
\treturn params
}
'''
new_norm = '''func NormalizeParams(params SBParams) SBParams {
\tbackfillHTF3Plus1Defaults(&params)
\t// V4 keeps the core ICT sequence mandatory while allowing research
\t// thresholds to be loosened without restoring the old restrictive defaults.
\tparams.HTFStructureLookback = max(params.HTFStructureLookback, 4)
\tparams.HTFPivotStrength = max(params.HTFPivotStrength, 1)
\tparams.HTFZoneATRMultiple = math.Max(params.HTFZoneATRMultiple, 0.25)
\tparams.CHOCHPivotStrength = max(params.CHOCHPivotStrength, 1)
\tparams.CHOCHLookback = max(params.CHOCHLookback, params.CHOCHPivotStrength*2+1)
\tparams.TargetBarrierBufferATR = math.Max(params.TargetBarrierBufferATR, 0)
\tparams.MinHTFSweepATR = math.Max(params.MinHTFSweepATR, 0.01)
\tparams.MinHTFReclaimATR = math.Max(params.MinHTFReclaimATR, 0.01)
\tparams.MinDisplacementBodyPct = math.Max(params.MinDisplacementBodyPct, 0.35)
\tparams.MaxOpposingWickPct = math.Min(math.Max(params.MaxOpposingWickPct, 0.20), 0.60)
\tparams.MinDisplacementATR = math.Max(params.MinDisplacementATR, 0.35)
\tparams.MinDisplacementVolume = math.Max(params.MinDisplacementVolume, 0.80)
\tparams.DisplacementATRLookback = max(params.DisplacementATRLookback, 5)
\tparams.DisplacementVolLookback = max(params.DisplacementVolLookback, 5)
\tparams.RequireRejection = true
\tparams.MinTargetCostMultiple = math.Max(params.MinTargetCostMultiple, 2)
\tparams.RiskRewardRatio = fixedRiskRewardRatio
\treturn params
}
'''
rep(old_norm, new_norm, "NormalizeParams")

rep(
    '''func targetPathClear(entry, target float64, bias HTFBias, params SBParams) bool {
\tif entry <= 0 || target <= 0 || bias.TargetBarrier <= 0 || bias.SweepATR <= 0 {
\t\treturn false
\t}
''',
    '''func targetPathClear(entry, target float64, bias HTFBias, params SBParams) bool {
\tif entry <= 0 || target <= 0 {
\t\treturn false
\t}
\tif bias.TargetBarrier <= 0 || bias.SweepATR <= 0 {
\t\treturn true
\t}
''',
    "targetPathClear",
)

old_context = '''\ttrend := classifyH4Trend(window, params.HTFPivotStrength)
\tif action == models.SignalBuy && trend == "空頭結構" {
\t\treturn htfContext{}
\t}
\tif action == models.SignalSell && trend == "多頭結構" {
\t\treturn htfContext{}
\t}

\tzone := ""
\tedgeDistance := atr * params.HTFZoneATRMultiple
\tif action == models.SignalBuy {
\t\tif sweep.Low > equilibrium {
\t\t\treturn htfContext{}
\t\t}
\t\tif sweep.Low <= low+edgeDistance {
\t\t\tzone = "H4折價SNR"
\t\t}
\t} else {
\t\tif sweep.High < equilibrium {
\t\t\treturn htfContext{}
\t\t}
\t\tif sweep.High >= high-edgeDistance {
\t\t\tzone = "H4溢價SNR"
\t\t}
\t}
\tif zone == "" {
\t\tzone = matchingFreshH4Zone(window, sweep, action, atr)
\t}
\tif zone == "" {
\t\treturn htfContext{}
\t}
'''
new_context = '''\ttrend := classifyH4Trend(window, params.HTFPivotStrength)
\tif !params.RelaxHTFContext {
\t\tif action == models.SignalBuy && trend == "空頭結構" {
\t\t\treturn htfContext{}
\t\t}
\t\tif action == models.SignalSell && trend == "多頭結構" {
\t\t\treturn htfContext{}
\t\t}
\t}

\tzone := ""
\tedgeDistance := atr * params.HTFZoneATRMultiple
\tif action == models.SignalBuy {
\t\tif !params.RelaxHTFContext && sweep.Low > equilibrium {
\t\t\treturn htfContext{}
\t\t}
\t\tif sweep.Low <= low+edgeDistance {
\t\t\tzone = "H4折價SNR"
\t\t} else if params.RelaxHTFContext && sweep.Low <= equilibrium {
\t\t\tzone = "H4折價區"
\t\t}
\t} else {
\t\tif !params.RelaxHTFContext && sweep.High < equilibrium {
\t\t\treturn htfContext{}
\t\t}
\t\tif sweep.High >= high-edgeDistance {
\t\t\tzone = "H4溢價SNR"
\t\t} else if params.RelaxHTFContext && sweep.High >= equilibrium {
\t\t\tzone = "H4溢價區"
\t\t}
\t}
\tif zone == "" {
\t\tzone = matchingFreshH4Zone(window, sweep, action, atr)
\t}
\tif zone == "" && params.RelaxHTFContext {
\t\tzone = "H4背景觀察"
\t}
\tif zone == "" {
\t\treturn htfContext{}
\t}
'''
rep(old_context, new_context, "buildHTFContext")

rep(
    '''\t\tif barrier <= 0 {
\t\t\tcontinue
\t\t}

\t\tout = append(out, h1Sweep{''',
    '''\t\tif barrier <= 0 && !params.RelaxHTFContext {
\t\t\tcontinue
\t\t}

\t\tout = append(out, h1Sweep{''',
    "barrier gate",
)

p.write_text(s, encoding="utf-8")
print("ICT2022 V4 patch applied")
