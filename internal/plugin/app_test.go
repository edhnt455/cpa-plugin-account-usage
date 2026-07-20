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

func TestAggregateAccountsUsesMaxForPercentBalancesByDefault(t *testing.T) {
	resp := AggregateAccounts(DefaultConfig(), []AccountUsage{
		{Available: true, Known: true, Balance: 66, Unit: "%", ResetAt: "2026-07-25T08:31:56Z", UsedPercent: 34, ResetCredits: 3, RawBalance: "0"},
		{Available: true, Known: true, Balance: 88, Unit: "%", ResetAt: "2026-07-26T08:31:56Z", UsedPercent: 12, ResetCredits: 1, RawBalance: "5"},
	})
	if resp.Balance != 88 || resp.Unit != "%" {
		t.Fatalf("Balance/unit = %v/%q, want 88/%%", resp.Balance, resp.Unit)
	}
	if resp.ResetAt != "2026-07-26T08:31:56Z" || resp.UsedPercent != 12 || resp.ResetCredits != 1 || resp.RawBalance != "5" {
		t.Fatalf("top-level quota details = %q/%v/%d/%q, want selected account details", resp.ResetAt, resp.UsedPercent, resp.ResetCredits, resp.RawBalance)
	}
}

func TestAuthAvailableHonorsCooldown(t *testing.T) {
	if AuthAvailable(HostAuthFileEntry{Status: "active", NextRetryAfter: time.Now().Add(time.Minute)}) {
		t.Fatal("AuthAvailable() = true during cooldown")
	}
}

func TestProviderMatchesAliases(t *testing.T) {
	if !providerMatchesFilter("xai", "grok") {
		t.Fatal("xai should match grok")
	}
	if !providerMatchesFilter("antigravity", "gemini") {
		t.Fatal("antigravity should match gemini")
	}
}

func TestPublicUsageResponseStripsAccounts(t *testing.T) {
	resp := publicUsageResponse(UsageResponse{
		IsValid:     true,
		Balance:     53,
		Unit:        "%",
		ResetAt:     "2026-07-25T08:31:56Z",
		UsedPercent: 47,
		Accounts: []AccountUsage{{
			AuthIndex: "secret-auth-index",
			Email:     "user@example.com",
			Name:      "codex-user.json",
		}},
	})
	if len(resp.Accounts) != 0 {
		t.Fatalf("Accounts len = %d, want 0", len(resp.Accounts))
	}
	if !resp.IsValid || resp.Balance != 53 || resp.Unit != "%" {
		t.Fatalf("public response summary changed: %#v", resp)
	}
	if resp.ResetAt != "2026-07-25T08:31:56Z" || resp.UsedPercent != 47 {
		t.Fatalf("public quota details = %q/%v, want reset/47", resp.ResetAt, resp.UsedPercent)
	}
}

func TestCodexQuotaWindowsReturnsRemainingPercent(t *testing.T) {
	windows := codexQuotaWindows([]byte(`{
		"rate_limit": {
			"allowed": true,
			"limit_reached": false,
			"primary_window": {
				"used_percent": 33,
				"reset_at": 1784968316
			}
		}
	}`))
	remaining, ok := minimumRemaining(windows)
	if !ok || remaining != 67 {
		t.Fatalf("remaining = %v/%v, want 67/true", remaining, ok)
	}
}

func TestKimiQuotaRowsUseRemainingOverUsedFallback(t *testing.T) {
	rows := kimiQuotaRows([]byte(`{
		"usage": {"limit": 100, "remaining": 25},
		"limits": [{"detail": {"limit": 50, "used": 10}}]
	}`))
	remaining, ok := minimumRemaining(rows)
	if !ok || remaining != 25 {
		t.Fatalf("remaining = %v/%v, want 25/true", remaining, ok)
	}
}

func TestXaiSummaryReturnsCreditRemainingPercent(t *testing.T) {
	summary, ok := parseXaiSummary([]byte(`{
		"config": {
			"currentPeriod": {"type": "weekly", "end": "2026-07-24T07:16:00Z"},
			"creditUsagePercent": 10
		}
	}`))
	if !ok || summary.RemainingPercent == nil || *summary.RemainingPercent != 90 {
		t.Fatalf("summary = %#v, ok=%v, want 90%% remaining", summary, ok)
	}
	if summary.ResetAt != "2026-07-24T07:16:00Z" {
		t.Fatalf("ResetAt = %q, want weekly period end", summary.ResetAt)
	}
}

func TestXaiSummaryReturnsBillingPeriodEndForMonthlyCredits(t *testing.T) {
	summary, ok := parseXaiSummary([]byte(`{
		"config": {
			"monthlyLimit": {"val": 15000},
			"used": {"val": 511},
			"billingPeriodEnd": "2026-08-01T00:00:00Z"
		}
	}`))
	if !ok || summary.RemainingPercent == nil || *summary.RemainingPercent != 96.59 {
		t.Fatalf("summary = %#v, ok=%v, want 96.59%% remaining", summary, ok)
	}
	if summary.ResetAt != "2026-08-01T00:00:00Z" {
		t.Fatalf("ResetAt = %q, want billing period end", summary.ResetAt)
	}
}

func TestAntigravityBucketsCanFilterGeminiGroup(t *testing.T) {
	buckets := antigravityBuckets([]byte(`{
		"groups": [{
			"displayName": "Gemini Models",
			"buckets": [{"remainingFraction": 0.72}]
		}, {
			"displayName": "Claude and GPT Models",
			"buckets": [{"remainingFraction": 0.15}]
		}]
	}`), "gemini")
	remaining, ok := minimumRemaining(buckets)
	if !ok || remaining != 72 {
		t.Fatalf("remaining = %v/%v, want 72/true", remaining, ok)
	}
}
