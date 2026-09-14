package config

import (
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"

	"github.com/joho/godotenv"
)

type Config struct {
	ServerPort                string
	BaseURL                   string
	DB                        DBConfig
	JWTSecret                 string
	JWTTTLHours               int
	SMTP                      SMTPConfig
	CancellationDeadlineHours int
	YooKassa                  YooKassaConfig

	StalePendingMinutes int
	CORSOrigins         []string
}

type DBConfig struct {
	Host     string
	Port     string
	Name     string
	User     string
	Password string
}

func (d DBConfig) DSN() string {
	return fmt.Sprintf(
		"host=%s port=%s dbname=%s user=%s password=%s sslmode=disable",
		d.Host, d.Port, d.Name, d.User, d.Password,
	)
}

type SMTPConfig struct {
	Host     string
	Port     int
	User     string
	Password string
	From     string
}

type YooKassaConfig struct {
	ShopID    string
	SecretKey string
	ReturnURL string

	WebhookIPWhitelist []string
}

func Load() (*Config, error) {
	if err := godotenv.Load(); err != nil {
		log.Printf("warning: .env not loaded: %v", err)
	}

	cfg := &Config{
		ServerPort: getEnv("SERVER_PORT", "8080"),
		BaseURL:    getEnv("BASE_URL", "http://localhost:8080"),

		DB: DBConfig{
			Host:     getEnv("DB_HOST", "localhost"),
			Port:     getEnv("DB_PORT", "5432"),
			Name:     getEnv("DB_NAME", "elena_db"),
			User:     getEnv("DB_USER", "elena_user"),
			Password: getEnv("DB_PASSWORD", ""),
		},

		JWTSecret:   requireEnv("JWT_SECRET"),
		JWTTTLHours: getEnvInt("JWT_TTL_HOURS", 24),

		SMTP: SMTPConfig{
			Host:     getEnv("SMTP_HOST", ""),
			Port:     getEnvInt("SMTP_PORT", 465),
			User:     getEnv("SMTP_USER", ""),
			Password: getEnv("SMTP_PASSWORD", ""),
			From:     getEnv("SMTP_FROM", ""),
		},

		CancellationDeadlineHours: getEnvInt("CANCELLATION_DEADLINE_HOURS", 12),

		YooKassa: YooKassaConfig{
			ShopID:    getEnv("YOOKASSA_SHOP_ID", ""),
			SecretKey: getEnv("YOOKASSA_SECRET_KEY", ""),
			ReturnURL: getEnv("YOOKASSA_RETURN_URL", ""),
			WebhookIPWhitelist: getEnvList("YOOKASSA_WEBHOOK_IPS",
				"185.71.76.0/27,185.71.77.0/27,77.75.153.0/25,77.75.156.11/32,77.75.156.35/32,77.75.154.128/25,2a02:5180::/32"),
		},

		StalePendingMinutes: getEnvInt("STALE_PENDING_MINUTES", 30),
		CORSOrigins:         getEnvList("CORS_ORIGINS", ""),
	}

	return cfg, nil
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func requireEnv(key string) string {
	v := os.Getenv(key)
	if v == "" {
		panic(fmt.Sprintf("required env variable %q is not set", key))
	}
	return v
}

func getEnvList(key, fallback string) []string {
	raw, set := os.LookupEnv(key)
	if !set {
		raw = fallback
	}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func getEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return fallback
}
