-- Add per-group Kiro forced prompt cache creation control.
ALTER TABLE groups
  ADD COLUMN IF NOT EXISTS kiro_cache_force_creation_enabled BOOLEAN NOT NULL DEFAULT FALSE;
