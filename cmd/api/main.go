// Command api is the Aebello backend: plan catalog, accounts, orders and
// eSIMs for the web and mobile apps.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/heismyke/aebello/svc/internal/auth"
	"github.com/heismyke/aebello/svc/internal/catalog"
	"github.com/heismyke/aebello/svc/internal/config"
	"github.com/heismyke/aebello/svc/internal/db"
	"github.com/heismyke/aebello/svc/internal/esimaccess"
	"github.com/heismyke/aebello/svc/internal/httpx"
	"github.com/heismyke/aebello/svc/internal/orders"
	"github.com/heismyke/aebello/svc/internal/paystack"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if err := run(); err != nil {
		slog.Error("fatal", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg := config.Load()
	if cfg.ESIMAccessCode == "" {
		return errors.New("ESIM_ACCESS_CODE is not set")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if cfg.AutoMigrate {
		if err := db.Migrate(ctx, cfg.DatabaseURL); err != nil {
			return err
		}
	}
	pool, err := db.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	pricing, err := catalog.LoadPricing(cfg.PricingFile)
	if err != nil {
		return err
	}
	esim := esimaccess.New(cfg.ESIMAccessCode)
	cat := catalog.New(esim, pricing, pool)
	go cat.Run(ctx, cfg.CatalogRefresh)

	pay := paystack.New(cfg.PaystackSecretKey)
	if pay == nil {
		slog.Warn("PAYSTACK_SECRET_KEY is not set; checkout is switched off")
	}
	if !cfg.ESIMOrderingEnabled {
		slog.Warn("ESIM_ORDERING_ENABLED is off; paid orders will not buy eSIMs")
	}
	orderSvc := &orders.Service{
		DB: pool, Catalog: cat, ESIM: esim, Paystack: pay,
		WebURL: cfg.WebURL, OrderingEnabled: cfg.ESIMOrderingEnabled,
	}
	go orderSvc.RunWorker(ctx, 20*time.Second)

	authH := &auth.Handler{DB: pool}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, map[string]any{"ok": true, "catalog": cat.Ready()})
	})
	(&catalog.Handler{Catalog: cat}).Routes(mux)
	authH.Routes(mux)
	(&orders.Handler{Service: orderSvc, Auth: authH}).Routes(mux)

	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           httpx.Logging(httpx.CORS(cfg.AllowedOrigins, mux)),
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	slog.Info("listening", "port", cfg.Port)
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
