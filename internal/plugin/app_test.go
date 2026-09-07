package plugin

import (
	"encoding/json"
	"testing"
	"time"
)

type hostCallerFunc func(method string, payload []byte) ([]byte, error)

func (f hostCallerFunc) Call(method string, payload []byte) ([]byte, error) {
	return f(method, payload)
}

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

func TestAuthUsageProbeAllowedDuringTemporaryUnavailability(t *testing.T) {
	auth := HostAuthFileEntry{
		Status:         "error",
		Unavailable:    true,
		NextRetryAfter: time.Now().Add(time.Minute),
	}
	if AuthAvailable(auth) {
		t.Fatal("AuthAvailable() = true for temporarily unavailable auth")
	}
	if !AuthUsageProbeAllowed(auth) {
		t.Fatal("AuthUsageProbeAllowed() = false for temporarily unavailable auth")
	}
}

func TestAuthUsageProbeRejectsDisabledAuth(t *testing.T) {
	for _, auth := range []HostAuthFileEntry{
		{Disabled: true, Status: "active"},
		{Status: "disabled"},
	} {
		if AuthUsageProbeAllowed(auth) {
			t.Fatalf("AuthUsageProbeAllowed(%#v) = true for disabled auth", auth)
		}
	}
}

func TestAggregateAccountsAcceptsKnownQuotaFromUnavailableAuth(t *testing.T) {
	resp := AggregateAccounts(DefaultConfig(), []AccountUsage{
		{Available: false, Known: true, Balance: 67, Unit: "%"},
	})
	if !resp.IsValid {
		t.Fatal("IsValid = false for a successfully queried quota")
	}
	if resp.AvailableCount != 0 || resp.KnownCount != 1 || resp.UnknownCount != 0 {
		t.Fatalf("counts = available:%d known:%d unknown:%d, want 0/1/0", resp.AvailableCount, resp.KnownCount, resp.UnknownCount)
	}
	if resp.Balance != 67 || resp.Unit != "%" {
		t.Fatalf("Balance/unit = %v/%q, want 67/%%", resp.Balance, resp.Unit)
	}
}

