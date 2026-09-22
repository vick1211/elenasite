package handler

import (
	"context"
	"elena-backend/internal/middleware"
	"elena-backend/internal/models"
	"elena-backend/internal/repository"
	"elena-backend/internal/service"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/jmoiron/sqlx"
	"golang.org/x/crypto/bcrypt"
)

type Handler struct {
	db              *sqlx.DB
	apptSvc         *service.AppointmentService
	clientRepo      *repository.ClientRepo
	apptRepo        *repository.AppointmentRepo
	serviceRepo     *repository.ServiceRepo
	newsRepo        *repository.NewsRepo
	reviewRepo      *repository.ReviewRepo
	blockedSlotRepo *repository.BlockedSlotRepo
	payment         service.PaymentProvider
	jwtSecret       string
	jwtTTL          time.Duration

	webhookIPWhitelist   []string
	publicBookingLimiter gin.HandlerFunc
	publicCancelLimiter  gin.HandlerFunc
	adminLoginLimiter    gin.HandlerFunc
}

func New(
	db *sqlx.DB,
	apptSvc *service.AppointmentService,
	clientRepo *repository.ClientRepo,
	apptRepo *repository.AppointmentRepo,
	serviceRepo *repository.ServiceRepo,
	newsRepo *repository.NewsRepo,
	reviewRepo *repository.ReviewRepo,
	blockedSlotRepo *repository.BlockedSlotRepo,
	payment service.PaymentProvider,
	jwtSecret string,
	jwtTTL time.Duration,
	webhookIPWhitelist []string,
) *Handler {
	return &Handler{
		publicBookingLimiter: middleware.NewRateLimiter(20, time.Minute).Middleware(),
		publicCancelLimiter:  middleware.NewRateLimiter(20, time.Minute).Middleware(),
		adminLoginLimiter:    middleware.NewRateLimiter(10, time.Minute).Middleware(),
		db:                   db,
		apptSvc:              apptSvc,
		clientRepo:           clientRepo,
		apptRepo:             apptRepo,
		serviceRepo:          serviceRepo,
		newsRepo:             newsRepo,
		reviewRepo:           reviewRepo,
		blockedSlotRepo:      blockedSlotRepo,
		payment:              payment,
		jwtSecret:            jwtSecret,
		jwtTTL:               jwtTTL,
		webhookIPWhitelist:   webhookIPWhitelist,
	}
}

func parseCalendarRFC3339(raw string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, errors.New("invalid starts_at, use RFC3339")
	}
	_, offset := t.Zone()
	if offset != 5*60*60 {
		return time.Time{}, errors.New("время должно быть передано с часовым поясом Asia/Yekaterinburg (+05:00)")
	}
	return t, nil
}

var allSlotTimes = []string{"10:00", "11:00", "12:00", "13:00", "14:00", "15:00", "16:00", "17:00", "18:00", "19:00"}

func (h *Handler) RegisterRoutes(r *gin.Engine) {
	r.GET("/healthz", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })
	r.GET("/readyz", h.Ready)
	api := r.Group("/api/v1")

	api.GET("/services", h.ListServices)
	api.GET("/news", h.ListNews)
	api.GET("/reviews", h.ListReviews)
	api.GET("/availability", h.GetAvailability)

	api.POST("/appointments", h.publicBookingLimiter, h.CreateAppointment)

	api.GET("/cancel", h.publicCancelLimiter, h.CancelPreview)
	api.POST("/cancel", h.publicCancelLimiter, h.CancelConfirm)

	api.POST("/webhooks/yookassa",
		middleware.RequireIPWhitelist(h.webhookIPWhitelist),
		h.YooKassaWebhook,
	)

	admin := api.Group("/admin")
	admin.POST("/login", h.adminLoginLimiter, h.AdminLogin)

	protected := admin.Group("", middleware.RequireAdmin(h.jwtSecret))
	{
		protected.GET("/clients", h.AdminListClients)
		protected.POST("/clients", h.AdminCreateClient)
		protected.POST("/appointments", h.AdminCreateAppointment)
		protected.DELETE("/clients/:id", h.AdminDeleteClient)

		protected.GET("/appointments", h.AdminListAppointments)
		protected.DELETE("/appointments/:id", h.AdminDeleteAppointment)
		protected.PATCH("/appointments/:id/reschedule", h.AdminReschedule)
		protected.PATCH("/appointments/:id/status", h.AdminUpdateStatus)

		protected.POST("/services", h.AdminCreateService)
		protected.PUT("/services/:id", h.AdminUpdateService)
		protected.DELETE("/services/:id", h.AdminDeleteService)

		protected.GET("/blocks", h.AdminListBlocks)
		protected.POST("/blocks", h.AdminCreateBlock)
		protected.DELETE("/blocks/:id", h.AdminDeleteBlock)

		protected.GET("/news", h.AdminListNews)
		protected.POST("/news", h.AdminCreateNews)
		protected.PUT("/news/:id", h.AdminUpdateNews)
		protected.DELETE("/news/:id", h.AdminDeleteNews)

		protected.GET("/reviews", h.AdminListReviews)
		protected.POST("/reviews", h.AdminCreateReview)
		protected.PUT("/reviews/:id", h.AdminUpdateReview)
		protected.DELETE("/reviews/:id", h.AdminDeleteReview)
	}
}

