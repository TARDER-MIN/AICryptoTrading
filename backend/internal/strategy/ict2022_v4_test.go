package strategy

import (
	"math"
	"testing"
	"time"

	"cryptotrading/internal/models"
)

func TestICT2022V4DefaultResearchParamsAreRelaxed(t *testing.T) {
	if StrategyVersion != "ict2022_sweep_mss_fvg_v4" {
		t.Fatalf("StrategyVersion = %q, want ict2022_sweep_mss_fvg_v4", StrategyVersion)
	}
	p := DefaultSBParams()
	if p.MinFVGSizePct != 0.05 {
		t.Fatalf("MinFVGSizePct = %.4f, want 0.05", p.MinFVGSizePct)
	}
	if p.MaxBarsForSweep != 6 {
		t.Fatalf("MaxBarsForSweep = %d, want 6", p.MaxBarsForSweep)
	}
	if p.MaxBarsForRetest != 12 {
		t.Fatalf("MaxBarsForRetest = %d, want 12", p.MaxBarsForRetest)
	}
	if p.CHOCHPivotStrength != 1 {
		t.Fatalf("CHOCHPivotStrength = %d, want 1", p.CHOCHPivotStrength)
	}
	if math.Abs(p.MinDisplacementATR-0.50) > 1e-9 {
		t.Fatalf("MinDisplacementATR = %.4f, want 0.50", p.MinDisplacementATR)
	}
	if math.Abs(p.MinDisplacementVolume-1.00) > 1e-9 {
		t.Fatalf("MinDisplacementVolume = %.4f, want 1.00", p.MinDisplacementVolume)
	}
	if math.Abs(p.MinDisplacementBodyPct-0.50) > 1e-9 {
		t.Fatalf("MinDisplacementBodyPct = %.4f, want 0.50", p.MinDisplacementBodyPct)
	}
	if math.Abs(p.MaxOpposingWickPct-0.35) > 1e-9 {
		t.Fatalf("MaxOpposingWickPct = %.4f, want 0.35", p.MaxOpposingWickPct)
	}
	if math.Abs(p.MinTargetCostMultiple-3.0) > 1e-9 {
		t.Fatalf("MinTargetCostMultiple = %.4f, want 3.0", p.MinTargetCostMultiple)
	}
	if !p.RelaxHTFContext {
		t.Fatal("DefaultSBParams must enable relaxed HTF context for V4 research")
	}
}

func TestICT2022V4NormalizeDoesNotRestoreOldHardDisplacementFloors(t *testing.T) {
	p := DefaultSBParams()
	p.MinDisplacementATR = 0.45
	p.MinDisplacementVolume = 0.90
	p.MinDisplacementBodyPct = 0.45
	p.MaxOpposingWickPct = 0.40
	p.MinTargetCostMultiple = 2.5

	got := NormalizeParams(p)
	if math.Abs(got.MinDisplacementATR-0.45) > 1e-9 {
		t.Fatalf("ATR threshold was re-tightened to %.4f", got.MinDisplacementATR)
	}
	if math.Abs(got.MinDisplacementVolume-0.90) > 1e-9 {
		t.Fatalf("volume threshold was re-tightened to %.4f", got.MinDisplacementVolume)
	}
	if math.Abs(got.MinDisplacementBodyPct-0.45) > 1e-9 {
		t.Fatalf("body threshold was re-tightened to %.4f", got.MinDisplacementBodyPct)
	}
	if math.Abs(got.MaxOpposingWickPct-0.40) > 1e-9 {
		t.Fatalf("wick threshold was re-tightened to %.4f", got.MaxOpposingWickPct)
	}
	if math.Abs(got.MinTargetCostMultiple-2.5) > 1e-9 {
		t.Fatalf("cost multiple was re-tightened to %.4f", got.MinTargetCostMultiple)
	}
}

func TestICT2022V4RelaxedHTFContextKeepsCounterTrendSweepAsContext(t *testing.T) {
	values := []struct {
		high, low, close float64
	}{
		{100, 96, 98}, {102, 97, 100}, {105, 98, 103},
		{103, 99, 101}, {104, 100, 102}, {106, 101, 104}, {107, 102, 106},
	}
	bars := make([]h1Bar, 0, len(values))
	for i, value := range values {
		barStart := htfBase.Add(time.Duration(i*4) * time.Hour)
		bars = append(bars, h1Bar{
			Start: barStart, End: barStart.Add(4 * time.Hour),
			Open: value.close - 0.5, High: value.high, Low: value.low, Close: value.close,
		})
	}
	p := DefaultSBParams()
	p.HTFStructureLookback = len(bars)
	p.HTFPivotStrength = 2
	p.RelaxHTFContext = true
	sweep := h1Bar{Low: 105, High: 108, Open: 106, Close: 106.5}
	context := buildHTFContext(bars, sweep, models.SignalSell, p)
	if !context.Valid {
		t.Fatalf("relaxed V4 context rejected counter-trend H1 sweep: %#v", context)
	}
	if context.Trend != "多頭結構" {
		t.Fatalf("trend = %q, want 多頭結構 retained as context", context.Trend)
	}
}

func TestICT2022V4MissingOpposingBarrierDoesNotAutoReject(t *testing.T) {
	p := DefaultSBParams()
	bias := HTFBias{Action: models.SignalBuy, SweepATR: 2}
	if !targetPathClear(100, 103, bias, p) {
		t.Fatal("missing confirmed opposing structure should not auto-reject a valid 1:1.5 path")
	}
}

func TestICT2022V4DetectsValidSweepEvenWhenNoForwardBarrierExists(t *testing.T) {
	bars := make([]h1Bar, 0, 49)
	for i := 0; i < 48; i++ {
		start := htfBase.Add(time.Duration(i) * time.Hour)
		bars = append(bars, h1Bar{
			Start: start, End: start.Add(time.Hour),
			Open: 100, High: 100.5, Low: 99.5, Close: 100, Volume: 10,
		})
	}
	start := htfBase.Add(48 * time.Hour)
	bars = append(bars, h1Bar{
		Start: start, End: start.Add(time.Hour),
		Open: 100, High: 101.2, Low: 98.0, Close: 101.0, Volume: 20,
	})

	p := DefaultSBParams()
	p.SwingLookback = 4
	p.HTFStructureLookback = 4
	p.RelaxHTFContext = true
	sweeps := detectH1Sweeps(bars, p)
	if len(sweeps) == 0 {
		t.Fatal("valid lower-liquidity sweep was discarded only because no opposing barrier existed")
	}
	last := sweeps[len(sweeps)-1]
	if last.Action != models.SignalBuy {
		t.Fatalf("lower-liquidity sweep action = %s, want BUY", last.Action)
	}
	if last.TargetBarrier != 0 {
		t.Fatalf("fixture unexpectedly produced barrier %.4f; test requires no barrier", last.TargetBarrier)
	}
}
