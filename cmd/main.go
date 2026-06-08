package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"nana/internal/apartment"
	"nana/internal/auth"
	"nana/internal/billing"
	"nana/internal/billingconfig"
	"nana/internal/billingreconciliation"
	"nana/internal/contract"
	"nana/internal/meterreading"
	"nana/internal/moveout"
	"nana/internal/room"
	"nana/internal/seed"
	"nana/internal/tenant"
	"nana/internal/shared/config"
	"nana/internal/shared/database"
	"nana/internal/shared/logger"
	"nana/internal/shared/middleware"
	"nana/internal/shared/role"

	"github.com/gofiber/fiber/v3"
	"github.com/gofiber/fiber/v3/middleware/recover"
	"github.com/joho/godotenv"
)

func main() {
	// Load .env file (ignore error if not present)
	_ = godotenv.Load()

	// Load config
	cfg := config.Load()
	if err := cfg.Validate(); err != nil {
		slog.Error("config validation failed", "error", err)
		os.Exit(1)
	}

	// Initialize logger
	logger.Init(cfg.Env)

	// Connect to database
	db, err := database.Connect(cfg)
	if err != nil {
		slog.Error("database connection failed", "error", err)
		os.Exit(1)
	}

	// Run migrations
	if err := database.RunMigrations(db); err != nil {
		slog.Error("database migration failed", "error", err)
		os.Exit(1)
	}

	// Seed data
	if err := seed.Run(db, cfg.Env); err != nil {
		slog.Error("seed failed", "error", err)
		os.Exit(1)
	}

	// Cancellable context for background goroutines
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Transaction manager
	txManager := database.NewTxManager(db)

	// Wire dependencies — Auth
	userRepo := auth.NewUserRepository(db)
	authService := auth.NewAuthService(userRepo, cfg, txManager)
	authHandler := auth.NewAuthHandler(authService, cfg)
	authService.StartTokenCleanup(ctx, 1*time.Hour)

	// Wire dependencies — Apartments
	aptRepo := apartment.NewApartmentRepository(db)
	aptService := apartment.NewApartmentService(aptRepo)
	aptHandler := apartment.NewApartmentHandler(aptService)

	// Wire dependencies — Bank Accounts
	bankRepo := apartment.NewBankAccountRepository(db)
	bankService := apartment.NewBankAccountService(bankRepo, aptRepo, txManager)
	bankHandler := apartment.NewBankAccountHandler(bankService)

	// Wire dependencies — Rooms
	roomRepo := room.NewRoomRepository(db)
	roomService := room.NewRoomService(roomRepo, aptRepo)
	roomHandler := room.NewRoomHandler(roomService)

	// Wire dependencies — Tenants
	tenantRepo := tenant.NewTenantRepository(db)

	// Wire dependencies — Contracts
	contractRepo := contract.NewContractRepository(db)
	contractService := contract.NewContractService(contractRepo, roomRepo, roomRepo, tenantRepo, txManager)
	contractHandler := contract.NewContractHandler(contractService)

	tenantService := tenant.NewTenantService(tenantRepo, contractRepo)
	tenantHandler := tenant.NewTenantHandler(tenantService)

	// Wire dependencies — Billing Configs
	bcRepo := billingconfig.NewBillingConfigRepository(db)
	bcService := billingconfig.NewBillingConfigService(bcRepo, aptRepo)
	bcHandler := billingconfig.NewBillingConfigHandler(bcService)

	// Wire dependencies — Meter Readings (repo first; service wired after moveOutRepo)
	meterRepo := meterreading.NewMeterReadingRepository(db)

	// Wire dependencies — Move-Out Notices (billingService injected below after billing init)
	moveOutRepo := moveout.NewMoveOutRepository(db)

	// Meter Reading service (needs moveOutRepo for MoveOutChecker port)
	meterService := meterreading.NewMeterReadingService(meterRepo, roomRepo, contractRepo, moveOutRepo, txManager)
	meterHandler := meterreading.NewMeterReadingHandler(meterService)

	// Wire dependencies — Billing
	billRepo := billing.NewBillingRepository(db)
	billAuditRepo := billing.NewBillAuditRepository(db)
	billService := billing.NewBillingService(billRepo, billAuditRepo, contractRepo, meterRepo, bcRepo, moveOutRepo, txManager)
	billHandler := billing.NewBillingHandler(billService)

	// Wire Move-Out service (needs billingService as BillingCommander + BillingQuerier)
	moveOutService := moveout.NewMoveOutService(moveOutRepo, contractRepo, contractRepo, roomRepo, meterService, billService, billService, txManager)
	moveOutHandler := moveout.NewMoveOutHandler(moveOutService)

	// Wire dependencies — Billing Reconciliation (Phase 1A: read-only audit)
	reconRepo := billingreconciliation.NewRepository(db)
	reconService := billingreconciliation.NewService(reconRepo, meterRepo, moveOutRepo, billService)
	reconHandler := billingreconciliation.NewHandler(reconService)

	// Create Fiber app
	app := fiber.New(fiber.Config{
		AppName:       "Nana Rental Management",
		CaseSensitive: true,
		StrictRouting: false,
		ReadTimeout:   10 * time.Second,
		WriteTimeout:  10 * time.Second,
		IdleTimeout:   120 * time.Second,
	})

	// Global middleware
	app.Use(recover.New())
	app.Use(middleware.SecurityHeaders())
	app.Use(middleware.CORS(cfg))

	// Health check
	app.Get("/health", func(c fiber.Ctx) error {
		return c.JSON(fiber.Map{
			"status": "ok",
			"env":    cfg.Env,
		})
	})

	// API v1 routes
	v1 := app.Group("/api/v1")

	// Public auth routes
	authHandler.RegisterRoutes(v1.Group("/auth"))

	// Dev-only smoke-test fixture endpoints (no auth; gated by env).
	// Registered BEFORE `protected` group so JWT middleware doesn't apply.
	if cfg.Env == "development" {
		dev := v1.Group("/dev")
		dev.Post("/smoke/seed", func(c fiber.Ctx) error {
			if err := seed.Run(db, cfg.Env); err != nil {
				return c.Status(500).JSON(fiber.Map{"status": "error", "message": err.Error()})
			}
			return c.JSON(fiber.Map{"status": "success", "message": "smoke fixtures ready"})
		})
		dev.Post("/smoke/cleanup", func(c fiber.Ctx) error {
			if err := seed.CleanupSmokeData(db); err != nil {
				return c.Status(500).JSON(fiber.Map{"status": "error", "message": err.Error()})
			}
			return c.JSON(fiber.Map{"status": "success", "message": "smoke fixtures removed"})
		})
		dev.Get("/smoke/fixtures", func(c fiber.Ctx) error {
			fixtures, err := seed.ListSmokeFixtures(db)
			if err != nil {
				return c.Status(500).JSON(fiber.Map{"status": "error", "message": err.Error()})
			}
			return c.JSON(fiber.Map{"status": "success", "data": fixtures})
		})
	}

	// Protected routes
	protected := v1.Group("", middleware.JWTProtected(cfg))

	// Protected auth routes
	authHandler.RegisterProtectedRoutes(protected.Group("/auth"))

	// Admin-only routes
	admin := protected.Group("", middleware.RequireRole(role.Admin))
	aptHandler.RegisterRoutes(admin.Group("/apartments"))
	bankHandler.RegisterRoutes(admin.Group("/apartments/:id/bank-accounts"))
	presetHandler := apartment.NewPresetHandler()
	presetHandler.RegisterRoutes(admin.Group("/apartments/:id/manual-line-item-presets"))
	roomHandler.RegisterRoutes(admin.Group("/apartments/:id/rooms"))
	tenantHandler.RegisterRoutes(admin.Group("/tenants"))
	contractHandler.RegisterRoutes(admin.Group("/contracts"))
	meterHandler.RegisterRoutes(admin.Group("/apartments/:apartmentId/meter-readings"))
	bcHandler.RegisterRoutes(admin.Group("/apartments/:id/billing-configs"))
	moveOutHandler.RegisterRoutes(admin.Group("/move-out-notices"))
	billHandler.RegisterRoutes(admin.Group("/bills"))
	reconHandler.RegisterRoutes(admin.Group("/billing-reconciliation"))

	// Graceful shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)

	go func() {
		addr := fmt.Sprintf(":%s", cfg.Port)
		slog.Info("server starting", "port", cfg.Port, "env", cfg.Env)
		if err := app.Listen(addr); err != nil {
			slog.Error("server error", "error", err)
			os.Exit(1)
		}
	}()

	<-quit
	slog.Info("shutting down server...")

	cancel()

	if err := app.Shutdown(); err != nil {
		slog.Error("server shutdown error", "error", err)
	}

	sqlDB, _ := db.DB()
	if sqlDB != nil {
		_ = sqlDB.Close()
	}

	slog.Info("server stopped")
}
