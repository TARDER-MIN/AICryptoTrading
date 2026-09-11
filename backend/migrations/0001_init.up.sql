-- Candle volume is fractional base-asset volume (crypto, not integer share
-- counts like the TWSE reference project), so NUMERIC not BIGINT.
CREATE TABLE candles_15m (
    symbol  TEXT NOT NULL,
    ts      TIMESTAMPTZ NOT NULL,
    open    NUMERIC(24,10) NOT NULL,
    high    NUMERIC(24,10) NOT NULL,
    low     NUMERIC(24,10) NOT NULL,
    close   NUMERIC(24,10) NOT NULL,
    volume  NUMERIC(24,8) NOT NULL DEFAULT 0,
    PRIMARY KEY (symbol, ts)
);

CREATE TABLE ai_signals (
    id             BIGSERIAL PRIMARY KEY,
    symbol         TEXT NOT NULL,
    candle_ts      TIMESTAMPTZ,
    action         TEXT NOT NULL CHECK (action IN ('BUY','SELL','HOLD')),
    confidence     NUMERIC(4,3),
    entry_hint     NUMERIC(24,10),
    stop_loss      NUMERIC(24,10),
    take_profit    NUMERIC(24,10),
    funding_rate   NUMERIC(10,6),
    rationale      TEXT,
    model          TEXT,
    raw_response   JSONB,
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_ai_signals_symbol ON ai_signals(symbol, created_at DESC);

-- Local mirror of REAL BingX futures orders (history/UI). The exchange is
-- the source of truth; this table is a read-optimized cache kept current by
-- the user-data-stream ORDER_TRADE_UPDATE events.
CREATE TABLE orders (
    id               BIGSERIAL PRIMARY KEY,
    binance_order_id BIGINT UNIQUE,
    client_order_id  TEXT,
    symbol           TEXT NOT NULL,
    side             TEXT NOT NULL CHECK (side IN ('BUY','SELL')),
    order_type       TEXT NOT NULL DEFAULT 'MARKET',
    reduce_only      BOOLEAN NOT NULL DEFAULT false,
    qty              NUMERIC(24,10) NOT NULL,
    notional_usd     NUMERIC(20,4),
    leverage         INT,
    status           TEXT NOT NULL DEFAULT 'NEW',
    source           TEXT NOT NULL CHECK (source IN ('manual','auto')),
    filled_price     NUMERIC(24,10),
    filled_at        TIMESTAMPTZ,
    raw_response     JSONB,
    submitted_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at       TIMESTAMPTZ NOT NULL DEFAULT now()
);
CREATE INDEX idx_orders_symbol ON orders(symbol, submitted_at DESC);

-- Transparency log: every 15m strategy evaluation per symbol, whether or not
-- it resulted in an order - including holds and skips, and why.
CREATE TABLE auto_trade_log (
    id            BIGSERIAL PRIMARY KEY,
    symbol        TEXT NOT NULL,
    ts            TIMESTAMPTZ NOT NULL DEFAULT now(),
    rule_action   TEXT NOT NULL,
    rule_reason   TEXT,
    ai_signal_id  BIGINT REFERENCES ai_signals(id),
    decision      TEXT NOT NULL CHECK (decision IN ('executed','skipped','hold','error')),
    skip_reason   TEXT,
    order_id      BIGINT REFERENCES orders(id)
);
CREATE INDEX idx_autotrade_log_symbol_ts ON auto_trade_log(symbol, ts DESC);

-- Single-row runtime-mutable settings. notional_cap_usd is clamped to <= 10
-- at the API layer regardless of what's stored here - see internal/settings.
CREATE TABLE settings (
    id                 INT PRIMARY KEY DEFAULT 1 CHECK (id = 1),
    autotrade_enabled  BOOLEAN NOT NULL DEFAULT true,
    leverage           INT NOT NULL DEFAULT 3,
    notional_cap_usd   NUMERIC(10,2) NOT NULL DEFAULT 10.00,
    margin_type        TEXT NOT NULL DEFAULT 'ISOLATED',
    updated_at         TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO settings (id) VALUES (1);

CREATE TABLE watchlist (
    id        SERIAL PRIMARY KEY,
    symbol    TEXT NOT NULL UNIQUE,
    enabled   BOOLEAN NOT NULL DEFAULT true,
    added_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);
INSERT INTO watchlist (symbol) VALUES
    ('BTC-USDT'),('ETH-USDT'),('SOL-USDT'),('XRP-USDT'),
    ('ADA-USDT'),('DOGE-USDT'),('TRX-USDT'),('1000PEPE-USDT');
