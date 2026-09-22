package repository

import (
	"context"
	"elena-backend/internal/models"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jmoiron/sqlx"
)

// ловит ошибку, если она вызвана нарушением ограничения unique в postgres
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

var ErrPhoneTakenByAnotherEmail = errors.New("этот телефон уже зарегистрирован с другой почтой")

type ClientRepo struct {
	db *sqlx.DB
}

func NewClientRepo(db *sqlx.DB) *ClientRepo {
	return &ClientRepo{db: db}
}

func (r *ClientRepo) Create(ctx context.Context, c *models.Client) error {
	c.ID = uuid.New()
	c.CreatedAt = time.Now()
	c.UpdatedAt = time.Now()

	_, err := r.db.NamedExecContext(ctx, `
		insert into clients (id, first_name, last_name, patronym, phone, email, created_at, updated_at)
		values (:id, :first_name, :last_name, :patronym, :phone, :email, :created_at, :updated_at)
	`, c)
	return err
}

func (r *ClientRepo) GetByID(ctx context.Context, id uuid.UUID) (*models.Client, error) {
	var c models.Client
	err := r.db.GetContext(ctx, &c, `select * from clients where id = $1`, id)
	if err != nil {
		return nil, fmt.Errorf("client not found: %w", err)
	}
	return &c, nil
}

func (r *ClientRepo) GetByEmail(ctx context.Context, email string) (*models.Client, error) {
	var c models.Client
	err := r.db.GetContext(ctx, &c, `select * from clients where email = $1`, email)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (r *ClientRepo) GetByPhone(ctx context.Context, phone string) (*models.Client, error) {
	var c models.Client
	err := r.db.GetContext(ctx, &c, `select * from clients where phone = $1`, phone)
	if err != nil {
		return nil, err
	}
	return &c, nil
}

func (r *ClientRepo) FindOrCreate(ctx context.Context, c *models.Client) (*models.Client, error) {
	existing, err := r.GetByEmail(ctx, c.Email)
	if err == nil {
		if existing.Phone != c.Phone {
			if byPhone, phoneErr := r.GetByPhone(ctx, c.Phone); phoneErr == nil && byPhone.Email != c.Email {
				return nil, ErrPhoneTakenByAnotherEmail
			}
		}
		existing.FirstName = c.FirstName
		existing.LastName = c.LastName
		existing.Patronym = c.Patronym
		existing.Phone = c.Phone
		if updErr := r.updateContactInfo(ctx, existing); updErr != nil {
			if isUniqueViolation(updErr) {
				return nil, ErrPhoneTakenByAnotherEmail
			}
			return nil, updErr
		}
		return existing, nil
	}

	if byPhone, phoneErr := r.GetByPhone(ctx, c.Phone); phoneErr == nil && byPhone.Email != c.Email {
		return nil, ErrPhoneTakenByAnotherEmail
	}

	if err := r.Create(ctx, c); err != nil {
		if isUniqueViolation(err) {
			if again, againErr := r.GetByEmail(ctx, c.Email); againErr == nil {
				return again, nil
			}
			return nil, ErrPhoneTakenByAnotherEmail
		}
		return nil, fmt.Errorf("create client: %w", err)
	}
	return c, nil
}

func (r *ClientRepo) updateContactInfo(ctx context.Context, c *models.Client) error {
	_, err := r.db.ExecContext(ctx, `
		update clients
		set first_name = $1, last_name = $2, patronym = $3, phone = $4, updated_at = now()
		where id = $5
	`, c.FirstName, c.LastName, c.Patronym, c.Phone, c.ID)
	return err
}

func (r *ClientRepo) List(ctx context.Context, limit, offset int) ([]models.Client, error) {
	var clients []models.Client
	err := r.db.SelectContext(ctx, &clients,
		`select * from clients order by created_at desc limit $1 offset $2`, limit, offset)
	return clients, err
}

func (r *ClientRepo) Delete(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.ExecContext(ctx, `delete from clients where id = $1`, id)
	return err
}
