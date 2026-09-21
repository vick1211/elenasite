package service

import (
	"context"
	"crypto/rand"
	"elena-backend/internal/mailer"
	"elena-backend/internal/models"
	"elena-backend/internal/repository"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

type MailSender interface {
	SendBookingConfirmation(to string, d mailer.BookingData) error
	SendCancellationConfirmation(to string, d mailer.CancellationData) error
	SendPaymentFailedNotice(to, firstName string) error
	SendRescheduleNotice(to string, d mailer.RescheduleData) error
}

type AppointmentService struct {
	apptRepo                  *repository.AppointmentRepo
	tokenRepo                 *repository.CancellationTokenRepo
	clientRepo                *repository.ClientRepo
	serviceRepo               *repository.ServiceRepo
	blockedRepo               *repository.BlockedSlotRepo
	mailer                    MailSender
	payment                   PaymentProvider
	baseURL                   string
	paymentReturnURL          string
	cancellationDeadlineHours int
}

func NewAppointmentService(
	apptRepo *repository.AppointmentRepo,
	tokenRepo *repository.CancellationTokenRepo,
	clientRepo *repository.ClientRepo,
	serviceRepo *repository.ServiceRepo,
	blockedRepo *repository.BlockedSlotRepo,
	mailer MailSender,
	payment PaymentProvider,
	baseURL string,
	paymentReturnURL string,
	cancellationDeadlineHours int,
) *AppointmentService {
	return &AppointmentService{
		apptRepo:                  apptRepo,
		tokenRepo:                 tokenRepo,
		clientRepo:                clientRepo,
		serviceRepo:               serviceRepo,
		blockedRepo:               blockedRepo,
		mailer:                    mailer,
		payment:                   payment,
		baseURL:                   baseURL,
		paymentReturnURL:          paymentReturnURL,
		cancellationDeadlineHours: cancellationDeadlineHours,
	}
}

type BookRequest struct {
	Client      ClientInfo
	ServiceID   uuid.UUID
	Format      models.AppointmentFormat
	StartsAt    time.Time
	PaymentMode models.PaymentMode
}

type ClientInfo struct {
	FirstName string
	LastName  string
	Patronym  string
	Phone     string
	Email     string
}

type BookResult struct {
	Appointment *models.Appointment
	PaymentURL  string
}

const (
	calendarStartHour = 10
	calendarEndHour   = 20
	calendarLocation  = "Asia/Yekaterinburg"
)

func validateAppointmentWindow(startsAt time.Time, duration time.Duration) error {
	loc, err := time.LoadLocation(calendarLocation)
	if err != nil {
		loc = time.FixedZone("YEKT", 5*3600)
	}
	local := startsAt.In(loc)
	now := time.Now().In(loc)
	if !local.After(now) {
		return errors.New("нельзя записаться на прошедшее время")
	}
	if local.Second() != 0 || local.Nanosecond() != 0 || local.Minute() != 0 {
		return errors.New("время записи должно быть ровно на целый час")
	}
	if local.Hour() < calendarStartHour || local.Hour() >= calendarEndHour {
		return errors.New("время записи должно быть с 10:00 до 19:00")
	}
	end := local.Add(duration)
	if end.Day() != local.Day() || end.Hour() > calendarEndHour || (end.Hour() == calendarEndHour && end.Minute() > 0) {
		return errors.New("сеанс выходит за пределы рабочего времени")
	}
	return nil
}

func validateServiceChoice(svc *models.Service, format models.AppointmentFormat) error {
	if !svc.IsActive {
		return errors.New("услуга недоступна для записи")
	}
	switch svc.Format {
	case models.FormatOnline:
		if format != models.AppointmentOnline {
			return errors.New("для этой услуги доступен только онлайн-формат")
		}
	case models.FormatOffline:
		if format != models.AppointmentOffline {
			return errors.New("для этой услуги доступен только очный формат")
		}
	case models.FormatBoth:
	default:
		return errors.New("у услуги указан некорректный формат")
	}
	return nil
}

func (s *AppointmentService) validateBookable(ctx context.Context, startsAt, endsAt time.Time) error {
	if err := validateAppointmentWindow(startsAt, endsAt.Sub(startsAt)); err != nil {
		return err
	}
	loc, err := time.LoadLocation(calendarLocation)
	if err != nil {
		loc = time.FixedZone("YEKT", 5*3600)
	}
	blocked, err := s.blockedRepo.IsBlocked(ctx, startsAt, endsAt, loc)
	if err != nil {
		return err
	}
	if blocked {
		return errors.New("выбранное время заблокировано")
	}
	return nil
}

func (s *AppointmentService) Book(ctx context.Context, req BookRequest) (*BookResult, error) {
	client, err := s.clientRepo.FindOrCreate(ctx, &models.Client{
		FirstName: req.Client.FirstName,
		LastName:  req.Client.LastName,
		Patronym:  req.Client.Patronym,
		Phone:     req.Client.Phone,
		Email:     req.Client.Email,
	})
	if err != nil {
		return nil, err
	}

	svc, err := s.serviceRepo.GetByID(ctx, req.ServiceID)
	if err != nil {
		return nil, fmt.Errorf("service: %w", err)
	}

	if req.PaymentMode == "" {
		req.PaymentMode = models.PaymentModeFull
	}
	amountKopeks := svc.PriceKopeks
	if req.PaymentMode == models.PaymentModePrepay50 {
		amountKopeks = svc.PriceKopeks / 2
	}

	if err := validateServiceChoice(svc, req.Format); err != nil {
		return nil, err
	}
	duration := time.Duration(svc.DurationMin) * time.Minute
	if duration <= 0 {
		duration = 60 * time.Minute
	}
	endsAt := req.StartsAt.Add(duration)
	if err := s.validateBookable(ctx, req.StartsAt, endsAt); err != nil {
		return nil, err
	}

	appt := &models.Appointment{
		ClientID:      client.ID,
		ServiceID:     req.ServiceID,
		Format:        req.Format,
		StartsAt:      req.StartsAt,
		EndsAt:        endsAt,
		Status:        models.StatusPending,
		PaymentMode:   req.PaymentMode,
		PaymentStatus: models.PaymentStatusPending,
		AmountKopeks:  amountKopeks,
	}

	if err := s.apptRepo.Create(ctx, appt); err != nil {
		return nil, fmt.Errorf("время уже занято или произошла ошибка: %w", err)
	}

	if amountKopeks <= 0 {
		appt.Status = models.StatusConfirmed
		appt.PaymentStatus = models.PaymentStatusPaid
		_ = s.apptRepo.UpdateStatus(ctx, appt.ID, models.StatusConfirmed)
		_ = s.apptRepo.UpdatePaymentStatus(ctx, appt.ID, models.PaymentStatusPaid)
		_ = s.issueTokenAndNotify(ctx, appt, client)
		return &BookResult{Appointment: appt}, nil
	}

	payResp, err := s.payment.CreatePayment(ctx, CreatePaymentReq{
		AmountKopeks:   amountKopeks,
		Description:    fmt.Sprintf("%s — приём %s", svc.Title, req.StartsAt.Format("02.01.2006 15:04")),
		ReturnURL:      s.paymentReturnURL,
		ClientEmail:    client.Email,
		PaymentMode:    req.PaymentMode,
		IdempotencyKey: "appointment-" + appt.ID.String(),
		Metadata: map[string]string{
			"appointment_id": appt.ID.String(),
		},
	})
	if err != nil {
		_ = s.apptRepo.Delete(ctx, appt.ID)
		return nil, fmt.Errorf("не удалось создать платёж: %w", err)
	}

	if err := s.apptRepo.SetPayment(ctx, appt.ID, payResp.PaymentID, amountKopeks, req.PaymentMode); err != nil {
		fmt.Printf("warn: save payment id: %v\n", err)
	}
	appt.PaymentID = payResp.PaymentID

	return &BookResult{Appointment: appt, PaymentURL: payResp.ConfirmationURL}, nil
}

func (s *AppointmentService) ConfirmPayment(ctx context.Context, apptID uuid.UUID) error {
	appt, err := s.apptRepo.GetByIDWithRelations(ctx, apptID)
	if err != nil {
		return err
	}
	if appt.PaymentStatus == models.PaymentStatusPaid && appt.Status == models.StatusConfirmed {
		return nil
	}
	if appt.Status != models.StatusPending {
		return nil
	}
	changed, err := s.apptRepo.ConfirmPendingPayment(ctx, apptID)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	appt.Status = models.StatusConfirmed
	appt.PaymentStatus = models.PaymentStatusPaid
	return s.issueTokenAndNotify(ctx, appt, appt.Client)
}

func (s *AppointmentService) NotifyRefunded(ctx context.Context, apptID uuid.UUID) error {
	appt, err := s.apptRepo.GetByIDWithRelations(ctx, apptID)
	if err != nil {
		return err
	}
	return s.mailer.SendCancellationConfirmation(appt.Client.Email, mailer.CancellationData{
		FirstName:       appt.Client.FirstName,
		AppointmentDate: appt.StartsAt,
		Refund:          true,
		DeadlineHours:   s.cancellationDeadlineHours,
		StaffInitiated:  true,
	})
}

func (s *AppointmentService) NotifyPaymentFailed(ctx context.Context, apptID uuid.UUID) error {
	appt, err := s.apptRepo.GetByIDWithRelations(ctx, apptID)
	if err != nil {
		return err
	}
	return s.mailer.SendPaymentFailedNotice(appt.Client.Email, appt.Client.FirstName)
}

type CancellationPreview struct {
	Appointment *models.Appointment
	Refundable  bool
	ClientName  string
}

func (s *AppointmentService) PreviewCancellation(ctx context.Context, token string) (*CancellationPreview, error) {
	ct, err := s.tokenRepo.GetByToken(ctx, token)
	if err != nil {
		return nil, errors.New("ссылка недействительна или уже использована")
	}

	appt, err := s.apptRepo.GetByIDWithRelations(ctx, ct.AppointmentID)
	if err != nil {
		return nil, err
	}
	if appt.Status == models.StatusCancelled {
		return nil, errors.New("запись уже отменена")
	}

	refundable := s.isRefundable(appt.StartsAt)
	clientName := appt.Client.FirstName

	return &CancellationPreview{
		Appointment: appt,
		Refundable:  refundable,
		ClientName:  clientName,
	}, nil
}

func (s *AppointmentService) ConfirmCancellation(ctx context.Context, token string) error {
	ct, err := s.tokenRepo.GetByToken(ctx, token)
	if err != nil {
		return errors.New("ссылка недействительна или уже использована")
	}

	appt, err := s.apptRepo.GetByIDWithRelations(ctx, ct.AppointmentID)
	if err != nil {
		return err
	}
	if appt.Status == models.StatusCancelled {
		return errors.New("запись уже отменена")
	}

	refundable := s.isRefundable(appt.StartsAt)

	used, err := s.tokenRepo.MarkUsed(ctx, ct.ID)
	if err != nil {
		return err
	}
	if !used {
		return errors.New("ссылка недействительна или уже использована")
	}
	if err := s.apptRepo.UpdateStatus(ctx, appt.ID, models.StatusCancelled); err != nil {
		return err
	}

	if refundable && appt.PaymentStatus == models.PaymentStatusPaid && appt.PaymentID != "" {
		if _, err := s.payment.Refund(ctx, appt.PaymentID, appt.AmountKopeks); err != nil {
			fmt.Printf("warn: refund failed for appointment %s: %v\n", appt.ID, err)
		} else {
			_ = s.apptRepo.UpdatePaymentStatus(ctx, appt.ID, models.PaymentStatusRefunded)
		}
	}

	_ = s.mailer.SendCancellationConfirmation(appt.Client.Email, mailer.CancellationData{
		FirstName:       appt.Client.FirstName,
		AppointmentDate: appt.StartsAt,
		Refund:          refundable,
		DeadlineHours:   s.cancellationDeadlineHours,
	})

	return nil
}

func (s *AppointmentService) ExpireStalePending(ctx context.Context, olderThan time.Duration) (int, error) {
	ids, err := s.apptRepo.ListStalePending(ctx, olderThan)
	if err != nil {
		return 0, err
	}
	cancelled := 0
	for _, id := range ids {
		appt, err := s.apptRepo.GetByIDWithRelations(ctx, id)
		if err != nil {
			fmt.Printf("warn: expire stale pending: load %s: %v\n", id, err)
			continue
		}

		if appt.PaymentID != "" {
			info, payErr := s.payment.GetPayment(ctx, appt.PaymentID)
			if payErr == nil && info.Paid && info.Status == "succeeded" {
				if err := s.ConfirmPayment(ctx, id); err != nil {
					fmt.Printf("warn: confirm stale paid appointment %s: %v\n", id, err)
				}
				continue
			}
		}
		changed, err := s.apptRepo.CancelPending(ctx, id)
		if err != nil {
			fmt.Printf("warn: expire stale pending: cancel %s: %v\n", id, err)
			continue
		}
		if !changed {
			continue
		}
		cancelled++
		if err := s.mailer.SendPaymentFailedNotice(appt.Client.Email, appt.Client.FirstName); err != nil {
			fmt.Printf("warn: expire stale pending: notify %s: %v\n", id, err)
		}
	}
	return cancelled, nil
}

func (s *AppointmentService) Reschedule(ctx context.Context, apptID uuid.UUID, newStart time.Time) error {
	appt, err := s.apptRepo.GetByIDWithRelations(ctx, apptID)
	if err != nil {
		return err
	}

	if appt.Status == models.StatusCancelled || appt.Status == models.StatusCompleted {
		return errors.New("эту запись нельзя перенести")
	}
	oldStart := appt.StartsAt
	duration := appt.EndsAt.Sub(appt.StartsAt)
	newEnd := newStart.Add(duration)
	if err := s.validateBookable(ctx, newStart, newEnd); err != nil {
		return err
	}

	if err := s.apptRepo.Reschedule(ctx, apptID, newStart, newEnd); err != nil {
		return fmt.Errorf("время уже занято или произошла ошибка: %w", err)
	}

	// Старая ссылка отмены привязана ко времени прежнего приёма и может
	// стать недействительной раньше новой даты — выпускаем новую и
	// уведомляем клиента о переносе.
	if err := s.reissueTokenAndNotifyReschedule(ctx, appt, oldStart, newStart); err != nil {
		fmt.Printf("warn: notify reschedule for appointment %s: %v\n", apptID, err)
	}

	return nil
}

func (s *AppointmentService) reissueTokenAndNotifyReschedule(ctx context.Context, appt *models.Appointment, oldStart, newStart time.Time) error {
	if err := s.tokenRepo.InvalidateActiveForAppointment(ctx, appt.ID); err != nil {
		fmt.Printf("warn: invalidate old cancellation tokens for %s: %v\n", appt.ID, err)
	}

	rawToken, err := randomURLSafeToken(32)
	if err != nil {
		return err
	}

	ct := &models.CancellationToken{
		AppointmentID: appt.ID,
		Token:         rawToken,
		ExpiresAt:     newStart.Add(1 * time.Hour),
	}
	if err := s.tokenRepo.Create(ctx, ct); err != nil {
		return err
	}

	cancelURL := fmt.Sprintf("%s/?cancel_token=%s", s.baseURL, rawToken)

	return s.mailer.SendRescheduleNotice(appt.Client.Email, mailer.RescheduleData{
		FirstName: appt.Client.FirstName,
		OldDate:   oldStart,
		NewDate:   newStart,
		CancelURL: cancelURL,
	})
}

func (s *AppointmentService) isRefundable(startsAt time.Time) bool {
	deadline := time.Duration(s.cancellationDeadlineHours) * time.Hour
	return time.Until(startsAt) > deadline
}

func (s *AppointmentService) issueTokenAndNotify(ctx context.Context, appt *models.Appointment, client *models.Client) error {
	rawToken, err := randomURLSafeToken(32)
	if err != nil {
		return err
	}

	ct := &models.CancellationToken{
		AppointmentID: appt.ID,
		Token:         rawToken,
		ExpiresAt:     appt.StartsAt.Add(1 * time.Hour),
	}
	if err := s.tokenRepo.Create(ctx, ct); err != nil {
		return err
	}

	cancelURL := fmt.Sprintf("%s/?cancel_token=%s", s.baseURL, rawToken)

	return s.mailer.SendBookingConfirmation(client.Email, mailer.BookingData{
		FirstName:       client.FirstName,
		AppointmentDate: appt.StartsAt,
		CancelURL:       cancelURL,
	})
}

type ManualBookRequest struct {
	Client        ClientInfo
	ServiceID     uuid.UUID
	Format        models.AppointmentFormat
	StartsAt      time.Time
	PaymentStatus string
	Notes         string
}

func (s *AppointmentService) BookManual(ctx context.Context, req ManualBookRequest) (*models.Appointment, error) {
	client, err := s.clientRepo.FindOrCreate(ctx, &models.Client{
		FirstName: req.Client.FirstName,
		LastName:  req.Client.LastName,
		Patronym:  req.Client.Patronym,
		Phone:     req.Client.Phone,
		Email:     req.Client.Email,
	})
	if err != nil {
		return nil, err
	}

	svc, err := s.serviceRepo.GetByID(ctx, req.ServiceID)
	if err != nil {
		return nil, fmt.Errorf("service: %w", err)
	}

	if err := validateServiceChoice(svc, req.Format); err != nil {
		return nil, err
	}
	duration := time.Duration(svc.DurationMin) * time.Minute
	if duration <= 0 {
		duration = 60 * time.Minute
	}
	endsAt := req.StartsAt.Add(duration)
	if err := s.validateBookable(ctx, req.StartsAt, endsAt); err != nil {
		return nil, err
	}

	paymentStatus := req.PaymentStatus
	if paymentStatus == "" {
		paymentStatus = models.PaymentStatusPaid
	}

	appt := &models.Appointment{
		ClientID:      client.ID,
		ServiceID:     req.ServiceID,
		Format:        req.Format,
		StartsAt:      req.StartsAt,
		EndsAt:        endsAt,
		Status:        models.StatusConfirmed,
		PaymentMode:   models.PaymentModeFull,
		PaymentStatus: paymentStatus,
		AmountKopeks:  svc.PriceKopeks,
		Notes:         req.Notes,
	}

	if err := s.apptRepo.Create(ctx, appt); err != nil {
		return nil, fmt.Errorf("время уже занято или произошла ошибка: %w", err)
	}

	appt.Client = client
	appt.Service = svc

	if err := s.issueTokenAndNotify(ctx, appt, client); err != nil {
		fmt.Printf("warn: notify manual booking: %v\n", err)
	}

	return appt, nil
}

func randomURLSafeToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
