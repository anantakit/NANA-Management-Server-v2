package config

import (
	"fmt"
	"os"
	"strconv"
)

type Config struct {
	Port                   string
	Env                    string
	DBHost                 string
	DBPort                 string
	DBUser                 string
	DBPassword             string
	DBName                 string
	DatabaseURL            string
	JWTSecret              string
	JWTExpiryHours         int
	RefreshTokenExpiryDays int
	CORSAllowedOrigin      string
}

func Load() *Config {
	cfg := &Config{
		Port:                   getEnv("PORT", "8080"),
		Env:                    getEnv("ENV", "development"),
		DBHost:                 getEnv("DB_HOST", "localhost"),
		DBPort:                 getEnv("DB_PORT", "5432"),
		DBUser:                 getEnv("DB_USER", "postgres"),
		DBPassword:             getEnv("DB_PASSWORD", "postgres"),
		DBName:                 getEnv("DB_NAME", "nana"),
		DatabaseURL:            os.Getenv("DATABASE_URL"),
		JWTSecret:              getEnv("JWT_SECRET", "change-me-in-production"),
		JWTExpiryHours:         getEnvInt("JWT_EXPIRY_HOURS", 1),
		RefreshTokenExpiryDays: getEnvInt("REFRESH_TOKEN_EXPIRY_DAYS", 7),
		CORSAllowedOrigin:      getEnv("CORS_ALLOWED_ORIGIN", "http://localhost:3000"),
	}
	return cfg
}

func (c *Config) DSN() string {
	if c.DatabaseURL != "" {
		return c.DatabaseURL
	}
	return fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=disable TimeZone=Asia/Bangkok",
		c.DBHost, c.DBPort, c.DBUser, c.DBPassword, c.DBName,
	)
}

func (c *Config) Validate() error {
	if c.Env == "production" {
		if c.JWTSecret == "" || c.JWTSecret == "change-me-in-production" {
			return fmt.Errorf("JWT_SECRET must be set in production")
		}
	}
	if c.JWTSecret == "" {
		return fmt.Errorf("JWT_SECRET is required")
	}
	return nil
}

func (c *Config) IsProduction() bool {
	return c.Env == "production"
}

func getEnv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func getEnvInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			return i
		}
	}
	return fallback
}
