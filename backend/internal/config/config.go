package config

import (
	"os"
	"strconv"
	"time"
)

type Config struct {
	Server        ServerConfig
	Database      DatabaseConfig
	JWT           JWTConfig
	Meili         MeiliConfig
	Storage       StorageConfig
	AIConfig      AIConfig
	Bot           BotConfig
	WebSearch     WebSearchConfig
	Auth          AuthConfig
	Audit         AuditConfig
	Embedding     EmbeddingConfig
	Rerank        RerankConfig
	Contact       ContactConfig
	SeedData      bool
	ServerURL     string
	MissThreshold float64
	// RerankMissThreshold gates on the cross-encoder rerank score (used instead
	// of MissThreshold when rerank is enabled). Below it the answer is a miss.
	RerankMissThreshold float64
}

type ServerConfig struct {
	Port         string
	ReadTimeout  time.Duration
	WriteTimeout time.Duration
	// CORSOrigins is a comma-separated allowlist. empty = same-origin only
	// (reverse proxy in front), no CORS middleware.
	CORSOrigins string
}

type DatabaseConfig struct {
	DSN string
}

type JWTConfig struct {
	Secret     string
	Expiration time.Duration
}

type MeiliConfig struct {
	Host   string
	APIKey string
}

type StorageConfig struct {
	DocsPath string
}

// AIConfig holds pluggable AI settings.
// Empty Provider = offline mode (BM25 + extractive answers).
type AIConfig struct {
	// Embedding (vector retrieval). empty = disabled
	EmbeddingProvider string
	EmbeddingAPIKey   string
	EmbeddingModel    string
	EmbeddingBaseURL  string
	// LLM (answer generation). empty = offline extractive answers
	LLMProvider string
	LLMAPIKey   string
	LLMModel    string
	LLMBaseURL  string
}

// BotConfig holds IM chatbot integration settings.
// Empty Platform/AppID/AppSecret = chatbot disabled (no effect on the rest of the app).
type BotConfig struct {
	Platform       string // "feishu" | "wecom" | ""
	AppID          string // feishu: app id; wecom: bot id
	AppSecret      string // feishu: app secret; wecom: long-connection secret
	MessageTimeout time.Duration
}

// WebSearchConfig enables optional web search for unanswered questions.
type WebSearchConfig struct {
	Enabled  bool
	APIKey   string
	BaseURL  string
	MaxCount int
}

// AuthConfig hardens the auth surface.
type AuthConfig struct {
	// RegisterEnabled gates the public /auth/register endpoint.
	// Enterprise deployments typically set AUTH_ALLOW_REGISTER=false and
	// provision users via the admin member management instead.
	RegisterEnabled bool
	// LoginMaxFailures + LoginLockMinutes implement per-email lockout after
	// repeated failed logins.
	LoginMaxFailures int
	LoginLockMinutes int
	// MinPasswordLen is the minimum accepted password length.
	MinPasswordLen int
}

// AuditConfig controls audit log retention (cleanup runs on a 24h ticker).
type AuditConfig struct {
	RetentionDays int
}

// EmbeddingConfig tunes embedding API usage (batching + concurrency).
type EmbeddingConfig struct {
	BatchSize     int
	MaxConcurrent int
}

// RerankConfig optionally re-ranks hybrid retrieval candidates with a
// cross-encoder before they are fed to the LLM. Empty Provider = disabled
// (the existing RRF-merged results are used as-is, fully backward compatible).
// OpenAI-compatible providers (e.g. SiliconFlow bge-reranker-v2-m3) and Cohere
// are supported.
type RerankConfig struct {
	Enabled  bool
	Provider string // "openai" (OpenAI-compatible: SiliconFlow/Jina/...) | "cohere" | "" (disabled)
	APIKey   string
	Model    string
	BaseURL  string
	TopN     int // keep top-N after rerank (0 = keep all)
}

// ContactConfig is an optional, admin-configured entry point shown on the
// chat page when an answer is not found in the knowledge base. A link is more
// reliable than mailto on mobile clients.
type ContactConfig struct {
	Link string // e.g. https://your-helpdesk.example.com  (empty = hidden)
	Text string // label shown on the button (empty = "联系管理员")
}

