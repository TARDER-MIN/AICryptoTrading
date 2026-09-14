-- Data-only safety migration: old optimized values cannot be reconstructed
-- reliably. A rollback therefore keeps automatic order execution paused.
UPDATE settings
SET autotrade_enabled = false,
    updated_at = now()
WHERE id = 1;
