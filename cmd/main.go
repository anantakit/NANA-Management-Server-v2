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
	"nana/internal/billing/monthly"
	"nana/internal/billing/settlement"
	"nana/internal/billdelivery"
	"nana/internal/billingconfig"
	"nana/internal/billingreconciliation"
	"nana/internal/contract"
	"nana/internal/meterreading"
	"nana/internal/moveout"
	"nana/internal/payment"
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

	// Wire dependencies — Billing root (repos must precede meterService since
	// Phase 5 Reading Recovery wires billing.RecoveryAdapter into meterService).
	billRepo := billing.NewBillingRepository(db)
	billAuditRepo := billing.NewBillAuditRepository(db)

	// Phase 5 Reading Recovery — RecoveryAdapter still ships for the legacy
	// AttachAdjustmentLine surface (future bill-creation auto-apply paths).
	// It is intentionally NOT injected into meterService anymore — Phase 7
	// (Split Meter Truth from Financial Truth) moved Adjustment Application
	// to the bill side (UpdateMonthlyDraft.applied_corrections). Keep the
	// constructor call so the adapter struct stays warm against
	// re-introduction in monthly-batch-side auto-apply.
	_ = billing.NewRecoveryAdapter(billRepo, billAuditRepo)

	// Phase 7 BillingApplicationChecker — derives baseline-correction
	// applied state from inverse-FK presence on bill_line_items.
	billRecoveryAppliedChecker := billing.NewRecoveryAppliedChecker(billRepo)

	// Meter Reading service (needs moveOutRepo for MoveOutChecker port,
	// billRecoveryAppliedChecker for BillingApplicationChecker port).
	meterService := meterreading.NewMeterReadingService(meterRepo, roomRepo, contractRepo, moveOutRepo, billRecoveryAppliedChecker, txManager)
	meterHandler := meterreading.NewMeterReadingHandler(meterService)

	// Wire dependencies — Payment Destination Routing
	routingRuleRepo := apartment.NewPaymentDestinationRuleRepository(db)
	routingService := apartment.NewPaymentRoutingService(routingRuleRepo, bankRepo, aptRepo)
	routingHandler := apartment.NewPaymentRoutingHandler(routingService)

	// Wire dependencies — Billing service (root repos constructed above).
	// Billing handler also takes the meterService for the Phase 7 convenience
	// route GET /:id/pending-baseline-corrections (bill→contract→room→meter).
	billService := billing.NewBillingService(billRepo, billAuditRepo, contractRepo, meterRepo, bcRepo, routingService, txManager)
	billHandler := billing.NewBillingHandler(billService, billRepo, contractRepo)

	// Wire dependencies — Monthly billing workflow (W2 batch mechanics).
	// MonthlyAdapter satisfies monthly.BillStore + monthly.AuditStore over
	// the shared bills + bill_audit_log tables. Batch persistence
	// (bill_generation_batches + bill_generation_batch_items) is owned by
	// monthly.BatchRepository directly — no adapter indirection needed
	// because the impl lives in the monthly package itself.
	// Meter + MoveOut repos satisfy monthly's narrower query ports via
	// structural typing (same method shapes as billing's existing ports).
	monthlyAdapter := billing.NewMonthlyAdapter(billRepo, billAuditRepo)
	monthlyBatchRepo := monthly.NewBatchRepository(db)
	monthlyService := monthly.NewService(monthlyAdapter, monthlyAdapter, monthlyBatchRepo, meterRepo, moveOutRepo, txManager)
	monthlyHandler := monthly.NewHandler(monthlyService)

	// Wire dependencies — Settlement billing workflow (W4 scaffold).
	// SettlementAdapter satisfies settlement.BillStore + settlement.AuditStore.
	// Cross-feature Source ports (Contract/Meter/BillingConfig/MoveOut/
	// PaymentRouting) are satisfied structurally by the existing
	// repos/services that already serve billing root's *Querier ports —
	// no extra adapter struct is needed for those. settlementService is
	// inert in commit 2 of the W4 plan (no methods yet); workflow methods
	// migrate from billing root in commits 3-5, handler routes in commit 6.
	// moveOutService below STILL points at billService — the swap to
	// settlementService lands in commit 5 once the moveout-port methods
	// (GenerateSettlement / CorrectSettlement / etc.) have migrated.
	settlementAdapter := billing.NewSettlementAdapter(billRepo, billAuditRepo)
	settlementService := settlement.NewService(settlementAdapter, settlementAdapter, contractRepo, meterRepo, bcRepo, moveOutRepo, routingService, txManager)
	settlementHandler := settlement.NewHandler(settlementService)

	// Wire Move-Out service. settlementService satisfies both
	// moveout.BillingCommander (Generate / Regenerate / Finalize / Void /
	// Correct) and moveout.BillingQuerier (PreviewSettlementForNotice) —
	// compile-time check pinned in settlement/adapter_check.go. W4 commit 3
	// swapped from billService → settlementService when the settlement
	// workflow methods migrated.
	moveOutService := moveout.NewMoveOutService(moveOutRepo, contractRepo, contractRepo, roomRepo, meterService, settlementService, settlementService, txManager)
	moveOutHandler := moveout.NewMoveOutHandler(moveOutService)

	// Wire dependencies — Payment recording
	billPaymentAdapter := billing.NewPaymentAdapter(billRepo, billAuditRepo)
	paymentRepo := payment.NewPaymentRepository(db)
	paymentService := payment.NewPaymentService(paymentRepo, billPaymentAdapter, txManager)
	paymentHandler := payment.NewPaymentHandler(paymentService)

	// Wire dependencies — Bill Delivery (event log, v1 manual LINE delivery)
	deliveryRepo := billdelivery.NewDeliveryRepository(db)
	deliveryService := billdelivery.NewDeliveryService(deliveryRepo, billRepo)
	deliveryHandler := billdelivery.NewDeliveryHandler(deliveryService)

	// Wire dependencies — Billing Reconciliation (Phase 1A: read-only audit)
	// Adapter satisfies BillsQuerier + BillsCommander so billingreconciliation
	// stays consumer-defined and billing's main service surface keeps no
	// reconciliation-shaped methods (mirrors PaymentAdapter).
	reconRepo := billingreconciliation.NewRepository(db)
	reconAdapter := billing.NewReconciliationAdapter(billRepo, contractRepo, meterRepo, billService)
	reconService := billingreconciliation.NewService(reconRepo, meterRepo, moveOutRepo, reconAdapter, reconAdapter)
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
		dev.Post("/smoke/reset-base-bills", func(c fiber.Ctx) error {
			if err := seed.ResetPaymentSmokeBills(db); err != nil {
				return c.Status(500).JSON(fiber.Map{"status": "error", "message": err.Error()})
			}
			return c.JSON(fiber.Map{"status": "success", "message": "base monthly bills reset"})
		})
		dev.Post("/smoke/reset-recovery", func(c fiber.Ctx) error {
			if err := seed.ResetRecoverySmoke(db); err != nil {
				return c.Status(500).JSON(fiber.Map{"status": "error", "message": err.Error()})
			}
			return c.JSON(fiber.Map{"status": "success", "message": "recovery smoke fixture reset"})
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
	routingHandler.RegisterRoutes(admin.Group("/apartments/:id/payment-destination-rules"))
	presetHandler := apartment.NewPresetHandler()
	presetHandler.RegisterRoutes(admin.Group("/apartments/:id/manual-line-item-presets"))
	roomHandler.RegisterRoutes(admin.Group("/apartments/:id/rooms"))
	tenantHandler.RegisterRoutes(admin.Group("/tenants"))
	contractHandler.RegisterRoutes(admin.Group("/contracts"))
	meterHandler.RegisterRoutes(admin.Group("/apartments/:apartmentId/meter-readings"))
	bcHandler.RegisterRoutes(admin.Group("/apartments/:id/billing-configs"))
	moveOutHandler.RegisterRoutes(admin.Group("/move-out-notices"))
	billGroup := admin.Group("/bills")
	// monthly MUST register FIRST so its literal segments (/batches/*,
	// /preflight, /batch-monthly, /finalize-all-by-month) beat /:id at
	// the radix match level. Preserves the original "batches before /:id"
	// registration order from the pre-extraction handler.
	monthlyHandler.RegisterRoutes(billGroup)
	// settlement registers BETWEEN monthly and bill. Empty in W4 commit 2
	// — pins the registration slot ahead of route migration in commit 6 so
	// /settlement + /settlement/preview literals win over /:id and the
	// PATCH /:id/settlement-draft slot is reserved without behavior drift.
	settlementHandler.RegisterRoutes(billGroup)
	billHandler.RegisterRoutes(billGroup)
	paymentHandler.RegisterRoutes(billGroup)
	deliveryHandler.RegisterRoutes(admin.Group("/bill-deliveries"))
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
