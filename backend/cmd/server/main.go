package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/cors"
	"github.com/gofiber/fiber/v2/middleware/limiter"
	"github.com/gofiber/fiber/v2/middleware/logger"
	"github.com/gofiber/fiber/v2/middleware/recover"

	"github.com/enterprise-kb/backend/internal/bot"
	"github.com/enterprise-kb/backend/internal/config"
	"github.com/enterprise-kb/backend/internal/database"
	"github.com/enterprise-kb/backend/internal/handlers"
	"github.com/enterprise-kb/backend/internal/llm"
	"github.com/enterprise-kb/backend/internal/middleware"
	"github.com/enterprise-kb/backend/internal/models"
	"github.com/enterprise-kb/backend/internal/rerank"
	"github.com/enterprise-kb/backend/internal/search"
	"github.com/enterprise-kb/backend/internal/services"
)

func main() {
	// Root context cancelled on SIGINT/SIGTERM for graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cfg := config.Load()

	if err := os.MkdirAll(cfg.Storage.DocsPath, 0755); err != nil {
		log.Fatalf("failed to create docs storage: %v", err)
	}

	db := database.Connect(cfg.Database.DSN)
	if err := models.AutoMigrate(db); err != nil {
		log.Fatalf("failed to migrate database: %v", err)
	}

	searchSvc := search.NewService(cfg.Meili)
	if err := searchSvc.InitIndexes(); err != nil {
		log.Printf("warning: search init: %v", err)
	}

	embedder := llm.NewEmbedding(cfg.AIConfig)
	answerer := llm.NewAnswer(cfg.AIConfig)
	if embedder.Enabled() {
		log.Printf("embedding enabled: %s/%s", cfg.AIConfig.EmbeddingProvider, cfg.AIConfig.EmbeddingModel)
	} else {
		log.Printf("embedding disabled -> BM25 only")
	}
	if answerer.Enabled() {
		log.Printf("llm enabled: %s/%s", cfg.AIConfig.LLMProvider, cfg.AIConfig.LLMModel)
	} else {
		log.Printf("llm disabled -> offline extractive answers")
	}

	authSvc := services.NewAuthService(cfg.JWT)
	authSvc.Configure(cfg.Auth.RegisterEnabled, cfg.Auth.MinPasswordLen, cfg.Auth.LoginMaxFailures, cfg.Auth.LoginLockMinutes)

	ingestSvc := services.NewIngestService(cfg.Storage.DocsPath, searchSvc, embedder)
	ingestSvc.Configure(cfg.Embedding.BatchSize, cfg.Embedding.MaxConcurrent)

	chatSvc := services.NewChatService(searchSvc, embedder, answerer, rerank.New(cfg.Rerank), cfg.MissThreshold, cfg.RerankMissThreshold)
	chatSvc.SetWebSearch(services.NewWebSearchClient(
		cfg.WebSearch.Enabled, cfg.WebSearch.APIKey, cfg.WebSearch.BaseURL, cfg.WebSearch.MaxCount,
	))
	adminSvc := services.NewAdminService()

	if cfg.SeedData {
		if err := services.Seed(cfg, ingestSvc); err != nil {
			log.Printf("[seed] error: %v", err)
		}
	}

	authHandler := handlers.NewAuthHandler(authSvc)
	kbHandler := handlers.NewKBHandler(searchSvc)
	docHandler := handlers.NewDocHandler(ingestSvc)
	chatHandler := handlers.NewChatHandler(chatSvc)
	adminHandler := handlers.NewAdminHandler(adminSvc, cfg)
	adminHandler.SetIngest(ingestSvc)
	botHandler := handlers.NewBotHandler()

	// IM chatbot (Feishu / WeCom) - runs in background goroutine, no-op if
	// unconfigured. It shares the root context so it stops during shutdown.
	switch cfg.Bot.Platform {
	case "wecom":
		wc := bot.NewWeComService(cfg.Bot, chatSvc)
		go func() {
			if err := wc.Start(ctx); err != nil {
				log.Printf("[wecom] fatal: %v", err)
			}
		}()
	default: // "feishu" or ""
		botSvc := bot.NewService(cfg.Bot, chatSvc)
		go func() {
			if err := botSvc.Start(ctx); err != nil {
				log.Printf("[bot] fatal: %v", err)
			}
		}()
	}

	// audit log cleanup: retention enforced on startup + every 24h
	go func() {
		runCleanup := func() {
			n, err := services.CleanupAudit(cfg.Audit.RetentionDays)
			if err != nil {
				log.Printf("[audit] cleanup error: %v", err)
			} else if n > 0 {
				log.Printf("[audit] cleaned %d old audit logs", n)
			}
		}
		runCleanup()
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				runCleanup()
			}
		}
	}()

	app := fiber.New(fiber.Config{
		ReadTimeout:  cfg.Server.ReadTimeout,
		WriteTimeout: cfg.Server.WriteTimeout,
		// nginx allows 50m; Fiber's default BodyLimit is 4MB which would 413
		// large uploads before the handler (file cap is enforced in SaveFile).
		BodyLimit: 25 * 1024 * 1024,
	})

	app.Use(recover.New())
	app.Use(logger.New())
	if cfg.Server.CORSOrigins != "" {
		app.Use(cors.New(cors.Config{
			AllowOrigins: cfg.Server.CORSOrigins,
			AllowMethods: "GET,POST,PUT,DELETE,OPTIONS",
			AllowHeaders: "Origin, Content-Type, Accept, Authorization",
		}))
	}

	api := app.Group("/api")

	api.Get("/health", func(c *fiber.Ctx) error {
		return c.JSON(fiber.Map{"status": "ok"})
	})

	loginLimiter := limiter.New(limiter.Config{
		Max:        10,
		Expiration: time.Minute,
		KeyGenerator: func(c *fiber.Ctx) string {
			return c.IP()
		},
		LimitReached: func(c *fiber.Ctx) error {
			return c.Status(429).JSON(fiber.Map{"error": "too many attempts, please try again later"})
		},
	})
	api.Post("/auth/register", authHandler.Register)
	api.Post("/auth/login", loginLimiter, authHandler.Login)

	auth := api.Group("", middleware.AuthRequired(cfg.JWT))
	auth.Get("/auth/me", authHandler.Me)

	// organization
	auth.Get("/members", authHandler.ListMembers)
	auth.Post("/members", middleware.RequireAdmin(), authHandler.CreateMember)
	auth.Put("/members/:id", middleware.RequireAdmin(), authHandler.UpdateMember)
	auth.Delete("/members/:id", middleware.RequireAdmin(), authHandler.DeleteMember)

	// knowledge bases
	auth.Get("/kbs", kbHandler.List)
	auth.Post("/kbs", middleware.RequireAdmin(), kbHandler.Create)
	auth.Put("/kbs/:id", middleware.RequireAdmin(), kbHandler.Update)
	auth.Delete("/kbs/:id", middleware.RequireAdmin(), kbHandler.Delete)
	auth.Get("/kbs/:id", kbHandler.Get)

	// documents
	auth.Get("/kbs/:kbId/docs", docHandler.List)
	auth.Post("/kbs/:kbId/docs", docHandler.Upload)
	auth.Delete("/docs/:docId", docHandler.Delete)
	auth.Post("/docs/:docId/reprocess", docHandler.Reprocess)
	auth.Get("/docs/:docId/chunks", docHandler.Chunks)

	// chat
	auth.Post("/chat/ask", chatHandler.Ask)
	auth.Get("/chat/sessions", chatHandler.Sessions)
	auth.Get("/chat/sessions/:sessionId/messages", chatHandler.Messages)
	auth.Delete("/chat/sessions/:sessionId", chatHandler.DeleteSession)
	auth.Post("/chat/messages/:messageId/feedback", chatHandler.Feedback)

	// contact admins (any logged-in user, for missed-answer guidance)
	auth.Get("/contacts/admins", adminHandler.Contact)

	// admin console
	admin := auth.Group("/admin", middleware.RequireAdmin())
	admin.Get("/stats", adminHandler.Stats)
	admin.Get("/audit", adminHandler.Audit)
	admin.Get("/gaps", adminHandler.Gaps)
	admin.Get("/feedback", adminHandler.Feedback)
	admin.Get("/sessions", adminHandler.Sessions)
	admin.Get("/sessions/:sessionId/messages", adminHandler.SessionMessages)
	// reconcile PostgreSQL <-> Meilisearch after a crashed processing pass
	admin.Post("/reindex", adminHandler.Reindex)

	// bot bindings (admin)
	admin.Get("/bot/bindings", botHandler.ListBindings)
	admin.Post("/bot/bindings/decide", botHandler.Decide)
	admin.Delete("/bot/bindings/:id", botHandler.Unbind)

	go func() {
		log.Printf("server starting on port %s", cfg.Server.Port)
		if err := app.Listen(":" + cfg.Server.Port); err != nil && ctx.Err() == nil {
			log.Fatalf("server failed: %v", err)
		}
	}()

	<-ctx.Done()
	log.Println("shutting down (signal received)...")
	stop()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := app.ShutdownWithContext(shutdownCtx); err != nil {
		log.Printf("shutdown error: %v", err)
	}
	log.Println("server stopped")
}