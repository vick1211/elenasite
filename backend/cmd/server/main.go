package main

import (
	"context"
	"elena-backend/internal/config"
	"elena-backend/internal/db"
	"elena-backend/internal/handler"
	"elena-backend/internal/mailer"
	"elena-backend/internal/repository"
	"elena-backend/internal/service"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config: %v", err)
	}

	database, err := db.Connect(cfg.DB)
	if err != nil {
		log.Fatalf("db connect: %v", err)
	}
	defer database.Close()
	log.Println("PostgreSQL connected")

	clientRepo := repository.NewClientRepo(database)
	apptRepo := repository.NewAppointmentRepo(database)
	tokenRepo := repository.NewCancellationTokenRepo(database)
	serviceRepo := repository.NewServiceRepo(database)
	newsRepo := repository.NewNewsRepo(database)
	reviewRepo := repository.NewReviewRepo(database)
	blockedSlotRepo := repository.NewBlockedSlotRepo(database)

	var mailSender service.MailSender
	if cfg.SMTP.Host == "" {
		log.Println("SMTP not configured — using stub mailer")
		mailSender = &mailer.StubMailer{}
	} else {
		mailSender = mailer.New(cfg.SMTP)
	}

	var paymentProvider service.PaymentProvider
	if cfg.YooKassa.ShopID == "" || cfg.YooKassa.SecretKey == "" {
		log.Println("YooKassa not configured — using stub payment provider")
		paymentProvider = &service.StubPaymentProvider{}
	} else {
		paymentProvider = service.NewYooKassaProvider(cfg.YooKassa)
	}

	apptSvc := service.NewAppointmentService(
		apptRepo,
		tokenRepo,
		clientRepo,
		serviceRepo,
		blockedSlotRepo,
		mailSender,
		paymentProvider,
		cfg.BaseURL,
		cfg.YooKassa.ReturnURL,
		cfg.CancellationDeadlineHours,
	)

	go runStalePendingCleanup(apptSvc, time.Duration(cfg.StalePendingMinutes)*time.Minute)

	r := gin.Default()

	if err := r.SetTrustedProxies([]string{"127.0.0.1", "172.18.0.1"}); err != nil {
		log.Fatalf("set trusted proxies: %v", err)
	}

	allowedOrigins := make(map[string]struct{}, len(cfg.CORSOrigins))
	for _, origin := range cfg.CORSOrigins {
		allowedOrigins[origin] = struct{}{}
	}
	r.Use(func(c *gin.Context) {
		origin := c.GetHeader("Origin")
		if origin != "" {
			if _, ok := allowedOrigins[origin]; ok {
				c.Header("Access-Control-Allow-Origin", origin)
				c.Header("Vary", "Origin")
			}
		}
		c.Header("Access-Control-Allow-Methods", "GET,POST,PUT,PATCH,DELETE,OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Content-Type,Authorization")
		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}
		c.Next()
	})

	h := handler.New(
		database,
		apptSvc,
		clientRepo,
		apptRepo,
		serviceRepo,
		newsRepo,
		reviewRepo,
		blockedSlotRepo,
		paymentProvider,
		cfg.JWTSecret,
		time.Duration(cfg.JWTTTLHours)*time.Hour,
		cfg.YooKassa.WebhookIPWhitelist,
	)
	h.RegisterRoutes(r)

	addr := fmt.Sprintf(":%s", cfg.ServerPort)
	server := &http.Server{Addr: addr, Handler: r, ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: 60 * time.Second}

	go func() {
		log.Printf("Server listening on %s", addr)
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server: %v", err)
		}
	}()

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		log.Printf("server shutdown: %v", err)
	}
}

func runStalePendingCleanup(apptSvc *service.AppointmentService, staleAfter time.Duration) {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		n, err := apptSvc.ExpireStalePending(ctx, staleAfter)
		cancel()
		if err != nil {
			log.Printf("stale pending cleanup: %v", err)
			continue
		}
		if n > 0 {
			log.Printf("stale pending cleanup: cancelled %d unpaid appointment(s)", n)
		}
	}
}
