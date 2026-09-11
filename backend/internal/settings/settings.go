// Package settings manages the single-row runtime-mutable trading
// configuration: the auto-trading kill switch, leverage, margin type, and
// the fixed per-order margin amount. Sizing is margin-based, not
// notional-based: every order commits MarginUSD of margin, and the
// resulting position notional (MarginUSD * Leverage) is whatever that
// implies - there is deliberately no separate absolute notional ceiling.
// This was an explicit choice: raising leverage is understood to scale
// position size proportionally, by design, not a loophole to guard against.
package settings

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"

	"cryptotrading/internal/models"
)

// MinLeverage/MaxLeverage bound what the settings API will accept, so a
// typo or a slip of the finger in the dashboard can't silently set
// dangerous leverage. 25x is BingX's own cap for several of this
// watchlist's lower-priced pairs (the exact per-symbol max is lower for
// some majors).
const (
	MinLeverage = 1
	MaxLeverage = 25
)

// MinMarginUSD/MaxMarginUSD are a loose typo-guard on the fixed per-order
// margin amount (e.g. catches "500" fat-fingered for "5"), not a meaningful
// behavioral ceiling - the user's actual value (5 USDT by default) is well
// inside this range.
const (
	MinMarginUSD = 1.0
	MaxMarginUSD = 50.0
)

func Get(ctx context.Context, pool *pgxpool.Pool) (models.Settings, error) {
	var s models.Settings
	err := pool.QueryRow(ctx, `
		SELECT autotrade_enabled, leverage, margin_usd, margin_type, updated_at
		FROM settings WHERE id = 1
	`).Scan(&s.AutotradeEnabled, &s.Leverage, &s.MarginUSD, &s.MarginType, &s.UpdatedAt)
	if err != nil {
		return models.Settings{}, err
	}
	s.EffectiveNotionalUSD = s.MarginUSD * float64(s.Leverage)
	return s, nil
}

type UpdateInput struct {
	AutotradeEnabled *bool
	Leverage         *int
	MarginUSD        *float64
	MarginType       *string
}

func Update(ctx context.Context, pool *pgxpool.Pool, in UpdateInput) (models.Settings, error) {
	current, err := Get(ctx, pool)
	if err != nil {
		return models.Settings{}, err
	}

	if in.AutotradeEnabled != nil {
		current.AutotradeEnabled = *in.AutotradeEnabled
	}
	if in.Leverage != nil {
		lev := *in.Leverage
		if lev < MinLeverage || lev > MaxLeverage {
			return models.Settings{}, fmt.Errorf("settings: leverage must be between %d and %d, got %d", MinLeverage, MaxLeverage, lev)
		}
		current.Leverage = lev
	}
	if in.MarginUSD != nil {
		margin := *in.MarginUSD
		if margin < MinMarginUSD || margin > MaxMarginUSD {
			return models.Settings{}, fmt.Errorf("settings: margin_usd must be between %.2f and %.2f, got %.2f", MinMarginUSD, MaxMarginUSD, margin)
		}
		current.MarginUSD = margin
	}
	if in.MarginType != nil {
		mt := *in.MarginType
		if mt != "ISOLATED" && mt != "CROSSED" {
			return models.Settings{}, fmt.Errorf("settings: margin_type must be ISOLATED or CROSSED, got %q", mt)
		}
		current.MarginType = mt
	}

	err = pool.QueryRow(ctx, `
		UPDATE settings SET autotrade_enabled = $1, leverage = $2, margin_usd = $3, margin_type = $4, updated_at = now()
		WHERE id = 1
		RETURNING updated_at
	`, current.AutotradeEnabled, current.Leverage, current.MarginUSD, current.MarginType).Scan(&current.UpdatedAt)
	if err != nil {
		return models.Settings{}, err
	}
	current.EffectiveNotionalUSD = current.MarginUSD * float64(current.Leverage)
	return current, nil
}
