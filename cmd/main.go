package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"nana/internal/config"
	"nana/internal/database"
	"nana/internal/domain"
	"nana/internal/handler"
	"nana/internal/logger"
	"nana/internal/middleware"
	"nana/internal/repository"
	"nana/internal/seed"
	"nana/internal/service"

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
	userRepo := repository.NewUserRepository(db)
	authService := service.NewAuthService(userRepo, cfg)
	authHandler := handler.NewAuthHandler(authService, cfg)
	authService.StartTokenCleanup(ctx, 1*time.Hour)

	// Wire dependencies — Apartments
	aptRepo := repository.NewApartmentRepository(db)
	aptService := service.NewApartmentService(aptRepo)
	aptHandler := handler.NewApartmentHandler(aptService)

	// Wire dependencies — Bank Accounts
	bankRepo := repository.NewBankAccountRepository(db)
	bankService := service.NewBankAccountService(bankRepo, aptRepo, txManager)
	bankHandler := handler.NewBankAccountHandler(bankService)

	// Wire dependencies — Rooms
	roomRepo := repository.NewRoomRepository(db)
	roomService := service.NewRoomService(roomRepo, aptRepo)
	roomHandler := handler.NewRoomHandler(roomService)

	// Wire dependencies — Tenants
	tenantRepo := repository.NewTenantRepository(db)

	// Wire dependencies — Contracts
	contractRepo := repository.NewContractRepository(db)
	contractService := service.NewContractService(contractRepo, roomRepo, tenantRepo, txManager)
	contractHandler := handler.NewContractHandler(contractService)

	tenantService := service.NewTenantService(tenantRepo, contractRepo)
	tenantHandler := handler.NewTenantHandler(tenantService)

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

	// Protected routes
	protected := v1.Group("", middleware.JWTProtected(cfg))

	// Protected auth routes
	authHandler.RegisterProtectedRoutes(protected.Group("/auth"))

	// Admin-only routes
	admin := protected.Group("", middleware.RequireRole(domain.UserRoleAdmin))
	aptHandler.RegisterRoutes(admin.Group("/apartments"))
	bankHandler.RegisterRoutes(admin.Group("/apartments/:id/bank-accounts"))
	roomHandler.RegisterRoutes(admin.Group("/apartments/:id/rooms"))
	tenantHandler.RegisterRoutes(admin.Group("/tenants"))
	contractHandler.RegisterRoutes(admin.Group("/contracts"))

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
