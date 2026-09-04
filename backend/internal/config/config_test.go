package config

import (
	"strings"
	"testing"
)

func TestLoadDefaultsToFiveMiBAndRequestDerivedShareURLs(t *testing.T) {
	t.Setenv("MAX_FILE_SIZE_MB", "")
	t.Setenv("PUBLIC_SHARE_BASE_URL", "")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MaxFileSizeBytes != 5<<20 {
		t.Errorf("MaxFileSizeBytes = %d, want %d", cfg.MaxFileSizeBytes, 5<<20)
	}
	if cfg.MaxFileSizeMB != 5 {
		t.Errorf("MaxFileSizeMB = %d, want 5", cfg.MaxFileSizeMB)
	}
	if cfg.PublicShareBaseURL != "" {
		t.Errorf("PublicShareBaseURL = %q, want empty", cfg.PublicShareBaseURL)
	}
}

func TestLoadAcceptsTwentyMiBAndNormalizesShareOrigin(t *testing.T) {
	t.Setenv("MAX_FILE_SIZE_MB", "20")
	t.Setenv("PUBLIC_SHARE_BASE_URL", "https://share.example.com/")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.MaxFileSizeBytes != 20<<20 {
		t.Errorf("MaxFileSizeBytes = %d, want %d", cfg.MaxFileSizeBytes, 20<<20)
	}
	if cfg.MaxFileSizeMB != 20 {
		t.Errorf("MaxFileSizeMB = %d, want 20", cfg.MaxFileSizeMB)
	}
	if cfg.PublicShareBaseURL != "https://share.example.com" {
		t.Errorf("PublicShareBaseURL = %q", cfg.PublicShareBaseURL)
	}
}

func TestLoadRejectsInvalidMaxFileSize(t *testing.T) {
	for _, value := range []string{"abc", "0", "-1", "4398046511104", "8796093022208"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("MAX_FILE_SIZE_MB", value)
			t.Setenv("PUBLIC_SHARE_BASE_URL", "")

			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), "MAX_FILE_SIZE_MB") {
				t.Fatalf("Load error = %v, want MAX_FILE_SIZE_MB error", err)
			}
		})
	}
}

func TestLoadRejectsShareBaseThatIsNotPureHTTPOrigin(t *testing.T) {
	invalid := []string{
		"ftp://share.example.com",
		"https://",
		"https://user@share.example.com",
		"https://share.example.com/res",
		"https://share.example.com/path",
		"https://share.example.com?x=1",
		"https://share.example.com#fragment",
		"https://share.example.com/#",
	}
	for _, value := range invalid {
		t.Run(value, func(t *testing.T) {
			t.Setenv("MAX_FILE_SIZE_MB", "")
			t.Setenv("PUBLIC_SHARE_BASE_URL", value)

			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), "PUBLIC_SHARE_BASE_URL") {
				t.Fatalf("Load error = %v, want PUBLIC_SHARE_BASE_URL error", err)
			}
		})
	}
}
