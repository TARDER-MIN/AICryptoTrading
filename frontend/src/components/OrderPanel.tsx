import { useState } from "react";
import { api } from "../api/client";
import { useI18n } from "../i18n/I18nContext";
import type { OrderSide, Settings } from "../types";

interface Props {
  symbol: string;
  settings: Settings | null;
  onFilled?: () => void;
}

// No quantity input: every order (manual or automatic) is sized
// server-side from the fixed margin-per-order setting (notional = margin *
// leverage) via the same validation path (internal/autotrader.
// PlaceManualOrder -> bingx.FilterCache.MaxQtyForCap) - there's nothing
// for the user to size manually.
export function OrderPanel({ symbol, settings, onFilled }: Props) {
  const { t } = useI18n();
  const [status, setStatus] = useState<string | null>(null);
  const [submitting, setSubmitting] = useState<OrderSide | null>(null);

  const submit = async (side: OrderSide) => {
    setSubmitting(side);
    setStatus(null);
    try {
      const order = await api.placeOrder(symbol, side);
      setStatus(
        t("order.sent", { side: order.side, qty: order.qty, notional: order.notional_usd.toFixed(2), status: order.status }),
      );
      onFilled?.();
    } catch (e) {
      setStatus(t("order.error", { error: String(e) }));
    } finally {
      setSubmitting(null);
    }
  };

  return (
    <div className="panel">
      <h3>{t("order.title", { symbol })}</h3>
      <p className="muted small">
        {t("order.description", {
          margin: settings?.margin_usd ?? "?",
          leverage: settings?.leverage ?? "?",
          marginType: settings?.margin_type ?? "",
          notional: settings ? (settings.margin_usd * settings.leverage).toFixed(2) : "?",
        })}
      </p>
      <div className="order-form">
        <div className="side-toggle">
          <button className="buy" disabled={submitting !== null} onClick={() => submit("BUY")}>
            {submitting === "BUY" ? t("order.buying") : t("order.buy")}
          </button>
          <button className="sell" disabled={submitting !== null} onClick={() => submit("SELL")}>
            {submitting === "SELL" ? t("order.selling") : t("order.sell")}
          </button>
        </div>
        {status && <p className="order-status">{status}</p>}
      </div>
    </div>
  );
}
