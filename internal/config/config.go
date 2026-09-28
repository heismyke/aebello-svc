// Package config reads settings from the environment. Every setting has a
// development default so `go run ./cmd/api` works against docker compose.
package config

import (
	"bufio"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Port        string
	DatabaseURL string
	AutoMigrate bool // apply pending migrations on start

	// WebURL is the public web app. Payment redirects return there, and it is
	// always allowed by CORS.
	WebURL         string
	AllowedOrigins []string

	ESIMAccessCode string
	// ESIMOrderingEnabled must be true before paid orders are sent to eSIM
	// Access. Their API has no sandbox, so every order spends real balance.
	ESIMOrderingEnabled bool

	PaystackSecretKey string
	// ChargeCurrency is what Paystack collects. USD prices are converted at
	// the daily rate plus FXBuffer; FXFallbackRate covers a rate-feed outage.
	ChargeCurrency string
	FXBuffer       float64
	FXFallbackRate float64

	PricingFile    string
	CatalogRefresh time.Duration
}

func Load() Config {
	loadDotEnv(".env")
	web := strings.TrimRight(env("WEB_URL", "http://localhost:5173"), "/")
	return Config{
		Port:                env("PORT", "8080"),
		DatabaseURL:         env("DATABASE_URL", "postgres://aebello:aebello@localhost:5460/aebello?sslmode=disable"),
		AutoMigrate:         envBool("AUTO_MIGRATE", true),
		WebURL:              web,
		AllowedOrigins:      append(envList("ALLOWED_ORIGINS"), web),
		ESIMAccessCode:      os.Getenv("ESIM_ACCESS_CODE"),
		ESIMOrderingEnabled: envBool("ESIM_ORDERING_ENABLED", false),
		PaystackSecretKey:   os.Getenv("PAYSTACK_SECRET_KEY"),
		ChargeCurrency:      strings.ToUpper(env("CHARGE_CURRENCY", "NGN")),
		FXBuffer:            envFloat("FX_BUFFER", 0.03),
		FXFallbackRate:      envFloat("FX_FALLBACK_RATE", 0),
		PricingFile:         env("PRICING_FILE", "pricing.json"),
		CatalogRefresh:      envDuration("CATALOG_REFRESH", 6*time.Hour),
	}
}

// loadDotEnv sets KEY=VALUE lines from a local file without overriding real
// environment variables. It is a convenience for local development only.
func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	s := bufio.NewScanner(f)
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.HasPrefix(line, "#") {
			continue
		}
		key = strings.TrimSpace(key)
		if _, set := os.LookupEnv(key); !set {
			os.Setenv(key, strings.Trim(strings.TrimSpace(value), `"'`))
		}
	}
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envList(key string) []string {
	var out []string
	for _, v := range strings.Split(os.Getenv(key), ",") {
		if v = strings.TrimRight(strings.TrimSpace(v), "/"); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func envBool(key string, fallback bool) bool {
	if b, err := strconv.ParseBool(os.Getenv(key)); err == nil {
		return b
	}
	return fallback
}

func envFloat(key string, fallback float64) float64 {
	if f, err := strconv.ParseFloat(os.Getenv(key), 64); err == nil {
		return f
	}
	return fallback
}

func envDuration(key string, fallback time.Duration) time.Duration {
	if d, err := time.ParseDuration(os.Getenv(key)); err == nil {
		return d
	}
	return fallback
}
