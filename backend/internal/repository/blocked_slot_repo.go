package repository

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
)

type BlockedSlot struct {
	ID          uuid.UUID `db:"id" json:"id"`
	BlockedDate time.Time `db:"blocked_date" json:"blocked_date"`
	SlotTime    *string   `db:"slot_time" json:"slot_time"`
	Reason      string    `db:"reason" json:"reason"`
	CreatedAt   time.Time `db:"created_at" json:"created_at"`
}

type BlockedSlotRepo struct {
	db *sqlx.DB
}

func NewBlockedSlotRepo(db *sqlx.DB) *BlockedSlotRepo {
	return &BlockedSlotRepo{db: db}
}

func (r *BlockedSlotRepo) Create(ctx context.Context, date time.Time, slotTime *string, reason string) (*BlockedSlot, error) {
	var bs BlockedSlot
	err := r.db.GetContext(ctx, &bs, `
		insert into blocked_slots (id, blocked_date, slot_time, reason)
		values (uuid_generate_v4(), $1, $2, $3)
		returning id, blocked_date, slot_time, reason, created_at
	`, date, slotTime, reason)
	if err != nil {
		return nil, fmt.Errorf("create blocked slot: %w", err)
	}
	return &bs, nil
}

func (r *BlockedSlotRepo) ListRange(ctx context.Context, from, to time.Time) ([]BlockedSlot, error) {
	var list []BlockedSlot
	err := r.db.SelectContext(ctx, &list, `
		select id, blocked_date, slot_time, reason, created_at
		from blocked_slots
		where blocked_date >= $1::date and blocked_date < $2::date
		order by blocked_date, slot_time nulls first
	`, from, to)
	if err != nil {
		return nil, fmt.Errorf("list blocked slots: %w", err)
	}
	return list, nil
}

func (r *BlockedSlotRepo) IsBlocked(ctx context.Context, startsAt, endsAt time.Time, loc *time.Location) (bool, error) {
	startLocal := startsAt.In(loc)
	endLocal := endsAt.In(loc)
	var rows []struct {
		BlockedDate time.Time `db:"blocked_date"`
		SlotTime    *string   `db:"slot_time"`
	}
	if err := r.db.SelectContext(ctx, &rows, `
		select blocked_date, slot_time
		from blocked_slots
		where blocked_date between $1::date and $2::date
	`, startLocal, endLocal); err != nil {
		return false, fmt.Errorf("check blocked slot: %w", err)
	}
	for _, row := range rows {
		date := row.BlockedDate.In(loc)
		if row.SlotTime == nil {
			return true, nil
		}
		slot, err := time.ParseInLocation("15:04", *row.SlotTime, loc)
		if err != nil {
			return false, fmt.Errorf("invalid stored blocked slot time: %w", err)
		}
		blockedAt := time.Date(date.Year(), date.Month(), date.Day(), slot.Hour(), slot.Minute(), 0, 0, loc)
		if !blockedAt.Before(startLocal) && blockedAt.Before(endLocal) {
			return true, nil
		}
	}
	return false, nil
}

func (r *BlockedSlotRepo) Delete(ctx context.Context, id uuid.UUID) error {
	_, err := r.db.ExecContext(ctx, `delete from blocked_slots where id = $1`, id)
	return err
}
