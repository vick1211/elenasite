package service

import (
	"context"
	"elena-backend/internal/models"
	"fmt"
)

type PaymentProvider interface {
	CreatePayment(ctx context.Context, req CreatePaymentReq) (*CreatePaymentResp, error)
	GetPayment(ctx context.Context, paymentID string) (*PaymentInfo, error)
	Refund(ctx context.Context, paymentID string, amountKopeks int64) (*RefundInfo, error)
}

type CreatePaymentReq struct {
	AmountKopeks   int64
	Description    string
	ReturnURL      string
	Metadata       map[string]string
	ClientEmail    string
	PaymentMode    models.PaymentMode
	IdempotencyKey string
}

type CreatePaymentResp struct {
	PaymentID       string
	ConfirmationURL string
	Status          string
}

type PaymentInfo struct {
	PaymentID    string
	Status       string
	Paid         bool
	AmountKopeks int64
	Metadata     map[string]string
}

type RefundInfo struct {
	RefundID string
	Status   string
}

type StubPaymentProvider struct{}

func (s *StubPaymentProvider) CreatePayment(_ context.Context, req CreatePaymentReq) (*CreatePaymentResp, error) {
	fmt.Printf("[STUB PAYMENT] CreatePayment: amount=%d desc=%q\n", req.AmountKopeks, req.Description)
	return &CreatePaymentResp{
		PaymentID:       "stub-payment-id",
		ConfirmationURL: req.ReturnURL,
		Status:          "pending",
	}, nil
}

func (s *StubPaymentProvider) GetPayment(_ context.Context, paymentID string) (*PaymentInfo, error) {
	return &PaymentInfo{PaymentID: paymentID, Status: "succeeded", Paid: true}, nil
}

func (s *StubPaymentProvider) Refund(_ context.Context, paymentID string, amountKopeks int64) (*RefundInfo, error) {
	fmt.Printf("[STUB PAYMENT] Refund: id=%s amount=%d\n", paymentID, amountKopeks)
	return &RefundInfo{RefundID: "stub-refund-id", Status: "succeeded"}, nil
}
