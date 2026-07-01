// Package config loads bootstrap configuration from environment variables. In
// Phase 1 it also parses the initial route table from GATEWAY_ROUTES; later
// phases move routes into the config store and keep only secrets/ports here.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
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

	// TrustedProxies are networks whose X-Forwarded-For header is trusted for
	// client-IP resolution. Empty means trust none (XFF ignored).
	TrustedProxies []*net.IPNet

	// Upstream transport timeouts.
	UpstreamDialTimeout     time.Duration
	UpstreamResponseTimeout time.Duration

	LogLevel    string
	MetricsPath string

	// DBPath is the SQLite config-store file. Routes are seeded from GATEWAY_ROUTES
	// on first run, then the store is authoritative.
	DBPath string
	// ConfigPollInterval, when > 0, polls the store for config changes and
	// hot-reloads the data plane. 0 disables polling.
	ConfigPollInterval time.Duration

	// Admin (control plane). The admin API runs on a separate private listener and
	// starts only when AdminJWTSecret is set.
	AdminAddr      string
	AdminJWTSecret string
	AdminUser      string
	AdminPassword  string
	AdminTokenTTL  time.Duration
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

		UpstreamDialTimeout:     getDuration("GATEWAY_UPSTREAM_DIAL_TIMEOUT", 10*time.Second),
		UpstreamResponseTimeout: getDuration("GATEWAY_UPSTREAM_RESPONSE_TIMEOUT", 30*time.Second),

		DBPath:             getString("GATEWAY_DB_PATH", "./gateway.db"),
		ConfigPollInterval: getDuration("GATEWAY_CONFIG_POLL_INTERVAL", 0),

		AdminAddr:      getString("GATEWAY_ADMIN_ADDR", "127.0.0.1:9000"),
		AdminJWTSecret: getString("GATEWAY_ADMIN_JWT_SECRET", ""),
		AdminUser:      getString("GATEWAY_ADMIN_USER", ""),
		AdminPassword:  getString("GATEWAY_ADMIN_PASSWORD", ""),
		AdminTokenTTL:  getDuration("GATEWAY_ADMIN_TOKEN_TTL", 30*time.Minute),
	}
	trusted, err := parseTrustedProxies(os.Getenv("GATEWAY_TRUSTED_PROXIES"))
	if err != nil {
		return nil, fmt.Errorf("GATEWAY_TRUSTED_PROXIES: %w", err)
	}
	c.TrustedProxies = trusted

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
	names := make(map[string]struct{})
	for i, r := range c.Routes {
		switch {
		case r.PathPrefix == "":
			return fmt.Errorf("routes[%d] (%q): path_prefix is required", i, r.Name)
		case !strings.HasPrefix(r.PathPrefix, "/"):
			return fmt.Errorf("routes[%d] (%q): path_prefix must start with '/'", i, r.Name)
		case r.Upstream == "":
			return fmt.Errorf("routes[%d] (%q): upstream is required", i, r.Name)
		}
		// W6: route names must be unique (used as metric labels / log fields).
		if r.Name != "" {
			if _, dup := names[r.Name]; dup {
				return fmt.Errorf("routes[%d]: duplicate route name %q", i, r.Name)
			}
			names[r.Name] = struct{}{}
		}
		// W7: a route must not be shadowed by an earlier one with the same prefix
		// and overlapping methods, which would make it silently unreachable.
		for j := 0; j < i; j++ {
			if c.Routes[j].PathPrefix == r.PathPrefix && methodsOverlap(c.Routes[j].Methods, r.Methods) {
				return fmt.Errorf("routes[%d] (%q): path_prefix %q with overlapping methods is shadowed by routes[%d] (%q)",
					i, r.Name, r.PathPrefix, j, c.Routes[j].Name)
			}
		}
	}
	return nil
}

// methodsOverlap reports whether two routes could both match a request. An empty
// method list means "any method", so it overlaps with everything.
func methodsOverlap(a, b []string) bool {
	if len(a) == 0 || len(b) == 0 {
		return true
	}
	for _, x := range a {
		for _, y := range b {
			if strings.EqualFold(x, y) {
				return true
			}
		}
	}
	return false
}

// parseTrustedProxies parses a comma-separated list of CIDRs or bare IPs.
func parseTrustedProxies(raw string) ([]*net.IPNet, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var nets []*net.IPNet
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if !strings.Contains(part, "/") {
			if ip := net.ParseIP(part); ip != nil {
				if ip.To4() != nil {
					part += "/32"
				} else {
					part += "/128"
				}
			}
		}
		_, n, err := net.ParseCIDR(part)
		if err != nil {
			return nil, fmt.Errorf("invalid trusted proxy %q: %w", part, err)
		}
		nets = append(nets, n)
	}
	return nets, nil
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
