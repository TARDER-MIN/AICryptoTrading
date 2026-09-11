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
