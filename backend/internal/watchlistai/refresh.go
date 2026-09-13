package watchlistai

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"cryptotrading/internal/ai"
	"cryptotrading/internal/bingx"
	"cryptotrading/internal/strategy"
)

// Pick is one AI-selected symbol, carrying both the model's rationale and
// the candidate stats it was judged on (for later display/audit).
type Pick struct {
	Rank              int     `json:"rank"`
	Symbol            string  `json:"symbol"`
	Rationale         string  `json:"rationale"`
	Price             float64 `json:"price"`
	ChangePct24h      float64 `json:"change_pct_24h"`
	QuoteVolume24h    float64 `json:"quote_volume_24h"`
	RecentSignalCount int     `json:"recent_signal_count"`
}

// Result is one completed daily-selection run.
type Result struct {
	RunDate        string    `json:"run_date"` // YYYY-MM-DD, UTC
	CandidateCount int       `json:"candidate_count"`
	Picks          []Pick    `json:"picks"`
	CreatedAt      time.Time `json:"created_at"`
}

// Refresh fetches the tradable-and-liquid candidate pool, backtest-scans
// each candidate for how often the currently-live HTF 3+1
// rules (sbParams) actually fired on it recently (see
// AnnotateSignalFrequency), asks Claude to pick pickCount of them -
// primarily by that signal frequency, not surface stats - records the run
// for audit, and replaces the live watchlist with the picks - using the
// same `enabled` boolean the manual add/remove endpoints use (disable
// everything, then re-enable/insert exactly the picked symbols) rather than
// a DELETE+INSERT, since other parts of this schema already rely on
// `enabled` as the single source of truth for "is this symbol live".
func Refresh(ctx context.Context, pool *pgxpool.Pool, aiClient *ai.Client, bclient *bingx.Client, filters *bingx.FilterCache, capUSD float64, poolSize, pickCount int, sbParams strategy.SBParams, interval string) (*Result, error) {
	candidates, err := FetchCandidates(ctx, bclient, filters, capUSD, poolSize)
	if err != nil {
		return nil, fmt.Errorf("watchlistai: fetch candidates: %w", err)
	}
	if len(candidates) == 0 {
		return nil, fmt.Errorf("watchlistai: no cap-feasible candidates found (capUSD=%.2f)", capUSD)
	}
	if pickCount > len(candidates) {
		pickCount = len(candidates)
	}

	candidates = AnnotateSignalFrequency(ctx, bclient, candidates, sbParams, interval)

	aiCandidates := make([]ai.WatchlistCandidate, len(candidates))
	bySymbol := make(map[string]Candidate, len(candidates))
	for i, c := range candidates {
		aiCandidates[i] = ai.WatchlistCandidate{
			Symbol: c.Symbol, Price: c.Price,
			ChangePct24h: c.ChangePct24h, QuoteVolume24h: c.QuoteVolume24h,
			FundingRate: c.FundingRate, RecentSignalCount: c.RecentSignalCount,
		}
		bySymbol[c.Symbol] = c
	}
	picks, err := aiClient.SelectDailyWatchlist(ctx, aiCandidates, pickCount, SignalScanLookbackDays)
	if err != nil {
		return nil, fmt.Errorf("watchlistai: ai selection: %w", err)
	}

	// The AI is instructed to pick only from the candidate list, but never
	// trust that blindly - silently drop (rather than error out on) any
	// symbol it hallucinated outside the set, since a partial valid result
	// is still useful and shouldn't block the whole refresh.
	valid := make([]Pick, 0, len(picks))
	for i, p := range picks {
		c, ok := bySymbol[p.Symbol]
		if !ok {
			continue
		}
		valid = append(valid, Pick{
			Rank: i + 1, Symbol: p.Symbol, Rationale: p.Rationale,
			Price: c.Price, ChangePct24h: c.ChangePct24h, QuoteVolume24h: c.QuoteVolume24h,
			RecentSignalCount: c.RecentSignalCount,
		})
	}
	if len(valid) == 0 {
		return nil, fmt.Errorf("watchlistai: ai returned no picks matching the candidate set")
	}

	runDate := time.Now().UTC().Format("2006-01-02")
	var runID int
	var createdAt time.Time

	tx, err := pool.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("watchlistai: begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	err = tx.QueryRow(ctx, `
		INSERT INTO ai_watchlist_runs (run_date, candidate_count)
		VALUES ($1, $2)
		ON CONFLICT (run_date) DO UPDATE SET candidate_count = EXCLUDED.candidate_count
		RETURNING id, created_at
	`, runDate, len(candidates)).Scan(&runID, &createdAt)
	if err != nil {
		return nil, fmt.Errorf("watchlistai: insert run: %w", err)
	}

	if _, err := tx.Exec(ctx, `DELETE FROM ai_watchlist_picks WHERE run_id = $1`, runID); err != nil {
		return nil, fmt.Errorf("watchlistai: clear prior picks: %w", err)
	}
	for _, p := range valid {
		if _, err := tx.Exec(ctx, `
			INSERT INTO ai_watchlist_picks (run_id, rank, symbol, rationale, price, price_change_pct_24h, quote_volume_24h, recent_signal_count)
			VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		`, runID, p.Rank, p.Symbol, p.Rationale, p.Price, p.ChangePct24h, p.QuoteVolume24h, p.RecentSignalCount); err != nil {
			return nil, fmt.Errorf("watchlistai: insert pick %s: %w", p.Symbol, err)
		}
	}

	if _, err := tx.Exec(ctx, `UPDATE watchlist SET enabled = false`); err != nil {
		return nil, fmt.Errorf("watchlistai: disable existing watchlist: %w", err)
	}
	for _, p := range valid {
		if _, err := tx.Exec(ctx, `
			INSERT INTO watchlist (symbol) VALUES ($1)
			ON CONFLICT (symbol) DO UPDATE SET enabled = true
		`, p.Symbol); err != nil {
			return nil, fmt.Errorf("watchlistai: enable pick %s: %w", p.Symbol, err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("watchlistai: commit: %w", err)
	}

	return &Result{
		RunDate: runDate, CandidateCount: len(candidates), Picks: valid, CreatedAt: createdAt,
	}, nil
}

// HasRunForToday reports whether a watchlist selection run already exists
// for the current UTC date - crypto trades every day with no weekday/
// timezone skip logic needed, unlike the equities-market sibling project.
func HasRunForToday(ctx context.Context, pool *pgxpool.Pool) (bool, error) {
	runDate := time.Now().UTC().Format("2006-01-02")
	var exists bool
	err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM ai_watchlist_runs WHERE run_date = $1)`, runDate).Scan(&exists)
	return exists, err
}

