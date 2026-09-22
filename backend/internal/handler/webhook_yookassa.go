package handler

import (
	"elena-backend/internal/models"
	"log"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

type yookassaWebhookPayload struct {
	Type   string `json:"type"`
	Event  string `json:"event"`
	Object struct {
		ID        string            `json:"id"`
		PaymentID string            `json:"payment_id"`
		Status    string            `json:"status"`
		Metadata  map[string]string `json:"metadata"`
	} `json:"object"`
}

func (h *Handler) YooKassaWebhook(c *gin.Context) {
	var payload yookassaWebhookPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		log.Printf("yookassa webhook: bad payload: %v", err)
		c.Status(http.StatusOK)
		return
	}

	paymentID := payload.Object.ID
	isRefundEvent := strings.HasPrefix(payload.Event, "refund.")
	if isRefundEvent && payload.Object.PaymentID != "" {
		paymentID = payload.Object.PaymentID
	}
	if paymentID == "" {
		c.Status(http.StatusOK)
		return
	}

	info, err := h.payment.GetPayment(c.Request.Context(), paymentID)
	if err != nil {
		log.Printf("yookassa webhook: GetPayment(%s): %v", paymentID, err)
		c.Status(http.StatusOK)
		return
	}

	metadata := info.Metadata
	if len(metadata) == 0 {
		metadata = payload.Object.Metadata
	}
	apptIDRaw, ok := metadata["appointment_id"]
	if !ok {
		log.Printf("yookassa webhook: no appointment_id in metadata for payment %s", paymentID)
		c.Status(http.StatusOK)
		return
	}
	apptID, err := uuid.Parse(apptIDRaw)
	if err != nil {
		log.Printf("yookassa webhook: invalid appointment_id %q: %v", apptIDRaw, err)
		c.Status(http.StatusOK)
		return
	}


	if appt, getErr := h.apptRepo.GetByID(c.Request.Context(), apptID); getErr == nil && appt.PaymentID == "" {
		if err := h.apptRepo.SetPaymentID(c.Request.Context(), apptID, paymentID); err != nil {
			log.Printf("yookassa webhook: recover payment id: %v", err)
		}
	}

	if isRefundEvent {
		if payload.Event == "refund.succeeded" {
			if appt, getErr := h.apptRepo.GetByID(c.Request.Context(), apptID); getErr == nil && appt.PaymentStatus == models.PaymentStatusRefunded {
				c.Status(http.StatusOK)
				return
			}
			if err := h.apptRepo.UpdatePaymentStatus(c.Request.Context(), apptID, models.PaymentStatusRefunded); err != nil {
				log.Printf("yookassa webhook: update status refunded: %v", err)
			} else if err := h.apptSvc.NotifyRefunded(c.Request.Context(), apptID); err != nil {
				log.Printf("yookassa webhook: notify refunded: %v", err)
			}
		}
		c.Status(http.StatusOK)
		return
	}

	switch info.Status {
	case "succeeded":
		if !info.Paid {
			log.Printf("yookassa webhook: payment %s status=succeeded but paid=false, skipping (event=%q)", paymentID, payload.Event)
			break
		}
		if err := h.apptSvc.ConfirmPayment(c.Request.Context(), apptID); err != nil {
			log.Printf("yookassa webhook: confirm payment: %v", err)
		}

	case "canceled":
		appt, getErr := h.apptRepo.GetByID(c.Request.Context(), apptID)
		if getErr != nil {
			log.Printf("yookassa webhook: load appointment: %v", getErr)
			break
		}
		if appt.Status != models.StatusPending {
			break
		}
		if err := h.apptRepo.UpdatePaymentStatus(c.Request.Context(), apptID, models.PaymentStatusFailed); err != nil {
			log.Printf("yookassa webhook: update payment status failed: %v", err)
		}
		if err := h.apptRepo.UpdateStatus(c.Request.Context(), apptID, models.StatusCancelled); err != nil {
			log.Printf("yookassa webhook: cancel unpaid appointment: %v", err)
		}
		if err := h.apptSvc.NotifyPaymentFailed(c.Request.Context(), apptID); err != nil {
			log.Printf("yookassa webhook: notify payment failed: %v", err)
		}

	default:
		log.Printf("yookassa webhook: payment %s has status %q (event=%q), no action taken", paymentID, info.Status, payload.Event)
	}

	c.Status(http.StatusOK)
}
