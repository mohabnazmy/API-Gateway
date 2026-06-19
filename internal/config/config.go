// Package config loads bootstrap configuration from environment variables. In
// Phase 1 it also parses the initial route table from GATEWAY_ROUTES; later
// phases move routes into the config store and keep only secrets/ports here.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/joho/godotenv"

	"github.com/mohabnazmy/API-Gateway/internal/model"
)

// DefaultEnvFile is the env file loaded when no path is given.
const DefaultEnvFile = ".env"

// LoadDotEnv loads variables from a .env file into the process environment so
// that Load can read them. Variables already set in the environment take
// precedence (the file only fills in what's unset). A missing file is not an
// error — env vars may be supplied directly (e.g. in production).
//
// This is the seam for additional config sources: a YAML loader will live
// alongside this in a later phase.
func LoadDotEnv(path string) error {
	if path == "" {
		path = DefaultEnvFile
	}
	if _, err := os.Stat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return godotenv.Load(path)
}

// Config is the resolved bootstrap configuration.
type Config struct {
	ProxyAddr       string
	ReadTimeout     time.Duration
	WriteTimeout    time.Duration
	IdleTimeout     time.Duration
	ShutdownTimeout time.Duration

	Routes []model.Route

	JWTSecret string
	APIKeys   map[string]struct{}

	LogLevel    string
	MetricsPath string
}

// Load reads configuration from the environment, applying defaults. The route
// table comes from GATEWAY_ROUTES (a JSON array). A global default rate limit
// (GATEWAY_RATE_LIMIT_RPS/BURST) is applied to routes that omit one.
func Load() (*Config, error) {
	c := &Config{
		ProxyAddr:       getString("GATEWAY_PROXY_ADDR", ":8080"),
		ReadTimeout:     getDuration("GATEWAY_READ_TIMEOUT", 15*time.Second),
		WriteTimeout:    getDuration("GATEWAY_WRITE_TIMEOUT", 30*time.Second),
		IdleTimeout:     getDuration("GATEWAY_IDLE_TIMEOUT", 60*time.Second),
		ShutdownTimeout: getDuration("GATEWAY_SHUTDOWN_TIMEOUT", 15*time.Second),
		JWTSecret:       getString("GATEWAY_JWT_SECRET", ""),
		LogLevel:        getString("GATEWAY_LOG_LEVEL", "info"),
		MetricsPath:     getString("GATEWAY_METRICS_PATH", "/metrics"),
	}
	c.APIKeys = parseAPIKeys(os.Getenv("GATEWAY_API_KEYS"))

	routes, err := parseRoutes(os.Getenv("GATEWAY_ROUTES"))
	if err != nil {
		return nil, fmt.Errorf("GATEWAY_ROUTES: %w", err)
	}
	defaultLimit := model.RateLimitPolicy{
		Algorithm: "token_bucket",
		RPS:       getFloat("GATEWAY_RATE_LIMIT_RPS", 100),
		Burst:     getInt("GATEWAY_RATE_LIMIT_BURST", 200),
	}
	for i := range routes {
		if !routes[i].RateLimit.Enabled() {
			routes[i].RateLimit = defaultLimit
		}
	}
	c.Routes = routes

	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Config) validate() error {
	for i, r := range c.Routes {
		switch {
		case r.PathPrefix == "":
			return fmt.Errorf("routes[%d] (%q): path_prefix is required", i, r.Name)
		case !strings.HasPrefix(r.PathPrefix, "/"):
			return fmt.Errorf("routes[%d] (%q): path_prefix must start with '/'", i, r.Name)
		case r.Upstream == "":
			return fmt.Errorf("routes[%d] (%q): upstream is required", i, r.Name)
		}
		if r.Auth.RequireAuth && c.JWTSecret == "" && len(c.APIKeys) == 0 {
			return fmt.Errorf("routes[%d] (%q): require_auth is set but no GATEWAY_JWT_SECRET or GATEWAY_API_KEYS configured", i, r.Name)
		}
	}
	return nil
}

func parseRoutes(raw string) ([]model.Route, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var routes []model.Route
	if err := json.Unmarshal([]byte(raw), &routes); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}
	return routes, nil
}

func parseAPIKeys(raw string) map[string]struct{} {
	keys := make(map[string]struct{})
	for _, k := range strings.Split(raw, ",") {
		if k = strings.TrimSpace(k); k != "" {
			keys[k] = struct{}{}
		}
	}
	return keys
}

func getString(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getInt(key string, def int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return def
}

func getFloat(key string, def float64) float64 {
	if v := os.Getenv(key); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

func getDuration(key string, def time.Duration) time.Duration {
	if v := os.Getenv(key); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