// LatestResult returns the most recent watchlist selection run, if any.
func LatestResult(ctx context.Context, pool *pgxpool.Pool) (*Result, error) {
	var res Result
	var runID int
	var runDate time.Time
	err := pool.QueryRow(ctx, `
		SELECT id, run_date, candidate_count, created_at
		FROM ai_watchlist_runs ORDER BY run_date DESC LIMIT 1
	`).Scan(&runID, &runDate, &res.CandidateCount, &res.CreatedAt)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("watchlistai: load latest run: %w", err)
	}
	res.RunDate = runDate.Format("2006-01-02")

	rows, err := pool.Query(ctx, `
		SELECT rank, symbol, rationale, price, price_change_pct_24h, quote_volume_24h, recent_signal_count
		FROM ai_watchlist_picks WHERE run_id = $1 ORDER BY rank
	`, runID)
	if err != nil {
		return nil, fmt.Errorf("watchlistai: load latest picks: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var p Pick
		if err := rows.Scan(&p.Rank, &p.Symbol, &p.Rationale, &p.Price, &p.ChangePct24h, &p.QuoteVolume24h, &p.RecentSignalCount); err != nil {
			return nil, fmt.Errorf("watchlistai: scan pick: %w", err)
		}
		res.Picks = append(res.Picks, p)
	}
	return &res, nil
}
