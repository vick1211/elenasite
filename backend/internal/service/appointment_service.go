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

func calendarLoc() *time.Location {
	loc, err := time.LoadLocation(calendarLocation)
	if err != nil {
		return time.FixedZone("YEKT", 5*3600)
	}
	return loc
}

func normalizeCalendarTime(t time.Time) (time.Time, error) {
	loc := calendarLoc()
	_, offset := t.Zone()
	_, expectedOffset := time.Date(2026, 1, 1, 0, 0, 0, 0, loc).Zone()
	if offset != expectedOffset {
		return time.Time{}, fmt.Errorf("время должно быть передано с часовым поясом %s", calendarLocation)
	}
	return t.In(loc), nil
}

func validateAppointmentWindow(startsAt time.Time, duration time.Duration) error {
	loc := calendarLoc()
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
	if svc.IsDemo {
		return errors.New("демо-услуга недоступна для публичной записи")
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
	loc := calendarLoc()
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
	startsAt, err := normalizeCalendarTime(req.StartsAt)
	if err != nil {
		return nil, err
	}
	duration := time.Duration(svc.DurationMin) * time.Minute
	if duration <= 0 {
		duration = 60 * time.Minute
	}
	endsAt := startsAt.Add(duration)
	if err := s.validateBookable(ctx, startsAt, endsAt); err != nil {
		return nil, err
	}

	appt := &models.Appointment{
		ClientID:      client.ID,
		ServiceID:     req.ServiceID,
		Format:        req.Format,
		StartsAt:      startsAt,
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
		Description:    fmt.Sprintf("%s — приём %s", svc.Title, startsAt.Format("02.01.2006 15:04")),
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

	var setPaymentErr error
	for attempt := 0; attempt < 3; attempt++ {
		setPaymentErr = s.apptRepo.SetPayment(ctx, appt.ID, payResp.PaymentID, amountKopeks, req.PaymentMode)
		if setPaymentErr == nil {
			break
		}
		if attempt < 2 {
			time.Sleep(time.Duration(attempt+1) * 100 * time.Millisecond)
		}
	}
	if setPaymentErr != nil {
		// Do not delete the appointment here: the payment has already been
		// created. The webhook/reconciliation path can recover the payment id
		// from YooKassa metadata.
		return nil, fmt.Errorf("платёж создан, но не удалось сохранить его в записи: %w", setPaymentErr)
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
	if appt.Status == models.StatusCancelled && appt.PaymentStatus == models.PaymentStatusRefunded {
		return nil
	}
	if appt.PaymentID == "" {
		return errors.New("у записи отсутствует идентификатор платежа")
	}
	info, err := s.payment.GetPayment(ctx, appt.PaymentID)
	if err != nil {
		return fmt.Errorf("проверка платежа: %w", err)
	}
	if !info.Paid || info.Status != "succeeded" {
		return nil
	}
	if appt.AmountKopeks > 0 && info.AmountKopeks > 0 && info.AmountKopeks != appt.AmountKopeks {
		return fmt.Errorf("сумма платежа не совпадает с суммой записи")
	}
	if appt.Status != models.StatusPending {
		// A late payment for a cancelled appointment must not resurrect it.
		if appt.Status == models.StatusCancelled {
			if appt.PaymentStatus == models.PaymentStatusRefunded {
				return nil
			}
			refundAmount := info.AmountKopeks
			if refundAmount <= 0 {
				refundAmount = appt.AmountKopeks
			}
			refund, refundErr := s.payment.Refund(ctx, appt.PaymentID, refundAmount)
			if refundErr != nil {
				return fmt.Errorf("платёж получен после отмены записи; возврат не выполнен: %w", refundErr)
			}
			if refund.Status == "succeeded" {
				return s.apptRepo.UpdatePaymentStatus(ctx, apptID, models.PaymentStatusRefunded)
			}
			return nil
		}
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
	needsRefund := refundable && appt.PaymentStatus == models.PaymentStatusPaid && appt.PaymentID != ""

	// Never cancel a paid appointment before a required refund has been
	// successfully initiated. Otherwise a transient YooKassa error leaves the
	// database in the dangerous state cancelled+paid.
	var refundStatus string
	if needsRefund {
		refund, err := s.payment.Refund(ctx, appt.PaymentID, appt.AmountKopeks)
		if err != nil {
			return fmt.Errorf("не удалось оформить возврат: %w", err)
		}
		refundStatus = refund.Status
		if refundStatus != "succeeded" && refundStatus != "pending" {
			return fmt.Errorf("возврат не принят: статус %s", refundStatus)
		}
	}

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
	if needsRefund && refundStatus == "succeeded" {
		_ = s.apptRepo.UpdatePaymentStatus(ctx, appt.ID, models.PaymentStatusRefunded)
	}

	if err := s.mailer.SendCancellationConfirmation(appt.Client.Email, mailer.CancellationData{
		FirstName:       appt.Client.FirstName,
		AppointmentDate: appt.StartsAt,
		Refund:          needsRefund,
		DeadlineHours:   s.cancellationDeadlineHours,
	}); err != nil {
		fmt.Printf("warn: cancellation email for %s: %v\n", appt.ID, err)
	}

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
	newStart, err = normalizeCalendarTime(newStart)
	if err != nil {
		return err
	}
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
	startsAt, err := normalizeCalendarTime(req.StartsAt)
	if err != nil {
		return nil, err
	}
	duration := time.Duration(svc.DurationMin) * time.Minute
	if duration <= 0 {
		duration = 60 * time.Minute
	}
	endsAt := startsAt.Add(duration)
	if err := s.validateBookable(ctx, startsAt, endsAt); err != nil {
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
		StartsAt:      startsAt,
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
