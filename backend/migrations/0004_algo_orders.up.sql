-- Stop-loss/take-profit orders placed alongside a fresh auto entry go
-- through BingX's Algo Order service (POST/GET/DELETE /fapi/v1/algoOrder),
-- which USDS-M Futures migrated conditional orders (STOP_MARKET/
-- TAKE_PROFIT_MARKET/etc.) to on 2025-12-09 - the legacy POST /fapi/v1/order
-- now rejects those types with -4120. Algo orders are identified by algoId,
-- a separate ID space from binance_order_id, so both columns coexist:
-- exactly one is set depending on which kind of order a row represents.
ALTER TABLE orders ADD COLUMN algo_id BIGINT UNIQUE;
