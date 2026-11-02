-- The mass-deletion guard is gone (docs/design.md "No deletion threshold"),
-- and with it the halt and resume state.
ALTER TABLE sources DROP COLUMN halted_reason, DROP COLUMN resumed;