func (h *Handler) Ready(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
	defer cancel()
	if err := h.db.PingContext(ctx); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"ok": false})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *Handler) ListServices(c *gin.Context) {
	list, err := h.serviceRepo.ListActive(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	c.JSON(http.StatusOK, list)
}

func (h *Handler) ListNews(c *gin.Context) {
	limit, offset, ok := parsePagination(c, 20)
	if !ok {
		return
	}
	list, err := h.newsRepo.ListPublished(c.Request.Context(), limit, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	c.JSON(http.StatusOK, list)
}

func (h *Handler) ListReviews(c *gin.Context) {
	list, err := h.reviewRepo.ListVisible(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	c.JSON(http.StatusOK, list)
}

func (h *Handler) GetAvailability(c *gin.Context) {
	loc, err := time.LoadLocation("Asia/Yekaterinburg")
	if err != nil {
		loc = time.FixedZone("YEKT", 5*3600)
	}

	monthParam := c.Query("month")
	var from time.Time
	if monthParam != "" {
		parsed, err := time.ParseInLocation("2006-01", monthParam, loc)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid month, expected YYYY-MM"})
			return
		}
		from = parsed
	} else {
		now := time.Now().In(loc)
		from = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc)
	}
	to := from.AddDate(0, 1, 0)

	busyTimes, err := h.apptRepo.ListBusyTimes(c.Request.Context(), from.UTC(), to.UTC())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}

	result := make(map[string][]string)
	for _, interval := range busyTimes {
		localStart := interval.StartsAt.In(loc)
		localEnd := interval.EndsAt.In(loc)
		for hour := localStart.Truncate(time.Hour); hour.Before(localEnd); hour = hour.Add(time.Hour) {
			if hour.Hour() < 10 || hour.Hour() > 19 {
				continue
			}
			dateKey := hour.Format("2006-01-02")
			timeKey := hour.Format("15:04")
			result[dateKey] = append(result[dateKey], timeKey)
		}
	}

	blocks, err := h.blockedSlotRepo.ListRange(c.Request.Context(), from, to)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	for _, b := range blocks {
		dateKey := b.BlockedDate.Format("2006-01-02")
		if b.SlotTime == nil {
			result[dateKey] = append(result[dateKey], allSlotTimes...)
		} else {
			result[dateKey] = append(result[dateKey], *b.SlotTime)
		}
	}

	c.JSON(http.StatusOK, result)
}

func safeAppointmentError(err error) string {
	msg := err.Error()
	for _, allowed := range []string{
		"нельзя записаться на прошедшее время",
		"время записи должно быть ровно на целый час",
		"время записи должно быть с 10:00 до 19:00",
		"сеанс выходит за пределы рабочего времени",
		"выбранное время заблокировано",
		"услуга недоступна для записи",
		"для этой услуги доступен только онлайн-формат",
		"для этой услуги доступен только очный формат",
		"время уже занято или произошла ошибка",
		"время должно быть передано с часовым поясом Asia/Yekaterinburg",
		"демо-услуга недоступна для публичной записи",
		"эту запись нельзя перенести",
	} {
		if strings.HasPrefix(msg, allowed) {
			return allowed
		}
	}
	if errors.Is(err, repository.ErrPhoneTakenByAnotherEmail) {
		return msg
	}
	return "не удалось создать запись"
}

