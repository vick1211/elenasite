package repository

import (
	"context"
	"elena-backend/internal/models"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

var ErrInvalidStatusTransition = fmt.Errorf("недопустимый переход статуса записи")

type AppointmentRepo struct {
	db *sqlx.DB
}

func NewAppointmentRepo(db *sqlx.DB) *AppointmentRepo {
	return &AppointmentRepo{db: db}
}

func (r *AppointmentRepo) Create(ctx context.Context, a *models.Appointment) error {
	a.ID = uuid.New()
	a.CreatedAt = time.Now()
	a.UpdatedAt = time.Now()

	_, err := r.db.NamedExecContext(ctx, `
		insert into appointments
			(id, client_id, service_id, format, starts_at, ends_at, status,
			 notes, payment_mode, payment_status, payment_id, amount_kopeks, created_at, updated_at)
		values
			(:id, :client_id, :service_id, :format, :starts_at, :ends_at, :status,
			 :notes, :payment_mode, :payment_status, :payment_id, :amount_kopeks, :created_at, :updated_at)
	`, a)
	if err != nil {
		return fmt.Errorf("create appointment: %w", err)
	}
	return nil
}

func (r *AppointmentRepo) GetByID(ctx context.Context, id uuid.UUID) (*models.Appointment, error) {
	var a models.Appointment
	err := r.db.GetContext(ctx, &a, `select * from appointments where id = $1`, id)
	if err != nil {
		return nil, fmt.Errorf("appointment not found: %w", err)
	}
	return &a, nil
}

func (r *AppointmentRepo) GetByIDWithRelations(ctx context.Context, id uuid.UUID) (*models.Appointment, error) {
	type row struct {
		models.Appointment
		CFirstName string `db:"c_first_name"`
		CLastName  string `db:"c_last_name"`
		CPatronym  string `db:"c_patronym"`
		CEmail     string `db:"c_email"`
		CPhone     string `db:"c_phone"`
		STitle     string `db:"s_title"`
	}

	var r2 row
	err := r.db.GetContext(ctx, &r2, `
		select
			a.*,
			c.first_name as c_first_name,
			c.last_name  as c_last_name,
			c.patronym   as c_patronym,
			c.email      as c_email,
			c.phone      as c_phone,
			s.title      as s_title
		from appointments a
		join clients  c on c.id = a.client_id
		join services s on s.id = a.service_id
		where a.id = $1
	`, id)
	if err != nil {
		return nil, err
	}

	appt := r2.Appointment
	appt.Client = &models.Client{
		ID:        appt.ClientID,
		FirstName: r2.CFirstName,
		LastName:  r2.CLastName,
		Patronym:  r2.CPatronym,
		Email:     r2.CEmail,
		Phone:     r2.CPhone,
	}
	appt.Service = &models.Service{
		ID:    appt.ServiceID,
		Title: r2.STitle,
	}
	return &appt, nil
}

func (r *AppointmentRepo) List(ctx context.Context, opts ListAppointmentsOpts) ([]models.Appointment, error) {
	type row struct {
		models.Appointment
		CFirstName string `db:"c_first_name"`
		CLastName  string `db:"c_last_name"`
		CPatronym  string `db:"c_patronym"`
		CEmail     string `db:"c_email"`
		CPhone     string `db:"c_phone"`
		STitle     string `db:"s_title"`
	}

	q := `
		select
			a.*,
			c.first_name as c_first_name,
			c.last_name  as c_last_name,
			c.patronym   as c_patronym,
			c.email      as c_email,
			c.phone      as c_phone,
			s.title      as s_title
		from appointments a
		join clients  c on c.id = a.client_id
		join services s on s.id = a.service_id
		where 1=1
	`
	args := []any{}
	i := 1

	if opts.Status != "" {
		q += fmt.Sprintf(" and a.status = $%d", i)
		args = append(args, opts.Status)
		i++
	}
	if !opts.From.IsZero() {
		q += fmt.Sprintf(" and a.starts_at >= $%d", i)
		args = append(args, opts.From)
		i++
	}
	if !opts.To.IsZero() {
		q += fmt.Sprintf(" and a.starts_at <= $%d", i)
		args = append(args, opts.To)
		i++
	}
	if opts.ClientID != uuid.Nil {
		q += fmt.Sprintf(" and a.client_id = $%d", i)
		args = append(args, opts.ClientID)
		i++
	}

	q += " order by a.starts_at asc"

	if opts.Limit > 0 {
		q += fmt.Sprintf(" limit $%d offset $%d", i, i+1)
		args = append(args, opts.Limit, opts.Offset)
	}

	var rows []row
	if err := r.db.SelectContext(ctx, &rows, q, args...); err != nil {
		return nil, err
	}

	list := make([]models.Appointment, len(rows))
	for idx, rr := range rows {
		appt := rr.Appointment
		appt.Client = &models.Client{
			ID:        appt.ClientID,
			FirstName: rr.CFirstName,
			LastName:  rr.CLastName,
			Patronym:  rr.CPatronym,
			Email:     rr.CEmail,
			Phone:     rr.CPhone,
		}
		appt.Service = &models.Service{
			ID:    appt.ServiceID,
			Title: rr.STitle,
		}
		list[idx] = appt
	}
	return list, nil
}

type ListAppointmentsOpts struct {
	Status   models.AppointmentStatus
	From     time.Time
	To       time.Time
	ClientID uuid.UUID
	Limit    int
	Offset   int
}

func (r *AppointmentRepo) ConfirmPendingPayment(ctx context.Context, id uuid.UUID) (bool, error) {
	res, err := r.db.ExecContext(ctx, `
		update appointments
		set payment_status = 'paid', status = 'confirmed', updated_at = now()
		where id = $1
		  and status = 'pending'
		  and payment_status = 'pending'
	`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (r *AppointmentRepo) UpdateStatus(ctx context.Context, id uuid.UUID, status models.AppointmentStatus) error {
	res, err := r.db.ExecContext(ctx, `
		update appointments
		set status = $1, updated_at = now()
		where id = $2
		  and (
			status = $1
			or (status = 'pending' and $1 in ('confirmed', 'cancelled'))
			or (status = 'confirmed' and $1 in ('cancelled', 'completed', 'no_show'))
		  )
	`, status, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrInvalidStatusTransition
	}
	return nil
}

func (r *AppointmentRepo) Reschedule(ctx context.Context, id uuid.UUID, startsAt, endsAt time.Time) error {
	_, err := r.db.ExecContext(ctx, `
		update appointments
		set starts_at = $1, ends_at = $2, updated_at = now()
		where id = $3
	`, startsAt, endsAt, id)
	return err
}

func (r *AppointmentRepo) Delete(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.ExecContext(ctx, `delete from appointments where id = $1`, id)
	return err
}

func (r *AppointmentRepo) SetPayment(ctx context.Context, id uuid.UUID, paymentID string, amountKopeks int64, mode models.PaymentMode) error {
	_, err := r.db.ExecContext(ctx, `
		update appointments
		set payment_id = $1, amount_kopeks = $2, payment_mode = $3, updated_at = now()
		where id = $4
	`, paymentID, amountKopeks, mode, id)
	return err
}

func (r *AppointmentRepo) SetPaymentID(ctx context.Context, id uuid.UUID, paymentID string) error {
	_, err := r.db.ExecContext(ctx, `
		update appointments
		set payment_id = $1, updated_at = now()
		where id = $2 and payment_id = ''
	`, paymentID, id)
	return err
}

func (r *AppointmentRepo) UpdatePaymentStatus(ctx context.Context, id uuid.UUID, status string) error {
	_, err := r.db.ExecContext(ctx,
		`update appointments set payment_status = $1, updated_at = now() where id = $2`, status, id)
	return err
}

func (r *AppointmentRepo) ListStalePending(ctx context.Context, olderThan time.Duration) ([]uuid.UUID, error) {
	var ids []uuid.UUID
	err := r.db.SelectContext(ctx, &ids, `
		select id from appointments
		where status = 'pending'
		  and payment_status = 'pending'
		  and created_at < now() - $1::interval
		order by created_at
	`, fmt.Sprintf("%d minutes", int(olderThan.Minutes())))
	if err != nil {
		return nil, fmt.Errorf("list stale pending: %w", err)
	}
	return ids, nil
}

func (r *AppointmentRepo) CancelPending(ctx context.Context, id uuid.UUID) (bool, error) {
	res, err := r.db.ExecContext(ctx, `
		update appointments
		set status = 'cancelled', payment_status = 'failed', updated_at = now()
		where id = $1 and status = 'pending' and payment_status = 'pending'
	`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (r *AppointmentRepo) GetByPaymentID(ctx context.Context, paymentID string) (*models.Appointment, error) {
	var a models.Appointment
	err := r.db.GetContext(ctx, &a, `select * from appointments where payment_id = $1`, paymentID)
	if err != nil {
		return nil, fmt.Errorf("appointment not found by payment_id: %w", err)
	}
	return &a, nil
}

type BusyInterval struct {
	StartsAt time.Time `db:"starts_at"`
	EndsAt   time.Time `db:"ends_at"`
}

func (r *AppointmentRepo) ListBusyTimes(ctx context.Context, from, to time.Time) ([]BusyInterval, error) {
	var intervals []BusyInterval
	err := r.db.SelectContext(ctx, &intervals, `
		select starts_at, ends_at from appointments
		where starts_at < $2 and ends_at > $1
		and status != 'cancelled'
	`, from, to)
	if err != nil {
		return nil, fmt.Errorf("list busy times: %w", err)
	}
	return intervals, nil
}

type CancellationTokenRepo struct {
	db *sqlx.DB
}

func NewCancellationTokenRepo(db *sqlx.DB) *CancellationTokenRepo {
	return &CancellationTokenRepo{db: db}
}

func (r *CancellationTokenRepo) Create(ctx context.Context, t *models.CancellationToken) error {
	t.ID = uuid.New()
	t.CreatedAt = time.Now()

	_, err := r.db.NamedExecContext(ctx, `
		insert into cancellation_tokens (id, appointment_id, token, expires_at, created_at)
		values (:id, :appointment_id, :token, :expires_at, :created_at)
	`, t)
	return err
}

func (r *CancellationTokenRepo) GetByToken(ctx context.Context, token string) (*models.CancellationToken, error) {
	var t models.CancellationToken
	err := r.db.GetContext(ctx, &t, `
		select * from cancellation_tokens
		where token = $1 and used_at is null and expires_at > now()
	`, token)
	if err != nil {
		return nil, fmt.Errorf("token not found or expired: %w", err)
	}
	return &t, nil
}

func (r *CancellationTokenRepo) MarkUsed(ctx context.Context, id uuid.UUID) (bool, error) {
	res, err := r.db.ExecContext(ctx, `
		update cancellation_tokens
		set used_at = now()
		where id = $1
		  and used_at is null
		  and expires_at > now()
	`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (r *CancellationTokenRepo) InvalidateActiveForAppointment(ctx context.Context, appointmentID uuid.UUID) error {
	_, err := r.db.ExecContext(ctx, `
		update cancellation_tokens
		set used_at = now()
		where appointment_id = $1
		  and used_at is null
		  and expires_at > now()
	`, appointmentID)
	return err
}
