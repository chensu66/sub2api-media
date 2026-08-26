package media

import (
	"errors"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Enabled        bool
	GateBaseURL    string
	PublicBaseURL  string
	Issuer         string
	Audience       string
	CallerID       string
	KeyID          string
	PrivateKeyPEM  string
	PrivateKeyJWK  string
	UsageAccountID int64
	RequestTimeout time.Duration
	PollInterval   time.Duration
}

func LoadConfig() (Config, error) {
	cfg := Config{
		Enabled:        envBool("SUB2API_MEDIA_ENABLED"),
		GateBaseURL:    envString("SUB2API_MEDIA_GATE_BASE_URL", "https://gate.ichen.su"),
		PublicBaseURL:  envString("SUB2API_MEDIA_PUBLIC_BASE_URL", "https://ai.ichen.su"),
		Issuer:         envString("SUB2API_MEDIA_SERVICE_ISSUER", "https://sub2api.local/internal/media"),
		Audience:       envString("SUB2API_MEDIA_SERVICE_AUDIENCE", "gate-media"),
		CallerID:       envString("SUB2API_MEDIA_CALLER_ID", "sub2api"),
		KeyID:          strings.TrimSpace(os.Getenv("SUB2API_MEDIA_SERVICE_KEY_ID")),
		PrivateKeyPEM:  strings.ReplaceAll(os.Getenv("SUB2API_MEDIA_SERVICE_PRIVATE_KEY_PEM"), `\n`, "\n"),
		PrivateKeyJWK:  strings.TrimSpace(os.Getenv("SUB2API_MEDIA_SERVICE_PRIVATE_JWK")),
		UsageAccountID: envInt64("SUB2API_MEDIA_USAGE_ACCOUNT_ID", 0),
		RequestTimeout: envDuration("SUB2API_MEDIA_REQUEST_TIMEOUT", 30*time.Second),
		PollInterval:   envDuration("SUB2API_MEDIA_POLL_INTERVAL", 3*time.Second),
	}
	cfg.GateBaseURL = strings.TrimRight(strings.TrimSpace(cfg.GateBaseURL), "/")
	cfg.PublicBaseURL = strings.TrimRight(strings.TrimSpace(cfg.PublicBaseURL), "/")
	if !cfg.Enabled {
		return cfg, nil
	}
	if cfg.GateBaseURL == "" || cfg.PublicBaseURL == "" || cfg.Issuer == "" || cfg.Audience == "" || cfg.CallerID == "" ||
		cfg.KeyID == "" || (strings.TrimSpace(cfg.PrivateKeyPEM) == "" && cfg.PrivateKeyJWK == "") || cfg.UsageAccountID <= 0 {
		return Config{}, errors.New("media platform is enabled but its service configuration is incomplete")
	}
	if cfg.RequestTimeout < time.Second || cfg.RequestTimeout > 5*time.Minute {
		return Config{}, errors.New("SUB2API_MEDIA_REQUEST_TIMEOUT must be between 1s and 5m")
	}
	if cfg.PollInterval < time.Second || cfg.PollInterval > time.Minute {
		return Config{}, errors.New("SUB2API_MEDIA_POLL_INTERVAL must be between 1s and 1m")
	}
	return cfg, nil
}

func envString(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}

func envBool(name string) bool {
	value, _ := strconv.ParseBool(strings.TrimSpace(os.Getenv(name)))
	return value
}

func envInt64(name string, fallback int64) int64 {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return fallback
	}
	return parsed
}

func envDuration(name string, fallback time.Duration) time.Duration {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return parsed
}
