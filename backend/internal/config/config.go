// Package config loads process-local runtime configuration from environment
// variables. None of these values are persisted in SQLite.
package config

import (
	"fmt"
	"math"
	"net/url"
	"os"
	"strconv"
	"strings"
)

const defaultMaxFileSizeMB int64 = 5

type Runtime struct {
	MaxFileSizeMB      int64
	MaxFileSizeBytes   int64
	PublicShareBaseURL string
}

func Default() Runtime {
	return Runtime{
		MaxFileSizeMB:    defaultMaxFileSizeMB,
		MaxFileSizeBytes: defaultMaxFileSizeMB << 20,
	}
}

func Load() (Runtime, error) {
	cfg := Default()

	if raw := strings.TrimSpace(os.Getenv("MAX_FILE_SIZE_MB")); raw != "" {
		mb, err := strconv.ParseInt(raw, 10, 64)
		// An MCP JSON string can expand each input byte to a six-byte \u00XX
		// escape. Reject values that would overflow that request-body cap.
		maxMB := int64((math.MaxInt64 - (64 << 10)) / (6 << 20))
		if err != nil || mb <= 0 || mb > maxMB {
			return Runtime{}, fmt.Errorf("MAX_FILE_SIZE_MB must be a positive whole number that fits in bytes")
		}
		cfg.MaxFileSizeMB = mb
		cfg.MaxFileSizeBytes = mb << 20
	}

	baseURL, err := normalizePublicShareBaseURL(os.Getenv("PUBLIC_SHARE_BASE_URL"))
	if err != nil {
		return Runtime{}, err
	}
	cfg.PublicShareBaseURL = baseURL
	return cfg, nil
}

func normalizePublicShareBaseURL(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}

	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
		u.User != nil || (u.Path != "" && u.Path != "/") || u.RawPath != "" ||
		u.RawQuery != "" || u.ForceQuery || strings.Contains(raw, "#") ||
		u.Fragment != "" || u.RawFragment != "" {
		return "", fmt.Errorf("PUBLIC_SHARE_BASE_URL must be an http/https origin without credentials, path, query, or fragment")
	}

	return u.Scheme + "://" + u.Host, nil
}
