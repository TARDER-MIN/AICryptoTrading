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
- Existing Silver Bullet strategy, AI confirmation, backtest, dashboard,
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

The optimizer requests up to 180 days from BingX, but the perpetual-futures
5-minute endpoint currently returns only about 45 days. The dashboard always
shows the actual first/last timestamps and candle count; it never substitutes
spot candles for missing futures history.

Each run uses the current 20 most-liquid crypto perpetuals. The first 80% is
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
