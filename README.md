# AICryptoTrading — BingX edition

This branch is the BingX USDT-M perpetual-futures edition of AICryptoTrading.
The original Binance implementation remains unchanged on the `main` branch.

> **Real-money warning:** when API credentials, Anthropic credentials, and
> auto-trading are enabled, this application can submit real BingX orders.
> Start with a restricted API key and a very small margin setting.

## What works

- BingX contract discovery and exchange sizing filters
- 5-minute candles, ticker statistics, mark price, and funding rate
- Account balance and live position reconciliation
- Hedge-mode or one-way-mode market orders
- Isolated/cross margin and leverage configuration
- Stop-market and take-profit-market protective orders
- HTF 3+1 strategy, AI confirmation, dual-range backtest, dashboard,
  watchlist, kill switch, cooldown, and daily order limit

Market candles and funding use REST polling (8 and 10 seconds). Account and
order state is reconciled every 15 seconds. This is intentional: it remains
functional on networks that accept HTTPS but silently interrupt long-lived
WebSocket connections.

## Branches

| Branch | Exchange | Symbol format | Credentials |
|---|---|---|---|
| `main` | Binance USDS-M | `BTCUSDT` | `BINANCE_API_KEY`, `BINANCE_API_SECRET` |
| `bingx` | BingX USDT-M | `BTC-USDT` | `BINGX_API_KEY`, `BINGX_API_SECRET` |

## Start

Requirements: Docker Desktop with Docker Compose.

```bash
git switch bingx
cp .env.example .env
docker compose up --build
```

Then open `http://localhost:5290`.

For PowerShell, `.\backend\restart-detached.ps1` remains available for the
native backend workflow.

## Required configuration

```dotenv
BINGX_API_KEY=
BINGX_API_SECRET=
BINGX_REST_BASE_URL=https://open-api.bingx.com
BINGX_WS_BASE_URL=wss://open-api-swap.bingx.com/swap-market
BINGX_POSITION_MODE=HEDGE
ANTHROPIC_API_KEY=
```

`BINGX_POSITION_MODE` must match the account:

- `HEDGE` (default): separate long/short positions; orders use `LONG` or
  `SHORT` and do not send `reduceOnly`.
- `ONEWAY`: single net position; orders use `BOTH` and closing orders send
  `reduceOnly=true`.

Other important settings are documented in `.env.example`, including
`MARGIN_USD`, `LEVERAGE_DEFAULT`, `MARGIN_TYPE`, and
`AUTOTRADE_ENABLED_DEFAULT`.

## Backtest interpretation

The active deterministic strategy is fixed as follows:

1. Only external higher-timeframe liquidity counts: the previous UTC-day
   high/low (PDH/PDL), with the extreme of prior fully completed H4 candles as
   a fallback. Ordinary internal H1 highs and lows cannot set direction.
2. Direction follows the move after the liquidity raid, not the raid wick. A
   closed H1 candle that sweeps above external highs and reclaims below creates
   SELL bias; one that sweeps below external lows and reclaims above creates
   BUY bias. A close outside is a breakout, not a reversal, and a candle that
   raids both sides is ignored.
3. The H1 raid must penetrate the level by at least 0.05 H1 ATR and reclaim
   inside it by at least 0.10 H1 ATR. M5 must then close through recent
   structure (CHOCH) with a displacement body of at least 60% of its range,
   0.8 M5 ATR, and 1.2 times prior average M5 volume while leaving an FVG. The
   last opposite M5 candle is tracked as an OB.
4. Entry is allowed only on the first retest of a fresh FVG or OB and only
   when that candle rejects the zone. A first touch without valid rejection
   consumes the zone.
5. The stop is beyond the triggering FVG/OB invalidation edge plus buffer.
   The entire take-profit is fixed at exactly 1:1.5. The planned target must
   also span at least five times the estimated round-trip fee and slippage;
   smaller setups are skipped. There is no session gate.

All H1 bars are built from already-closed M5 candles. A partial H1 candle can
never create direction in live evaluation or historical replay.

### Same-date BingX comparison

When a saved report exists, the comparison button reuses that report's exact
symbol list and first/last timestamps so a strategy change can be compared on
the same sample. On a clean installation it selects the current 20 most-liquid
crypto perpetuals and requests up to 180 days from BingX. The perpetual-futures
5-minute endpoint currently returns only about 45 days. The dashboard always
shows the actual first/last timestamps and candle count; it never substitutes
spot candles for missing futures history.