func (h *Handler) CreateAppointment(c *gin.Context) {
	var req struct {
		Client struct {
			FirstName string `json:"first_name" binding:"required"`
			LastName  string `json:"last_name"  binding:"required"`
			Patronym  string `json:"patronym"`
			Phone     string `json:"phone"      binding:"required"`
			Email     string `json:"email"      binding:"required,email"`
		} `json:"client" binding:"required"`
		ServiceID   string `json:"service_id"   binding:"required"`
		Format      string `json:"format"       binding:"required,oneof=online offline"`
		StartsAt    string `json:"starts_at"    binding:"required"`
		PaymentMode string `json:"payment_mode" binding:"omitempty,oneof=full prepay_50"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "internal server error"})
		return
	}

	serviceID, err := uuid.Parse(req.ServiceID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid service_id"})
		return
	}
	startsAt, err := parseCalendarRFC3339(req.StartsAt)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid starts_at, use RFC3339"})
		return
	}

	paymentMode := models.PaymentMode(req.PaymentMode)
	if paymentMode == "" {
		paymentMode = models.PaymentModeFull
	}

	result, err := h.apptSvc.Book(c.Request.Context(), service.BookRequest{
		Client: service.ClientInfo{
			FirstName: req.Client.FirstName,
			LastName:  req.Client.LastName,
			Patronym:  req.Client.Patronym,
			Phone:     req.Client.Phone,
			Email:     req.Client.Email,
		},
		ServiceID:   serviceID,
		Format:      models.AppointmentFormat(req.Format),
		StartsAt:    startsAt,
		PaymentMode: paymentMode,
	})
	if err != nil {
		if errors.Is(err, repository.ErrPhoneTakenByAnotherEmail) {
			c.JSON(http.StatusConflict, gin.H{"error": "номер телефона уже используется другим email"})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": safeAppointmentError(err)})
		return
	}

	c.JSON(http.StatusCreated, gin.H{
		"appointment": result.Appointment,
		"payment_url": result.PaymentURL,
	})
}

func (h *Handler) CancelPreview(c *gin.Context) {
	token := c.Query("token")
	if token == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "missing token"})
		return
	}

	preview, err := h.apptSvc.PreviewCancellation(c.Request.Context(), token)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "ссылка недействительна или уже использована"})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"client_name":      preview.ClientName,
		"appointment_date": preview.Appointment.StartsAt.Format("02.01.2006 в 15:04"),
		"refundable":       preview.Refundable,
		"token":            token,
	})
}

func (h *Handler) CancelConfirm(c *gin.Context) {
	var req struct {
		Token string `json:"token" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "internal server error"})
		return
	}

	if err := h.apptSvc.ConfirmCancellation(c.Request.Context(), req.Token); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *Handler) AdminLogin(c *gin.Context) {
	var req struct {
		Username string `json:"username" binding:"required"`
		Password string `json:"password" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "internal server error"})
		return
	}

	var admin models.Admin
	err := h.db.GetContext(c.Request.Context(), &admin,
		`select * from admins where username = $1`, req.Username)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "неверные данные"})
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(admin.PasswordHash), []byte(req.Password)); err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "неверные данные"})
		return
	}

	token, err := middleware.GenerateToken(admin.ID.String(), h.jwtSecret, h.jwtTTL)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "token generation failed"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"token": token})
}

func parsePagination(c *gin.Context, defaultLimit int) (int, int, bool) {
	limit, offset := defaultLimit, 0
	if raw := c.Query("limit"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 200 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "limit must be between 1 and 200"})
			return 0, 0, false
		}
		limit = n
	}
	if raw := c.Query("offset"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "offset must be >= 0"})
			return 0, 0, false
		}
		offset = n
	}
	return limit, offset, true
}

func (h *Handler) AdminListClients(c *gin.Context) {
	limit, offset, ok := parsePagination(c, 50)
	if !ok {
		return
	}
	list, err := h.clientRepo.List(c.Request.Context(), limit, offset)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	c.JSON(http.StatusOK, list)
}

func (h *Handler) AdminDeleteClient(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	if err := h.clientRepo.Delete(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *Handler) AdminCreateClient(c *gin.Context) {
	var req struct {
		FirstName string `json:"first_name" binding:"required"`
		LastName  string `json:"last_name" binding:"required"`
		Patronym  string `json:"patronym"`
		Phone     string `json:"phone" binding:"required"`
		Email     string `json:"email" binding:"required,email"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "internal server error"})
		return
	}

	client := &models.Client{
		FirstName: req.FirstName,
		LastName:  req.LastName,
		Patronym:  req.Patronym,
		Phone:     req.Phone,
		Email:     req.Email,
	}

	created, err := h.clientRepo.FindOrCreate(c.Request.Context(), client)
	if err != nil {
		if errors.Is(err, repository.ErrPhoneTakenByAnotherEmail) {
			c.JSON(http.StatusConflict, gin.H{"error": "номер телефона уже используется другим email"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}

	c.JSON(http.StatusCreated, created)
}

func (h *Handler) AdminListAppointments(c *gin.Context) {
	opts := repository.ListAppointmentsOpts{
		Limit:  50,
		Offset: 0,
	}
	if s := c.Query("status"); s != "" {
		opts.Status = models.AppointmentStatus(s)
	}
	if c.Query("limit") != "" {
		l, err := strconv.Atoi(c.Query("limit"))
		if err != nil || l < 1 || l > 200 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "limit must be between 1 and 200"})
			return
		}
		opts.Limit = l
	}
	if c.Query("offset") != "" {
		o, err := strconv.Atoi(c.Query("offset"))
		if err != nil || o < 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "offset must be >= 0"})
			return
		}
		opts.Offset = o
	}

	list, err := h.apptRepo.List(c.Request.Context(), opts)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	c.JSON(http.StatusOK, list)
}

