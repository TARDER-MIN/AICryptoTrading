-- Data-only safety migration: an older strategy cannot safely inherit the
-- location model's optimized values. Rollback therefore keeps execution off.
UPDATE settings
SET autotrade_enabled = false,
    updated_at = now()
WHERE id = 1;