Every report records the strategy-rule version. After an upgrade, an older
report remains visible only as a labelled baseline for reusing its exact
symbols and dates; it is never presented as evidence for the new rules. The
upgrade resets old active metrics and keeps automatic trading paused.

The first 80% is
the development window: every fixed candidate is scored over three
chronological validation folds and must remain profitable across time and on
both BUY and SELL trades. The final 20% stays untouched until the robust
candidate is fixed and is used only as a deployment gate. A trade crossing a
split boundary is excluded from the earlier interval so future exit prices
cannot leak backward. The
reported return, Sharpe, profit factor, and drawdown use net per-trade returns
after the configured taker-fee and slippage assumptions:

```dotenv
BACKTEST_TAKER_FEE_PCT=0.05
BACKTEST_SLIPPAGE_PCT=0.02
```

Those values are percentage points per side, so the defaults deduct about
0.14% for a complete entry/exit. When credentials are available, the
optimizer replaces the configured fee with the account's current BingX taker
rate. It also fetches BingX's historical funding settlements and applies each
one crossed by a simulated position using its settlement mark price. A
positive funding-cost value is paid by the position; a negative value is
funding income. If complete funding history is unavailable for any tested
symbol, funding is omitted for every symbol and the dashboard says so rather
than mixing unlike cost coverage. Return and drawdown are cumulative
unleveraged price-return percentage points, not account-equity returns. The
current-volume universe also introduces selection bias, so even the final-test
result is evidence, not a promise of future profitability.

### At-least-one-year broad study

The second dashboard button reuses the exact symbol list and history end time
from the latest BingX run, then requests one full calendar year of public
Binance USDT-perpetual M5 history. This is necessary because BingX's M5 API
does not currently supply a full year. The report clearly labels Binance as a
proxy venue, lists symbols with no equivalent or partial listing history, and
uses Binance's own historical funding settlements while retaining the user's
BingX taker-fee assumption. Prices, wicks, liquidity, and funding can differ
between venues, so this study is supporting robustness evidence rather than a
claim that old Binance fills would have occurred on BingX.

The one-year run uses the same cost-aware 60% training, 20% selection
validation, untouched final 20%, direction breakdowns, and walk-forward
checks. It can take several minutes because roughly two million public M5
candles must be downloaded under the exchange rate limit. Its report is saved
separately and **can never update active live parameters**, even if every gate
passes. Automatic trading remains paused throughout.

The latest report also keeps the untouched final test explainable instead of
showing only one aggregate number. It breaks that period down by contract,
long/short direction, and UTC entry date; reports target, stop, and
end-of-data exits; and separately shows completed trades so positions merely
marked to market at the final candle cannot hide inside the total. The
headline result still includes every trade to avoid selectively deleting bad
outcomes.

Four expanding-window walk-forward folds provide a second stability check.
The full history is divided into five chronological blocks. Each fold selects
parameters from only the blocks already available at that time and tests the
next block; the four out-of-sample blocks are then combined. A fold passes
only with at least 10 trades, positive net return, profit factor above 1, and
positive per-trade Sharpe. Walk-forward outcomes never feed back into
candidate ranking or rewrite the untouched final-test result, but they can
block deployment. Parameters are applied only when a robust development
candidate exists, at least three of four adaptive walk-forward folds pass,
the aggregate walk-forward profit factor is at least 1.20, and the completed
final-test sample is profitable overall and independently for both BUY and
SELL directions. A rejected run saves its full report without replacing the
active parameters. Triggering research also explicitly keeps automatic order
execution paused.

## Safety model

Every manual and automatic order is sized through the exchange filter cache.
The application rounds quantity and trigger prices to BingX contract
precision and refuses orders below the exchange minimum. Automatic entries
also require the rule signal, AI confirmation, kill-switch permission,
per-symbol cooldown, and daily order-cap checks.

The local `orders.binance_order_id` database column and matching JSON key are
kept for migration compatibility. In this branch they store a BingX order ID;
renaming the persisted column would risk breaking existing databases.

## Verification

```bash
cd backend
go test ./...

cd ../frontend
npm ci
npm run build
```

GitHub Actions runs both checks on every push and pull request. Public BingX
market endpoints are usable without credentials; signed account and order
paths require the user's API key and therefore must be validated on a demo or
small restricted account before production use.
