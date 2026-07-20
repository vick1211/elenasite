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
}

type AppointmentService struct {
	apptRepo    *repository.AppointmentRepo
	tokenRepo   *repository.CancellationTokenRepo
	clientRepo  *repository.ClientRepo
	serviceRepo *repository.ServiceRepo
	mailer      MailSender
	payment     PaymentProvider
	baseURL string
	paymentReturnURL string
	cancellationDeadlineHours int
}

func NewAppointmentService(
	apptRepo *repository.AppointmentRepo,
	tokenRepo *repository.CancellationTokenRepo,
	clientRepo *repository.ClientRepo,
	serviceRepo *repository.ServiceRepo,
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

	duration := time.Duration(svc.DurationMin) * time.Minute
	if duration == 0 {
		duration = 60 * time.Minute
	}

	appt := &models.Appointment{
		ClientID:      client.ID,
		ServiceID:     req.ServiceID,
		Format:        req.Format,
		StartsAt:      req.StartsAt,
		EndsAt:        req.StartsAt.Add(duration),
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
		AmountKopeks: amountKopeks,
		Description:  fmt.Sprintf("%s — приём %s", svc.Title, req.StartsAt.Format("02.01.2006 15:04")),
		ReturnURL:    s.paymentReturnURL,
		ClientEmail:  client.Email,
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

	if appt.PaymentStatus == models.PaymentStatusPaid {
		return nil
	}

	if err := s.apptRepo.UpdatePaymentStatus(ctx, apptID, models.PaymentStatusPaid); err != nil {
		return err
	}
	if err := s.apptRepo.UpdateStatus(ctx, apptID, models.StatusConfirmed); err != nil {
		return err
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

	_ = s.tokenRepo.MarkUsed(ctx, ct.ID)
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
	ids, err := s.apptRepo.CancelStalePending(ctx, olderThan)
	if err != nil {
		return 0, err
	}

	for _, id := range ids {
		appt, err := s.apptRepo.GetByIDWithRelations(ctx, id)
		if err != nil {
			fmt.Printf("warn: expire stale pending: load %s: %v\n", id, err)
			continue
		}
		if err := s.mailer.SendPaymentFailedNotice(appt.Client.Email, appt.Client.FirstName); err != nil {
			fmt.Printf("warn: expire stale pending: notify %s: %v\n", id, err)
		}
	}

	return len(ids), nil
}

func (s *AppointmentService) Reschedule(ctx context.Context, apptID uuid.UUID, newStart time.Time) error {
	appt, err := s.apptRepo.GetByIDWithRelations(ctx, apptID)
	if err != nil {
		return err
	}

	duration := appt.EndsAt.Sub(appt.StartsAt)
	newEnd := newStart.Add(duration)

	return s.apptRepo.Reschedule(ctx, apptID, newStart, newEnd)
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
 
	duration := time.Duration(svc.DurationMin) * time.Minute
	if duration == 0 {
		duration = 60 * time.Minute
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
		EndsAt:        req.StartsAt.Add(duration),
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
