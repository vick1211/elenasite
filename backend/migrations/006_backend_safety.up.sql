create unique index if not exists appointments_payment_id_unique_idx
    on appointments (payment_id)
    where payment_id <> '';

create index if not exists services_public_active_idx
    on services (is_active, is_demo, title);
