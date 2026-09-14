drop index if exists appointments_pending_idx;
alter table blocked_slots drop constraint if exists blocked_slot_time_format_check;