func (h *Handler) AdminDeleteAppointment(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	appt, err := h.apptRepo.GetByID(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "appointment not found"})
		return
	}
	if appt.PaymentStatus == models.PaymentStatusPaid {
		c.JSON(http.StatusConflict, gin.H{"error": "оплаченную запись нельзя удалить; сначала оформите возврат"})
		return
	}
	if err := h.apptRepo.Delete(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "не удалось удалить запись"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *Handler) AdminReschedule(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	var req struct {
		StartsAt string `json:"starts_at" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "internal server error"})
		return
	}
	t, err := parseCalendarRFC3339(req.StartsAt)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid starts_at"})
		return
	}
	if err := h.apptSvc.Reschedule(c.Request.Context(), id, t); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": safeAppointmentError(err)})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *Handler) AdminUpdateStatus(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	var req struct {
		Status string `json:"status" binding:"required"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "internal server error"})
		return
	}
	allowedStatus := models.AppointmentStatus(req.Status)
	switch allowedStatus {
	case models.StatusPending, models.StatusConfirmed, models.StatusCancelled, models.StatusCompleted, models.StatusNoShow:
	default:
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid status"})
		return
	}
	appt, err := h.apptRepo.GetByID(c.Request.Context(), id)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "appointment not found"})
		return
	}
	if allowedStatus == models.StatusCancelled && appt.PaymentStatus == models.PaymentStatusPaid {
		c.JSON(http.StatusConflict, gin.H{"error": "оплаченную запись нельзя отменить вручную; сначала оформите возврат"})
		return
	}
	if err := h.apptRepo.UpdateStatus(c.Request.Context(), id, allowedStatus); err != nil {
		if errors.Is(err, repository.ErrInvalidStatusTransition) {
			c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "не удалось изменить статус"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *Handler) AdminCreateService(c *gin.Context) {
	var s models.Service
	if err := c.ShouldBindJSON(&s); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "internal server error"})
		return
	}
	if err := h.serviceRepo.Create(c.Request.Context(), &s); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	c.JSON(http.StatusCreated, s)
}

func (h *Handler) AdminUpdateService(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	var s models.Service
	if err := c.ShouldBindJSON(&s); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "internal server error"})
		return
	}
	s.ID = id
	if err := h.serviceRepo.Update(c.Request.Context(), &s); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	c.JSON(http.StatusOK, s)
}

func (h *Handler) AdminDeleteService(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	if err := h.serviceRepo.Delete(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *Handler) AdminListNews(c *gin.Context) {
	list, err := h.newsRepo.ListAll(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	c.JSON(http.StatusOK, list)
}

func (h *Handler) AdminCreateNews(c *gin.Context) {
	var n models.News
	if err := c.ShouldBindJSON(&n); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "internal server error"})
		return
	}
	if err := h.newsRepo.Create(c.Request.Context(), &n); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	c.JSON(http.StatusCreated, n)
}

func (h *Handler) AdminUpdateNews(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	var n models.News
	if err := c.ShouldBindJSON(&n); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "internal server error"})
		return
	}
	n.ID = id
	if err := h.newsRepo.Update(c.Request.Context(), &n); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	c.JSON(http.StatusOK, n)
}

