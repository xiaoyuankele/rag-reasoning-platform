package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultAuthSessionTTL      = 7 * 24 * time.Hour
	defaultAuthRateLimitWindow = time.Minute
	defaultAuthPerClientLimit  = 10
	defaultAuthGlobalLimit     = 200
)

// AuthConfig 保存注册、登录与 Session Cookie 的运行配置。
type AuthConfig struct {
	SessionTTL      time.Duration
	CookieSecure    bool
	RateLimitWindow time.Duration
	PerClientLimit  int
	GlobalLimit     int
}

// LoadAuth 从环境变量加载认证能力配置。
func LoadAuth() (AuthConfig, error) {
	sessionTTL, err := loadPositiveDuration("AUTH_SESSION_TTL", defaultAuthSessionTTL)
	if err != nil {
		return AuthConfig{}, fmt.Errorf("load auth session TTL: %w", err)
	}

	cookieSecure := false
	if rawValue := strings.TrimSpace(os.Getenv("AUTH_COOKIE_SECURE")); rawValue != "" {
		cookieSecure, err = strconv.ParseBool(rawValue)
		if err != nil {
			return AuthConfig{}, fmt.Errorf("load AUTH_COOKIE_SECURE: %w", err)
		}
	}

	rateLimitWindow, err := loadPositiveDuration(
		"AUTH_RATE_LIMIT_WINDOW",
		defaultAuthRateLimitWindow,
	)
	if err != nil {
		return AuthConfig{}, fmt.Errorf("load auth rate-limit window: %w", err)
	}
	perClientLimit, err := loadPositiveBoundedInt(
		"AUTH_PER_CLIENT_LIMIT",
		defaultAuthPerClientLimit,
		1000,
	)
	if err != nil {
		return AuthConfig{}, fmt.Errorf("load auth per-client limit: %w", err)
	}
	globalLimit, err := loadPositiveBoundedInt(
		"AUTH_GLOBAL_LIMIT",
		defaultAuthGlobalLimit,
		10000,
	)
	if err != nil {
		return AuthConfig{}, fmt.Errorf("load auth global limit: %w", err)
	}
	if globalLimit < perClientLimit {
		return AuthConfig{}, fmt.Errorf(
			"auth global rate limit must not be smaller than per-client limit",
		)
	}

	return AuthConfig{
		SessionTTL:      sessionTTL,
		CookieSecure:    cookieSecure,
		RateLimitWindow: rateLimitWindow,
		PerClientLimit:  perClientLimit,
		GlobalLimit:     globalLimit,
	}, nil
}
