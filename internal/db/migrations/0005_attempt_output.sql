-- What an attempt produced, beyond its artifacts.
--
-- A probe's findings are the first case: the control plane parses them into
-- tracks, and keeping the tool's own output verbatim means a change of mind
-- about which fields matter does not require re-probing every asset.
alter table task_attempts add column output jsonb;
