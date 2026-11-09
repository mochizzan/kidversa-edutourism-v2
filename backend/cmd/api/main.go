package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/cors"
	"github.com/gofiber/fiber/v3/middleware/healthcheck"
	"github.com/gofiber/fiber/v3/middleware/logger"
	"github.com/gofiber/fiber/v3/middleware/recover"
	"github.com/gofiber/fiber/v3/middleware/requestid"
	"github.com/gofiber/fiber/v3/middleware/static"

	"github.com/kidversa/edutourism-v2/backend/internal"
)

// main follows 02 §2: env → db → migrate → seed → routes → listen.
// Any boot failure is loud: clear log + non-zero exit.
func main() {
	cfg, err := internal.LoadConfig()
	if err != nil {
		slog.Error("config: " + err.Error())
		os.Exit(1)
	}
	db, err := internal.OpenDB(cfg)
	if err != nil {
		slog.Error("db: " + err.Error())
		os.Exit(1)
	}
	if err := internal.Migrate(cfg); err != nil {
		slog.Error("migrate: " + err.Error())
		os.Exit(1)
	}
	if err := internal.Seed(db); err != nil {
		slog.Error("seed: " + err.Error())
		os.Exit(1)
	}
	s, err := internal.NewServer(db, cfg)
	if err != nil {
		slog.Error("server: " + err.Error())
		os.Exit(1)
	}

	app := fiber.New(fiber.Config{
		BodyLimit:    52 * 1024 * 1024, // matches nginx client_max_body_size 52m (Pembaruan 2026-10-11)
		ErrorHandler: internal.ErrorHandler,
		// nginx is the only proxy: c.IP() must see X-Forwarded-For or every
		// limiter/subscription bucket collapses onto the proxy IP (01 §2).
		TrustProxy:       true,
		ProxyHeader:      fiber.HeaderXForwardedFor,
		TrustProxyConfig: fiber.TrustProxyConfig{Proxies: []string{"127.0.0.1", "10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"}},
	})

	// Binding middleware order (01 §2): recover → requestid → logger → cors.
	// requestid MUST be mounted before logger: the ${requestid} tag reads
	// c.RequestID() because the id lives in context, not Locals.
	app.Use(recover.New())
	app.Use(requestid.New())
	app.Use(logger.New(logger.Config{
		Format: "${ip} ${port} ${status} ${method} ${path} ${error} ${latency} ${pid} ${requestid}\n",
		CustomTags: map[string]logger.LogFunc{
			"requestid": func(o logger.Buffer, c fiber.Ctx, _ *logger.Data, _ string) (int, error) {
				return o.WriteString(c.RequestID())
			},
		},
	}))
	app.Use(cors.New(cors.Config{
		// Exact explicit list; never "*" with credentials (validated at load).
		AllowOrigins:     cfg.CorsOrigins,
		AllowCredentials: true,
		AllowMethods:     []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
		AllowHeaders:     []string{"Authorization", "Content-Type", "X-Tenant-ID", "X-Idempotency-Key"},
		ExposeHeaders:    []string{"Retry-After"},
		MaxAge:           600,
	}))

	// Health probes (plain text, outside the envelope rule); readiness probes DB.
	app.Get(healthcheck.LivenessEndpoint, healthcheck.New())
	app.Get(healthcheck.ReadinessEndpoint, healthcheck.New(healthcheck.Config{
		Probe: func(fiber.Ctx) bool {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			return db.PingContext(ctx) == nil
		},
	}))
	app.Get(healthcheck.StartupEndpoint, healthcheck.New())

	// Uploads: read-only static bytes (Browse/Download off, 10 s cache).
	app.Get("/uploads/*", static.New(cfg.UploadDir, static.Config{
		Browse:        false,
		Download:      false,
		CacheDuration: 10 * time.Second,
	}))

	// Groups WITHOUT global auth — every domain attaches s.authed(...) and its
	// own limiters per route (02 §2 pipeline).
	api := app.Group("/api")
	pub := app.Group("/public")

	s.RegisterAuth(api)
	s.RegisterStream(api, pub)
	s.RegisterWa(api)

	// Domain registrars (I1 integration): each attaches its own
	// s.authed(...) + per-route limiters; api = /api group, pub = /public.
	s.RegisterTenants(api)
	s.RegisterUsers(api)
	s.RegisterPrograms(api)
	s.RegisterTopics(api)
	s.RegisterActivities(api)
	s.RegisterMissions(api)
	s.RegisterFrames(api)
	s.RegisterChildren(api)
	s.RegisterSessions(api)
	s.RegisterAssessments(api)
	s.RegisterPhotos(api)
	s.RegisterConsents(api)
	s.RegisterReports(api)
	s.RegisterDashboard(api)
	s.RegisterPublic(pub)

	// Janitor + OpenWA watcher + stats.changed aggregator tick (02 §2c).
	s.StartBackground()

	// Graceful shutdown: signal → stop background + in-flight streams → drain
	// HTTP → close the pool.
	shutdownCh := make(chan struct{})
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sigCh
		close(shutdownCh)
		slog.Info("shutting down")
		s.Stop()
		if err := app.ShutdownWithTimeout(10 * time.Second); err != nil {
			slog.Error("http shutdown: " + err.Error())
		}
	}()

	if err := app.Listen(":" + cfg.AppPort); err != nil {
		select {
		case <-shutdownCh:
			// listen returned because of the requested shutdown
		default:
			slog.Error("listen: " + err.Error())
			os.Exit(1)
		}
	}
	db.Close()
}
