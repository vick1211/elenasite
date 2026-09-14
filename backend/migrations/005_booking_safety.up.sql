alter table blocked_slots
    add constraint blocked_slot_time_format_check
    check (slot_time is null or slot_time ~ '^(0[0-9]|1[0-9]|2[0-3]):[0-5][0-9]$');

create index if not exists appointments_pending_idx
    on appointments (created_at)
    where status = 'pending' and payment_status = 'pending';