func (h *Handler) AdminDeleteNews(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	if err := h.newsRepo.Delete(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *Handler) AdminListReviews(c *gin.Context) {
	list, err := h.reviewRepo.ListAll(c.Request.Context())
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	c.JSON(http.StatusOK, list)
}

func (h *Handler) AdminCreateReview(c *gin.Context) {
	var r models.Review
	if err := c.ShouldBindJSON(&r); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "internal server error"})
		return
	}
	if err := h.reviewRepo.Create(c.Request.Context(), &r); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	c.JSON(http.StatusCreated, r)
}

func (h *Handler) AdminUpdateReview(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	var r models.Review
	if err := c.ShouldBindJSON(&r); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "internal server error"})
		return
	}
	r.ID = id
	if err := h.reviewRepo.Update(c.Request.Context(), &r); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	c.JSON(http.StatusOK, r)
}

func (h *Handler) AdminDeleteReview(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	if err := h.reviewRepo.Delete(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"ok": true})
}

func (h *Handler) AdminListBlocks(c *gin.Context) {
	loc, err := time.LoadLocation("Asia/Yekaterinburg")
	if err != nil {
		loc = time.FixedZone("YEKT", 5*3600)
	}

	monthParam := c.Query("month")
	var from time.Time
	if monthParam != "" {
		parsed, err := time.ParseInLocation("2006-01", monthParam, loc)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid month, expected YYYY-MM"})
			return
		}
		from = parsed
	} else {
		now := time.Now().In(loc)
		from = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc)
	}
	to := from.AddDate(0, 1, 0)

	list, err := h.blockedSlotRepo.ListRange(c.Request.Context(), from, to)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	c.JSON(http.StatusOK, list)
}

type createBlockRequest struct {
	Date     string  `json:"date" binding:"required"`
	SlotTime *string `json:"slot_time"`
	Reason   string  `json:"reason"`
}

func (h *Handler) AdminCreateBlock(c *gin.Context) {
	var req createBlockRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "internal server error"})
		return
	}

	date, err := time.Parse("2006-01-02", req.Date)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid date, expected YYYY-MM-DD"})
		return
	}

	bs, err := h.blockedSlotRepo.Create(c.Request.Context(), date, req.SlotTime, req.Reason)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	c.JSON(http.StatusCreated, bs)
}

func (h *Handler) AdminDeleteBlock(c *gin.Context) {
	id, err := uuid.Parse(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid id"})
		return
	}
	if err := h.blockedSlotRepo.Delete(c.Request.Context(), id); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) AdminCreateAppointment(c *gin.Context) {
	var req struct {
		Client struct {
			FirstName string `json:"first_name" binding:"required"`
			LastName  string `json:"last_name"  binding:"required"`
			Patronym  string `json:"patronym"`
			Phone     string `json:"phone"      binding:"required"`
			Email     string `json:"email"      binding:"required,email"`
		} `json:"client" binding:"required"`
		ServiceID     string `json:"service_id"     binding:"required"`
		Format        string `json:"format"         binding:"required,oneof=online offline"`
		StartsAt      string `json:"starts_at"      binding:"required"`
		PaymentStatus string `json:"payment_status" binding:"omitempty,oneof=paid pending"`
		Notes         string `json:"notes"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "internal server error"})
		return
	}

	serviceID, err := uuid.Parse(req.ServiceID)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid service_id"})
		return
	}
	startsAt, err := parseCalendarRFC3339(req.StartsAt)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid starts_at, use RFC3339"})
		return
	}

	appt, err := h.apptSvc.BookManual(c.Request.Context(), service.ManualBookRequest{
		Client: service.ClientInfo{
			FirstName: req.Client.FirstName,
			LastName:  req.Client.LastName,
			Patronym:  req.Client.Patronym,
			Phone:     req.Client.Phone,
			Email:     req.Client.Email,
		},
		ServiceID:     serviceID,
		Format:        models.AppointmentFormat(req.Format),
		StartsAt:      startsAt,
		PaymentStatus: req.PaymentStatus,
		Notes:         req.Notes,
	})
	if err != nil {
		if errors.Is(err, repository.ErrPhoneTakenByAnotherEmail) {
			c.JSON(http.StatusConflict, gin.H{"error": "номер телефона уже используется другим email"})
			return
		}
		c.JSON(http.StatusBadRequest, gin.H{"error": "internal server error"})
		return
	}

	c.JSON(http.StatusCreated, appt)
}
