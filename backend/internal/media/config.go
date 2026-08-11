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
	Issuer         string
	Audience       string
	CallerID       string
	KeyID          string
	PrivateKeyPEM  string
	PrivateKeyJWK  string
	RequestTimeout time.Duration
	PollInterval   time.Duration
}

func LoadConfig() (Config, error) {
	cfg := Config{
		Enabled:        envBool("SUB2API_MEDIA_ENABLED"),
		GateBaseURL:    envString("SUB2API_MEDIA_GATE_BASE_URL", "https://gate.ichen.su"),
		Issuer:         envString("SUB2API_MEDIA_SERVICE_ISSUER", "https://sub2api.local/internal/media"),
		Audience:       envString("SUB2API_MEDIA_SERVICE_AUDIENCE", "gate-media"),
		CallerID:       envString("SUB2API_MEDIA_CALLER_ID", "sub2api"),
		KeyID:          strings.TrimSpace(os.Getenv("SUB2API_MEDIA_SERVICE_KEY_ID")),
		PrivateKeyPEM:  strings.ReplaceAll(os.Getenv("SUB2API_MEDIA_SERVICE_PRIVATE_KEY_PEM"), `\n`, "\n"),
		PrivateKeyJWK:  strings.TrimSpace(os.Getenv("SUB2API_MEDIA_SERVICE_PRIVATE_JWK")),
		RequestTimeout: envDuration("SUB2API_MEDIA_REQUEST_TIMEOUT", 30*time.Second),
		PollInterval:   envDuration("SUB2API_MEDIA_POLL_INTERVAL", 3*time.Second),
	}
	cfg.GateBaseURL = strings.TrimRight(strings.TrimSpace(cfg.GateBaseURL), "/")
	if !cfg.Enabled {
		return cfg, nil
	}
	if cfg.GateBaseURL == "" || cfg.Issuer == "" || cfg.Audience == "" || cfg.CallerID == "" ||
		cfg.KeyID == "" || (strings.TrimSpace(cfg.PrivateKeyPEM) == "" && cfg.PrivateKeyJWK == "") {
		return Config{}, errors.New("media platform is enabled but its Gate service identity is incomplete")
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
