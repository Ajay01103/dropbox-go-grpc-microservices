package main

import (
	"testing"
	"time"

	"github.com/Ajay01103/go-notion/auth/config"
	"go.uber.org/zap"
)

func TestValidateTokenDurationsWarnsWhenAccessTokenOutlivesSessionCache(t *testing.T) {
	warnings := ValidateTokenDurations(config.Config{AccessTokenDuration: 30 * time.Minute}, zap.NewNop())
	if len(warnings) != 1 {
		t.Fatalf("expected one warning, got %d: %v", len(warnings), warnings)
	}
}

func TestValidateTokenDurationsReturnsNoWarningsWhenDurationsAreAligned(t *testing.T) {
	warnings := ValidateTokenDurations(config.Config{AccessTokenDuration: 15 * time.Minute}, zap.NewNop())
	if len(warnings) != 0 {
		t.Fatalf("expected no warnings, got %d: %v", len(warnings), warnings)
	}
}