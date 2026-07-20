package plugin

import (
	"testing"
	"time"
)

func TestDecodeConfigNormalizesProviderKeys(t *testing.T) {
	cfg, err := DecodeConfig([]byte(`
aggregate: min
include_providers: ["Codex", "XAI"]
providers:
  XAI:
    url: "https://example.invalid/billing"
    balance_path: "data.balance"
`))
	if err != nil {
		t.Fatalf("DecodeConfig() error = %v", err)
	}
	if cfg.Aggregate != "min" {
		t.Fatalf("Aggregate = %q, want min", cfg.Aggregate)
	}
	if len(cfg.IncludeProviders) != 2 || cfg.IncludeProviders[0] != "codex" || cfg.IncludeProviders[1] != "xai" {
		t.Fatalf("IncludeProviders = %#v, want normalized providers", cfg.IncludeProviders)
	}
	if _, ok := cfg.Providers["xai"]; !ok {
		t.Fatalf("Providers = %#v, want xai key", cfg.Providers)
	}
}

func TestAggregateAccountsFallsBackToAvailableAccountCount(t *testing.T) {
	resp := AggregateAccounts(DefaultConfig(), []AccountUsage{
		{Available: true},
		{Available: false},
		{Available: true},
	})
	if !resp.IsValid {
		t.Fatal("IsValid = false, want true")
	}
	if resp.Balance != 2 || resp.Unit != "accounts" {
		t.Fatalf("Balance/unit = %v/%q, want 2/accounts", resp.Balance, resp.Unit)
	}
}

func TestAggregateAccountsSumsKnownBalances(t *testing.T) {
	cfg := DefaultConfig()
	resp := AggregateAccounts(cfg, []AccountUsage{
		{Available: true, Known: true, Balance: 1.25, Unit: "USD"},
		{Available: true, Known: true, Balance: 2.75, Unit: "USD"},
	})
	if resp.Balance != 4 || resp.Unit != "USD" {
		t.Fatalf("Balance/unit = %v/%q, want 4/USD", resp.Balance, resp.Unit)
	}
}

func TestAuthAvailableHonorsCooldown(t *testing.T) {
	if AuthAvailable(HostAuthFileEntry{Status: "active", NextRetryAfter: time.Now().Add(time.Minute)}) {
		t.Fatal("AuthAvailable() = true during cooldown")
	}
}
