package repository

import (
	"context"
	"elena-backend/internal/models"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

type ServiceRepo struct {
	db *sqlx.DB
}

func NewServiceRepo(db *sqlx.DB) *ServiceRepo {
	return &ServiceRepo{db: db}
}

func (r *ServiceRepo) Create(ctx context.Context, s *models.Service) error {
	s.ID = uuid.New()
	s.CreatedAt = time.Now()
	s.UpdatedAt = time.Now()

	_, err := r.db.NamedExecContext(ctx, `
		insert into services (id, title, description, format, price_kopeks, duration_min, is_demo, is_active, created_at, updated_at)
		values (:id, :title, :description, :format, :price_kopeks, :duration_min, :is_demo, :is_active, :created_at, :updated_at)
	`, s)
	return err
}

func (r *ServiceRepo) ListActive(ctx context.Context) ([]models.Service, error) {
	var list []models.Service
	err := r.db.SelectContext(ctx, &list,
		`select * from services where is_active = true and is_demo = false order by title`)
	return list, err
}

func (r *ServiceRepo) ListAll(ctx context.Context) ([]models.Service, error) {
	var list []models.Service
	err := r.db.SelectContext(ctx, &list, `select * from services order by title`)
	return list, err
}

func (r *ServiceRepo) GetByID(ctx context.Context, id uuid.UUID) (*models.Service, error) {
	var s models.Service
	if err := r.db.GetContext(ctx, &s, `select * from services where id = $1`, id); err != nil {
		return nil, err
	}
	return &s, nil
}

func (r *ServiceRepo) Update(ctx context.Context, s *models.Service) error {
	_, err := r.db.NamedExecContext(ctx, `
		update services set
			title = :title,
			description = :description,
			format = :format,
			price_kopeks = :price_kopeks,
			duration_min = :duration_min,
			is_demo = :is_demo,
			is_active = :is_active
		where id = :id
	`, s)
	return err
}

func (r *ServiceRepo) Delete(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.ExecContext(ctx, `delete from services where id = $1`, id)
	return err
}

type NewsRepo struct {
	db *sqlx.DB
}

func NewNewsRepo(db *sqlx.DB) *NewsRepo {
	return &NewsRepo{db: db}
}

func (r *NewsRepo) Create(ctx context.Context, n *models.News) error {
	n.ID = uuid.New()
	n.CreatedAt = time.Now()
	n.UpdatedAt = time.Now()

	_, err := r.db.NamedExecContext(ctx, `
		insert into news (id, title, body, published_at, created_at, updated_at)
		values (:id, :title, :body, :published_at, :created_at, :updated_at)
	`, n)
	return err
}

func (r *NewsRepo) ListPublished(ctx context.Context, limit, offset int) ([]models.News, error) {
	var list []models.News
	err := r.db.SelectContext(ctx, &list, `
		select * from news
		where published_at is not null and published_at <= now()
		order by published_at desc
		limit $1 offset $2
	`, limit, offset)
	return list, err
}

func (r *NewsRepo) ListAll(ctx context.Context) ([]models.News, error) {
	var list []models.News
	err := r.db.SelectContext(ctx, &list, `select * from news order by created_at desc`)
	return list, err
}

func (r *NewsRepo) Update(ctx context.Context, n *models.News) error {
	_, err := r.db.NamedExecContext(ctx, `
		update news set title = :title, body = :body, published_at = :published_at
		where id = :id
	`, n)
	return err
}

func (r *NewsRepo) Delete(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.ExecContext(ctx, `delete from news where id = $1`, id)
	return err
}

type ReviewRepo struct {
	db *sqlx.DB
}

func NewReviewRepo(db *sqlx.DB) *ReviewRepo {
	return &ReviewRepo{db: db}
}

func (r *ReviewRepo) Create(ctx context.Context, rev *models.Review) error {
	rev.ID = uuid.New()
	rev.CreatedAt = time.Now()
	rev.UpdatedAt = time.Now()

	_, err := r.db.NamedExecContext(ctx, `
		insert into reviews (id, author_name, body, is_visible, created_at, updated_at)
		values (:id, :author_name, :body, :is_visible, :created_at, :updated_at)
	`, rev)
	return err
}

func (r *ReviewRepo) ListVisible(ctx context.Context) ([]models.Review, error) {
	var list []models.Review
	err := r.db.SelectContext(ctx, &list,
		`select * from reviews where is_visible = true order by created_at desc`)
	return list, err
}

func (r *ReviewRepo) ListAll(ctx context.Context) ([]models.Review, error) {
	var list []models.Review
	err := r.db.SelectContext(ctx, &list, `select * from reviews order by created_at desc`)
	return list, err
}

func (r *ReviewRepo) Update(ctx context.Context, rev *models.Review) error {
	_, err := r.db.NamedExecContext(ctx, `
		update reviews set author_name = :author_name, body = :body, is_visible = :is_visible
		where id = :id
	`, rev)
	return err
}

func (r *ReviewRepo) Delete(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.ExecContext(ctx, `delete from reviews where id = $1`, id)
	return err
}