func TestInspectCodexUsageProbesTemporarilyUnavailableAuth(t *testing.T) {
	httpCalled := false
	app := NewApp(hostCallerFunc(func(method string, payload []byte) ([]byte, error) {
		switch method {
		case MethodHostAuthGet:
			return OKEnvelope(HostAuthGetResponse{
				AuthIndex: "codex-1",
				JSON:      json.RawMessage(`{"access_token":"test-token","account_id":"account-1"}`),
			})
		case MethodHostHTTPDo:
			httpCalled = true
			var req HostHTTPRequest
			if err := json.Unmarshal(payload, &req); err != nil {
				t.Fatalf("decode HTTP request: %v", err)
			}
			if req.URL != codexUsageURL {
				t.Fatalf("URL = %q, want %q", req.URL, codexUsageURL)
			}
			if got := headerValue(req.Headers, "User-Agent"); got != "codex-tui/0.149.1 (Mac OS 26.5.2; arm64) iTerm.app/3.6.11 (codex-tui; 0.149.1)" {
				t.Fatalf("User-Agent = %q, want current management-center value", got)
			}
			return OKEnvelope(HostHTTPResponse{
				StatusCode: 200,
				Body: []byte(`{
					"rate_limit": {
						"primary_window": {"used_percent": 33, "reset_at": 1784968316},
						"secondary_window": {"used_percent": 71, "reset_at": 1785573116}
					}
				}`),
			})
		default:
			t.Fatalf("unexpected host method %q", method)
			return nil, nil
		}
	}))

	account := app.inspectAuthUsage(DefaultConfig(), UsageRequest{Provider: "codex"}, HostAuthFileEntry{
		AuthIndex:      "codex-1",
		Provider:       "codex",
		Status:         "error",
		Unavailable:    true,
		NextRetryAfter: time.Now().Add(time.Minute),
	}, "callback-1")
	if !httpCalled {
		t.Fatal("Codex quota HTTP request was not made")
	}
	if account.Available {
		t.Fatal("Available = true for temporarily unavailable auth")
	}
	if !account.Known || account.Balance != 67 || account.Unit != "%" {
		t.Fatalf("account quota = known:%v balance:%v unit:%q, want true/67/%%", account.Known, account.Balance, account.Unit)
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
		FiveHour: &QuotaWindow{
			Balance:     53,
			Unit:        "%",
			ResetAt:     "2026-07-25T08:31:56Z",
			UsedPercent: 47,
		},
		Weekly: &QuotaWindow{
			Balance:     81,
			Unit:        "%",
			ResetAt:     "2026-07-30T08:31:56Z",
			UsedPercent: 19,
		},
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
	if resp.FiveHour == nil || resp.FiveHour.Balance != 53 || resp.Weekly == nil || resp.Weekly.Balance != 81 {
		t.Fatalf("public quota windows = %#v/%#v, want 5h=53 and weekly=81", resp.FiveHour, resp.Weekly)
	}
}

func TestCodexQuotaBalanceDefaultsToFiveHourAndExposesWeekly(t *testing.T) {
	balance, ok := codexQuotaBalance([]byte(`{
		"rate_limit": {
			"allowed": true,
			"limit_reached": false,
			"primary_window": {
				"used_percent": 33,
				"reset_at": 1784968316
			},
			"secondary_window": {
				"used_percent": 71,
				"reset_at": 1785573116
			}
		},
		"code_review_rate_limit": {
			"primary_window": {
				"used_percent": 99,
				"reset_at": 1784960000
			},
			"secondary_window": {
				"used_percent": 98,
				"reset_at": 1785570000
			}
		}
	}`))
	if !ok {
		t.Fatal("codexQuotaBalance() ok = false")
	}
	if balance.Balance != 67 || balance.UsedPercent != 33 || balance.ResetAt != "2026-07-25T08:31:56Z" {
		t.Fatalf("default balance = %v/%v/%q, want five-hour 67/33/reset", balance.Balance, balance.UsedPercent, balance.ResetAt)
	}
	if balance.FiveHour == nil || balance.FiveHour.Balance != 67 || balance.FiveHour.UsedPercent != 33 {
		t.Fatalf("five-hour window = %#v, want 67%% remaining and 33%% used", balance.FiveHour)
	}
	if balance.Weekly == nil || balance.Weekly.Balance != 29 || balance.Weekly.UsedPercent != 71 || balance.Weekly.ResetAt != "2026-08-01T08:31:56Z" {
		t.Fatalf("weekly window = %#v, want 29%% remaining and reset time", balance.Weekly)
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
	summary, ok := parseXaiWeeklySummary([]byte(`{
		"config": {
			"currentPeriod": {"type": "USAGE_PERIOD_TYPE_WEEKLY", "end": "2026-07-24T07:16:00Z"},
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

func TestXaiWeeklySummaryTreatsOmittedUsageAsZero(t *testing.T) {
	summary, ok := parseXaiWeeklySummary([]byte(`{
		"config": {
			"currentPeriod": {"type": "USAGE_PERIOD_TYPE_WEEKLY", "end": "2026-07-24T07:16:00Z"}
		}
	}`))
	if !ok || summary.RemainingPercent == nil || *summary.RemainingPercent != 100 {
		t.Fatalf("summary = %#v, ok=%v, want 100%% remaining", summary, ok)
	}
}

func TestXaiWeeklySummaryRejectsMonthlyPeriod(t *testing.T) {
	summary, ok := parseXaiWeeklySummary([]byte(`{
		"config": {
			"currentPeriod": {"type": "MONTHLY", "end": "2026-08-01T00:00:00Z"},
			"creditUsagePercent": 10
		}
	}`))
	if ok || summary.RemainingPercent != nil {
		t.Fatalf("summary = %#v, ok=%v, want monthly quota ignored", summary, ok)
	}
}

func TestXaiWeeklySummaryRejectsInvalidUsage(t *testing.T) {
	summary, ok := parseXaiWeeklySummary([]byte(`{
		"config": {
			"currentPeriod": {"type": "WEEKLY", "end": "2026-07-24T07:16:00Z"},
			"creditUsagePercent": "invalid"
		}
	}`))
	if ok || summary.RemainingPercent != nil {
		t.Fatalf("summary = %#v, ok=%v, want invalid usage rejected", summary, ok)
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

func TestAntigravityQuotaBalanceDefaultsToFiveHourAndExposesWeekly(t *testing.T) {
	balance, ok := antigravityQuotaBalance([]byte(`{
		"response": {
			"groups": [{
				"displayName": "Gemini Models",
				"buckets": [{
					"bucketId": "gemini-weekly",
					"displayName": "Weekly Limit",
					"window": "weekly",
					"remainingFraction": 0.31,
					"resetTime": "2026-08-09T12:00:00Z"
				}, {
					"bucketId": "gemini-five-hour",
					"displayName": "Five Hour Limit",
					"window": "5h",
					"remainingFraction": 0.74,
					"resetTime": "2026-08-03T12:00:00Z"
				}]
			}, {
				"displayName": "Claude and GPT Models",
				"buckets": [{
					"window": "5h",
					"remainingFraction": 0.05,
					"resetTime": "2026-08-03T11:00:00Z"
				}]
			}]
		}
	}`), "gemini", "project-123")
	if !ok {
		t.Fatal("antigravityQuotaBalance() ok = false")
	}
	if balance.Balance != 74 || balance.UsedPercent != 26 || balance.ResetAt != "2026-08-03T12:00:00Z" {
		t.Fatalf("default balance = %v/%v/%q, want five-hour 74/26/reset", balance.Balance, balance.UsedPercent, balance.ResetAt)
	}
	if balance.FiveHour == nil || balance.FiveHour.Balance != 74 || balance.FiveHour.ResetAt != "2026-08-03T12:00:00Z" {
		t.Fatalf("five-hour window = %#v, want 74%% and reset time", balance.FiveHour)
	}
	if balance.Weekly == nil || balance.Weekly.Balance != 31 || balance.Weekly.ResetAt != "2026-08-09T12:00:00Z" {
		t.Fatalf("weekly window = %#v, want 31%% and reset time", balance.Weekly)
	}
}

func TestNormalizeQuotaWindowSupportsLabels(t *testing.T) {
	for input, want := range map[string]string{
		"Five Hour Limit": "5h",
		"5-hour":          "5h",
		"Weekly Limit":    "weekly",
		"seven_day":       "weekly",
	} {
		if got := normalizeQuotaWindow(input); got != want {
			t.Errorf("normalizeQuotaWindow(%q) = %q, want %q", input, got, want)
		}
	}
}