func Load() *Config {
	return &Config{
		Server: ServerConfig{
			Port:         getEnv("SERVER_PORT", "8080"),
			ReadTimeout:  120 * time.Second,
			WriteTimeout: 300 * time.Second,
			CORSOrigins:  getEnv("CORS_ORIGINS", "*"),
		},
		Database: DatabaseConfig{
			DSN: getEnv("DATABASE_DSN", "postgres://postgres:postgres@localhost:5432/kb_hub?sslmode=disable"),
		},
		JWT: JWTConfig{
			Secret:     getEnv("JWT_SECRET", "change-me-in-production"),
			Expiration: 24 * time.Hour,
		},
		Meili: MeiliConfig{
			Host:   getEnv("MEILI_HOST", "http://localhost:7700"),
			APIKey: getEnv("MEILI_API_KEY", ""),
		},
		Storage: StorageConfig{
			DocsPath: getEnv("DOCS_PATH", "./data/docs"),
		},
		AIConfig: AIConfig{
			EmbeddingProvider: getEnv("EMBEDDING_PROVIDER", ""),
			EmbeddingAPIKey:   getEnv("EMBEDDING_API_KEY", ""),
			EmbeddingModel:    getEnv("EMBEDDING_MODEL", "BAAI/bge-m3"),
			EmbeddingBaseURL:  getEnv("EMBEDDING_BASE_URL", "https://api.siliconflow.cn/v1"),
			LLMProvider:       getEnv("LLM_PROVIDER", ""),
			LLMAPIKey:         getEnv("LLM_API_KEY", ""),
			LLMModel:          getEnv("LLM_MODEL", "deepseek-chat"),
			LLMBaseURL:        getEnv("LLM_BASE_URL", "https://api.deepseek.com/v1"),
		},
		SeedData:  getEnvBool("SEED_DATA", true),
		ServerURL: getEnv("SERVER_URL", "http://localhost:8080"),
		// MissThreshold is the minimum retrieval relevance (max of BM25
		// ranking score and vector cosine similarity) for a question to be
		// considered answered. Below it, the answer is treated as a miss so
		// off-topic questions don't get hallucinated from weak chunks.
		MissThreshold:       getEnvFloat("MISS_THRESHOLD", 0.25),
		RerankMissThreshold: getEnvFloat("RERANK_MISS_THRESHOLD", 0.1),
		Auth: AuthConfig{
			RegisterEnabled:  getEnvBool("AUTH_ALLOW_REGISTER", true),
			LoginMaxFailures: getEnvInt("AUTH_LOGIN_MAX_FAILURES", 5),
			LoginLockMinutes: getEnvInt("AUTH_LOGIN_LOCK_MINUTES", 15),
			MinPasswordLen:   getEnvInt("AUTH_MIN_PASSWORD_LEN", 8),
		},
		Audit: AuditConfig{
			RetentionDays: getEnvInt("AUDIT_RETENTION_DAYS", 180),
		},
		Embedding: EmbeddingConfig{
			BatchSize:     getEnvInt("EMBED_BATCH_SIZE", 32),
			MaxConcurrent: getEnvInt("EMBED_MAX_CONCURRENCY", 4),
		},
		Rerank: RerankConfig{
			Enabled:  getEnvBool("RERANK_ENABLED", false),
			Provider: getEnv("RERANK_PROVIDER", ""),
			APIKey:   getEnv("RERANK_API_KEY", ""),
			Model:    getEnv("RERANK_MODEL", "BAAI/bge-reranker-v2-m3"),
			BaseURL:  getEnv("RERANK_BASE_URL", "https://api.siliconflow.cn/v1"),
			TopN:     getEnvInt("RERANK_TOP_N", 4),
		},
		Contact: ContactConfig{
			Link: getEnv("CONTACT_LINK", ""),
			Text: getEnv("CONTACT_TEXT", ""),
		},
		WebSearch: WebSearchConfig{
			Enabled:  getEnvBool("WEB_SEARCH_ENABLED", false),
			APIKey:   getEnv("WEB_SEARCH_API_KEY", ""),
			BaseURL:  getEnv("WEB_SEARCH_BASE_URL", "https://api.tavily.com/search"),
			MaxCount: getEnvInt("WEB_SEARCH_MAX_RESULTS", 5),
		},
		Bot: BotConfig{
			Platform:       getEnv("BOT_PLATFORM", ""),
			AppID:          getEnv("FEISHU_APP_ID", getEnv("WECOM_BOT_ID", "")),
			AppSecret:      getEnv("FEISHU_APP_SECRET", getEnv("WECOM_BOT_SECRET", "")),
			MessageTimeout: 120 * time.Second,
		},
	}
}

func getEnv(key, fallback string) string {
	if val := os.Getenv(key); val != "" {
		return val
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if val := os.Getenv(key); val != "" {
		if i, err := strconv.Atoi(val); err == nil {
			return i
		}
	}
	return fallback
}

func getEnvBool(key string, fallback bool) bool {
	if val := os.Getenv(key); val != "" {
		if b, err := strconv.ParseBool(val); err == nil {
			return b
		}
	}
	return fallback
}

func getEnvFloat(key string, fallback float64) float64 {
	if val := os.Getenv(key); val != "" {
		if f, err := strconv.ParseFloat(val, 64); err == nil {
			return f
		}
	}
	return fallback
}
