package service

import (
	"bytes"
	"context"
	"elena-backend/internal/config"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const yookassaBaseURL = "https://api.yookassa.ru/v3"

type YooKassaProvider struct {
	shopID     string
	secretKey  string
	httpClient *http.Client
}

func NewYooKassaProvider(cfg config.YooKassaConfig) *YooKassaProvider {
	return &YooKassaProvider{
		shopID:    cfg.ShopID,
		secretKey: cfg.SecretKey,
		httpClient: &http.Client{
			Timeout: 15 * time.Second,
		},
	}
}

func (p *YooKassaProvider) doRequest(ctx context.Context, method, path string, body any, idempotencyKey string, out any) error {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("yookassa: marshal request: %w", err)
		}
		reader = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, yookassaBaseURL+path, reader)
	if err != nil {
		return fmt.Errorf("yookassa: build request: %w", err)
	}
	req.SetBasicAuth(p.shopID, p.secretKey)
	req.Header.Set("Content-Type", "application/json")
	if idempotencyKey != "" {
		req.Header.Set("Idempotence-Key", idempotencyKey)
	}

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("yookassa: request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("yookassa: read response: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("yookassa: unexpected status %d: %s", resp.StatusCode, string(respBody))
	}

	if out != nil {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("yookassa: decode response: %w", err)
		}
	}
	return nil
}

type yookassaAmount struct {
	Value    string `json:"value"`
	Currency string `json:"currency"`
}

type yookassaConfirmation struct {
	Type      string `json:"type"`
	ReturnURL string `json:"return_url,omitempty"`
	URL       string `json:"confirmation_url,omitempty"`
}

type yookassaReceiptItem struct {
	Description    string         `json:"description"`
	Quantity       string         `json:"quantity"`
	Amount         yookassaAmount `json:"amount"`
	VatCode        int            `json:"vat_code"`
	PaymentMode    string         `json:"payment_mode"`
	PaymentSubject string         `json:"payment_subject"`
}

type yookassaReceipt struct {
	Customer struct {
		Email string `json:"email,omitempty"`
	} `json:"customer"`
	Items []yookassaReceiptItem `json:"items"`
}

type yookassaCreatePaymentReq struct {
	Amount       yookassaAmount       `json:"amount"`
	Description  string               `json:"description,omitempty"`
	Confirmation yookassaConfirmation `json:"confirmation"`
	Capture      bool                 `json:"capture"`
	Metadata     map[string]string    `json:"metadata,omitempty"`
	Receipt      *yookassaReceipt     `json:"receipt,omitempty"`
}

type yookassaPaymentResp struct {
	ID           string               `json:"id"`
	Status       string               `json:"status"`
	Paid         bool                 `json:"paid"`
	Amount       yookassaAmount       `json:"amount"`
	Confirmation yookassaConfirmation `json:"confirmation"`
	Metadata     map[string]string    `json:"metadata"`
}

func kopeksToRubles(amountKopeks int64) string {
	return fmt.Sprintf("%d.%02d", amountKopeks/100, amountKopeks%100)
}

func rublesToKopeks(value string) int64 {
	var rub, kop int64
	fmt.Sscanf(value, "%d.%d", &rub, &kop)
	return rub*100 + kop
}

func (p *YooKassaProvider) CreatePayment(ctx context.Context, req CreatePaymentReq) (*CreatePaymentResp, error) {
	body := yookassaCreatePaymentReq{
		Amount: yookassaAmount{
			Value:    kopeksToRubles(req.AmountKopeks),
			Currency: "RUB",
		},
		Description: req.Description,
		Confirmation: yookassaConfirmation{
			Type:      "redirect",
			ReturnURL: req.ReturnURL,
		},
		Capture:  true,
		Metadata: req.Metadata,
	}
	if req.ClientEmail != "" {
		receipt := &yookassaReceipt{
			Items: []yookassaReceiptItem{
				{
					Description:    req.Description,
					Quantity:       "1",
					Amount:         body.Amount,
					VatCode:        1,
					PaymentMode:    "full_payment",
					PaymentSubject: "service",
				},
			},
		}
		receipt.Customer.Email = req.ClientEmail
		body.Receipt = receipt
	}

	var resp yookassaPaymentResp
	if err := p.doRequest(ctx, http.MethodPost, "/payments", body, req.IdempotencyKey, &resp); err != nil {
		return nil, err
	}

	return &CreatePaymentResp{
		PaymentID:       resp.ID,
		ConfirmationURL: resp.Confirmation.URL,
		Status:          resp.Status,
	}, nil
}

func (p *YooKassaProvider) GetPayment(ctx context.Context, paymentID string) (*PaymentInfo, error) {
	var resp yookassaPaymentResp
	if err := p.doRequest(ctx, http.MethodGet, "/payments/"+paymentID, nil, "", &resp); err != nil {
		return nil, err
	}
	return &PaymentInfo{
		PaymentID:    resp.ID,
		Status:       resp.Status,
		Paid:         resp.Paid,
		AmountKopeks: rublesToKopeks(resp.Amount.Value),
		Metadata:     resp.Metadata,
	}, nil
}

type yookassaCreateRefundReq struct {
	PaymentID string         `json:"payment_id"`
	Amount    yookassaAmount `json:"amount"`
}

type yookassaRefundResp struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

func (p *YooKassaProvider) Refund(ctx context.Context, paymentID string, amountKopeks int64) (*RefundInfo, error) {
	body := yookassaCreateRefundReq{
		PaymentID: paymentID,
		Amount: yookassaAmount{
			Value:    kopeksToRubles(amountKopeks),
			Currency: "RUB",
		},
	}
	idempotencyKey := fmt.Sprintf("refund-%s-%d", paymentID, amountKopeks)

	var resp yookassaRefundResp
	if err := p.doRequest(ctx, http.MethodPost, "/refunds", body, idempotencyKey, &resp); err != nil {
		return nil, err
	}
	return &RefundInfo{RefundID: resp.ID, Status: resp.Status}, nil
}
