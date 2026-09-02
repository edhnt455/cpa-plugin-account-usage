package plugin

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

const (
	antigravityOAuthTokenURL = "https://oauth2.googleapis.com/token"

	codexUsageURL                 = "https://chatgpt.com/backend-api/wham/usage"
	kimiUsageURL                  = "https://api.kimi.com/coding/v1/usages"
	xaiBillingWeeklyURL           = "https://cli-chat-proxy.grok.com/v1/billing?format=credits"
	antigravityQuotaURLPrimary    = "https://daily-cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary"
	antigravityQuotaURLSandbox    = "https://daily-cloudcode-pa.sandbox.googleapis.com/v1internal:retrieveUserQuotaSummary"
	antigravityQuotaURLProduction = "https://cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary"
)

type officialBalance struct {
	Balance      float64
	Unit         string
	Source       string
	ResetAt      string
	UsedPercent  float64
	RawBalance   string
	ResetCredits int
	FiveHour     *QuotaWindow
	Weekly       *QuotaWindow
	Details      any
	Error        string
}

type quotaWindowDetail struct {
	ID        string  `json:"id"`
	Label     string  `json:"label,omitempty"`
	Window    string  `json:"window,omitempty"`
	Used      float64 `json:"used_percent,omitempty"`
	Remaining float64 `json:"remaining_percent,omitempty"`
	ResetAt   string  `json:"reset_at,omitempty"`
}

func (a *App) fetchOfficialBalance(provider string, requestedProvider string, auth HostAuthFileEntry, hostCallbackID string) (officialBalance, bool) {
	switch normalizeProvider(provider) {
	case "codex":
		return a.fetchCodexOfficialBalance(auth, hostCallbackID), true
	case "xai":
		return a.fetchXaiOfficialBalance(auth, hostCallbackID), true
	case "kimi":
		return a.fetchKimiOfficialBalance(auth, hostCallbackID), true
	case "gemini":
		if normalizeProvider(requestedProvider) == "" {
			requestedProvider = "gemini"
		}
		return a.fetchAntigravityOfficialBalance(auth, requestedProvider, hostCallbackID), true
	case "antigravity":
		return a.fetchAntigravityOfficialBalance(auth, requestedProvider, hostCallbackID), true
	default:
		return officialBalance{}, false
	}
}

func (a *App) fetchCodexOfficialBalance(auth HostAuthFileEntry, hostCallbackID string) officialBalance {
	token, rawAuthJSON, errToken := a.tokenForAuth(auth.AuthIndex, []string{"access_token", "metadata.access_token", "token", "metadata.token"})
	if errToken != nil {
		return officialBalance{Error: errToken.Error()}
	}
	headers := map[string]string{
		"Authorization": "Bearer " + token,
		"Content-Type":  "application/json",
		"User-Agent":    "codex_cli_rs/0.76.0 (Debian 13.0.0; x86_64) WindowsTerminal",
	}
	if accountID := codexChatGPTAccountID(rawAuthJSON); accountID != "" {
		headers["Chatgpt-Account-Id"] = accountID
	}
	body, errHTTP := a.doHostHTTP(http.MethodGet, codexUsageURL, headers, nil, hostCallbackID)
	if errHTTP != nil {
		return officialBalance{Error: errHTTP.Error()}
	}
	balance, okBalance := codexQuotaBalance(body)
	creditsBalance := strings.TrimSpace(gjson.GetBytes(body, "credits.balance").String())
	resetCredits := int(gjson.GetBytes(body, "rate_limit_reset_credits.available_count").Int())
	if resetCredits == 0 {
		resetCredits = int(gjson.GetBytes(body, "rateLimitResetCredits.availableCount").Int())
	}
	if okBalance {
		balance.RawBalance = creditsBalance
		balance.ResetCredits = resetCredits
		return balance
	}
	details := map[string]any{
		"plan_type":     gjson.GetBytes(body, "plan_type").String(),
		"windows":       codexQuotaWindows(body),
		"credits":       creditsBalance,
		"reset_credits": resetCredits,
	}
	if credits, okCredits := parseNumber(creditsBalance); okCredits {
		return officialBalance{
			Balance:      credits,
			Unit:         "credits",
			Source:       "codex-wham-usage",
			RawBalance:   creditsBalance,
			ResetCredits: resetCredits,
			Details:      details,
		}
	}
	return officialBalance{Error: "codex usage payload did not contain usable quota fields"}
}

func codexQuotaBalance(body []byte) (officialBalance, bool) {
	windows := codexQuotaWindows(body)
	fiveHour := quotaWindowByID(windows, "codex:primary")
	weekly := quotaWindowByID(windows, "codex:secondary")

	// Codex's primary and secondary rate-limit windows are the 5-hour and
	// 7-day limits respectively. Keep the top-level response on the shorter
	// window, matching the Gemini response contract.
	selected := fiveHour
	if selected == nil {
		selected = weekly
	}
	if selected == nil {
		return officialBalance{}, false
	}

	return officialBalance{
		Balance:     selected.Balance,
		Unit:        selected.Unit,
		Source:      "codex-wham-usage",
		ResetAt:     selected.ResetAt,
		UsedPercent: selected.UsedPercent,
		FiveHour:    fiveHour,
		Weekly:      weekly,
		Details: map[string]any{
			"plan_type": gjson.GetBytes(body, "plan_type").String(),
			"windows":   windows,
		},
	}, true
}

func quotaWindowByID(windows []quotaWindowDetail, id string) *QuotaWindow {
	for _, window := range windows {
		if window.ID != id {
			continue
		}
		return &QuotaWindow{
			Balance:     window.Remaining,
			Unit:        "%",
			ResetAt:     window.ResetAt,
			UsedPercent: window.Used,
		}
	}
	return nil
}

func codexQuotaWindows(body []byte) []quotaWindowDetail {
	windows := make([]quotaWindowDetail, 0)
	addCodexLimitWindows(&windows, "codex", firstGJSONResult(body, "rate_limit", "rateLimit"))
	addCodexLimitWindows(&windows, "code_review", firstGJSONResult(body, "code_review_rate_limit", "codeReviewRateLimit"))
	firstGJSONResult(body, "additional_rate_limits", "additionalRateLimits").ForEach(func(_, item gjson.Result) bool {
		name := firstNonEmpty(item.Get("limit_name").String(), item.Get("limitName").String(), item.Get("metered_feature").String(), item.Get("meteredFeature").String(), "additional")
		addCodexLimitWindows(&windows, "additional:"+name, firstResult(item, "rate_limit", "rateLimit"))
		return true
	})
	return windows
}

func addCodexLimitWindows(out *[]quotaWindowDetail, prefix string, limit gjson.Result) {
	if !limit.Exists() || limit.Type == gjson.Null {
		return
	}
	addCodexWindow(out, prefix+":primary", "5h", firstResult(limit, "primary_window", "primaryWindow"), limit)
	addCodexWindow(out, prefix+":secondary", "weekly", firstResult(limit, "secondary_window", "secondaryWindow"), limit)
}

func addCodexWindow(out *[]quotaWindowDetail, id string, kind string, window gjson.Result, limit gjson.Result) {
	if !window.Exists() || window.Type == gjson.Null {
		return
	}
	used, okUsed := firstGJSONFloat(window, "used_percent", "usedPercent")
	if !okUsed {
		allowed := limit.Get("allowed")
		if firstResult(limit, "limit_reached", "limitReached").Bool() || (allowed.Exists() && !allowed.Bool()) {
			used = 100
			okUsed = true
		}
	}
	if !okUsed {
		return
	}
	remaining := clampPercent(100 - used)
	*out = append(*out, quotaWindowDetail{
		ID:        id,
		Window:    kind,
		Used:      round2(clampPercent(used)),
		Remaining: round2(remaining),
		ResetAt:   unixSecondsString(firstResult(window, "reset_at", "resetAt").Int(), firstResult(window, "reset_after_seconds", "resetAfterSeconds").Int()),
	})
}

func (a *App) fetchKimiOfficialBalance(auth HostAuthFileEntry, hostCallbackID string) officialBalance {
	token, _, errToken := a.tokenForAuth(auth.AuthIndex, []string{"access_token", "api_key", "token", "metadata.access_token", "metadata.api_key", "metadata.token", "attributes.api_key"})
	if errToken != nil {
		return officialBalance{Error: errToken.Error()}
	}
	body, errHTTP := a.doHostHTTP(http.MethodGet, kimiUsageURL, map[string]string{
		"Authorization": "Bearer " + token,
	}, nil, hostCallbackID)
	if errHTTP != nil {
		return officialBalance{Error: errHTTP.Error()}
	}
	rows := kimiQuotaRows(body)
	remaining, okRemaining := minimumRemaining(rows)
	if !okRemaining {
		return officialBalance{Error: "kimi usage payload did not contain usable quota rows"}
	}
	return officialBalance{
		Balance:     round2(remaining),
		Unit:        "%",
		Source:      "kimi-coding-usages",
		ResetAt:     firstResetAt(rows),
		UsedPercent: round2(100 - remaining),
		Details:     map[string]any{"windows": rows},
	}
}

func kimiQuotaRows(body []byte) []quotaWindowDetail {
	rows := make([]quotaWindowDetail, 0)
	addKimiRow(&rows, "summary", gjson.GetBytes(body, "usage"))
	gjson.GetBytes(body, "limits").ForEach(func(key, item gjson.Result) bool {
		detail := item.Get("detail")
		if !detail.Exists() || detail.Type == gjson.Null {
			detail = item
		}
		addKimiRow(&rows, "limit:"+key.String(), detail)
		return true
	})
	return rows
}

func addKimiRow(out *[]quotaWindowDetail, id string, row gjson.Result) {
	if !row.Exists() || row.Type == gjson.Null {
		return
	}
	limit, okLimit := gjsonFloat(row.Get("limit"))
	used, okUsed := gjsonFloat(row.Get("used"))
	if !okUsed {
		if remaining, okRemaining := gjsonFloat(row.Get("remaining")); okRemaining && okLimit {
			used = limit - remaining
			okUsed = true
		}
	}
	if !okLimit || limit <= 0 || !okUsed {
		return
	}
	remaining := clampPercent(((limit - used) / limit) * 100)
	*out = append(*out, quotaWindowDetail{
		ID:        id,
		Used:      round2(clampPercent((used / limit) * 100)),
		Remaining: round2(remaining),
		ResetAt:   firstNonEmpty(row.Get("reset_at").String(), row.Get("resetAt").String(), row.Get("reset_time").String(), row.Get("resetTime").String()),
	})
}

func (a *App) fetchXaiOfficialBalance(auth HostAuthFileEntry, hostCallbackID string) officialBalance {
	token, rawAuthJSON, errToken := a.tokenForAuth(auth.AuthIndex, []string{"access_token", "api_key", "token", "metadata.access_token", "metadata.api_key", "metadata.token", "attributes.api_key"})
	if errToken != nil {
		return officialBalance{Error: errToken.Error()}
	}
	headers := map[string]string{
		"Authorization":         "Bearer " + token,
		"x-xai-token-auth":      "xai-grok-cli",
		"x-grok-client-version": "0.2.91",
		"accept":                "*/*",
		"user-agent":            "grok-pager/0.2.91 grok-shell/0.2.91 (macos; aarch64)",
	}
	if userID := xaiUserID(auth, rawAuthJSON); userID != "" {
		headers["x-userid"] = userID
	}
	body, errHTTP := a.doHostHTTP(http.MethodGet, xaiBillingWeeklyURL, headers, nil, hostCallbackID)
	if errHTTP != nil {
		return officialBalance{Error: errHTTP.Error()}
	}
	summary, okSummary := parseXaiWeeklySummary(body)
	if !okSummary || summary.RemainingPercent == nil {
		return officialBalance{Error: "xai weekly billing payload did not contain usable remaining percent"}
	}
	remaining := *summary.RemainingPercent
	weekly := &QuotaWindow{
		Balance:     round2(remaining),
		Unit:        "%",
		ResetAt:     summary.ResetAt,
		UsedPercent: round2(100 - remaining),
	}
	return officialBalance{
		Balance:     round2(remaining),
		Unit:        "%",
		Source:      "xai-billing",
		ResetAt:     summary.ResetAt,
		UsedPercent: round2(100 - remaining),
		Weekly:      weekly,
		Details:     map[string]any{"summaries": []xaiSummary{summary}},
	}
}

type xaiSummary struct {
	PeriodType       string   `json:"period_type"`
	PeriodStart      string   `json:"period_start,omitempty"`
	PeriodEnd        string   `json:"period_end,omitempty"`
	ResetAt          string   `json:"reset_at,omitempty"`
	RemainingPercent *float64 `json:"remaining_percent,omitempty"`
}

func parseXaiWeeklySummary(body []byte) (xaiSummary, bool) {
	cfg := gjson.GetBytes(body, "config")
	if !cfg.Exists() || cfg.Type == gjson.Null {
		return xaiSummary{}, false
	}
	periodType := strings.ToLower(cfg.Get("currentPeriod.type").String())
	if periodType == "" {
		periodType = strings.ToLower(cfg.Get("current_period.type").String())
	}
	if !isXaiWeeklyPeriod(periodType) {
		return xaiSummary{}, false
	}
	currentPeriod := firstResult(cfg, "currentPeriod", "current_period")
	periodStart := firstNonEmpty(
		currentPeriod.Get("start").String(),
		cfg.Get("billingPeriodStart").String(),
		cfg.Get("billing_period_start").String(),
	)
	periodEnd := firstNonEmpty(
		currentPeriod.Get("end").String(),
		cfg.Get("billingPeriodEnd").String(),
		cfg.Get("billing_period_end").String(),
	)
	summary := xaiSummary{
		PeriodType:  periodType,
		PeriodStart: periodStart,
		PeriodEnd:   periodEnd,
		ResetAt:     periodEnd,
	}
	usage := firstResult(cfg, "creditUsagePercent", "credit_usage_percent")
	if usage.Exists() {
		used, okUsed := gjsonFloat(usage)
		if !okUsed {
			return summary, false
		}
		remaining := round2(clampPercent(100 - used))
		summary.RemainingPercent = &remaining
		return summary, true
	}
	// The credits endpoint omits the proto3 scalar when the weekly usage is 0%.
	remaining := float64(100)
	summary.RemainingPercent = &remaining
	return summary, true
}

func isXaiWeeklyPeriod(periodType string) bool {
	normalized := strings.ToLower(strings.TrimSpace(periodType))
	return normalized == "weekly" || normalized == "week" || strings.HasSuffix(normalized, "_weekly")
}

func (a *App) fetchAntigravityOfficialBalance(auth HostAuthFileEntry, requestedProvider string, hostCallbackID string) officialBalance {
	token, rawAuthJSON, errToken := a.antigravityAccessToken(auth, hostCallbackID)
	if errToken != nil {
		return officialBalance{Error: errToken.Error()}
	}
	projectID := antigravityProjectID(auth, rawAuthJSON)
	if projectID == "" {
		return officialBalance{Error: "antigravity project_id not found"}
	}
	requestBody, _ := json.Marshal(map[string]string{"project": projectID})
	headers := map[string]string{
		"Authorization": "Bearer " + token,
		"Content-Type":  "application/json",
		"User-Agent":    "antigravity/cli/1.0.13 (aidev_client; os_type=darwin; arch=arm64)",
	}
	var lastErr string
	for _, targetURL := range []string{antigravityQuotaURLPrimary, antigravityQuotaURLSandbox, antigravityQuotaURLProduction} {
		body, errHTTP := a.doHostHTTP(http.MethodPost, targetURL, headers, requestBody, hostCallbackID)
		if errHTTP != nil {
			lastErr = errHTTP.Error()
			continue
		}
		balance, okBalance := antigravityQuotaBalance(body, requestedProvider, projectID)
		if !okBalance {
			lastErr = "antigravity quota payload did not contain usable buckets"
			continue
		}
		return balance
	}
	return officialBalance{Error: lastErr}
}

func antigravityQuotaBalance(body []byte, requestedProvider string, projectID string) (officialBalance, bool) {
	buckets := antigravityBuckets(body, requestedProvider)
	remaining, okRemaining := minimumRemaining(buckets)
	if !okRemaining {
		return officialBalance{}, false
	}

	fiveHour := quotaWindow(buckets, "5h")
	weekly := quotaWindow(buckets, "weekly")
	defaultBalance := round2(remaining)
	defaultResetAt := firstResetAt(buckets)
	if fiveHour != nil {
		defaultBalance = fiveHour.Balance
		defaultResetAt = fiveHour.ResetAt
	}
	return officialBalance{
		Balance:     defaultBalance,
		Unit:        "%",
		Source:      "antigravity-quota-summary",
		ResetAt:     defaultResetAt,
		UsedPercent: round2(100 - defaultBalance),
		FiveHour:    fiveHour,
		Weekly:      weekly,
		Details:     map[string]any{"project_id": projectID, "buckets": buckets},
	}, true
}

func antigravityBuckets(body []byte, requestedProvider string) []quotaWindowDetail {
	requestedProvider = normalizeProvider(requestedProvider)
	onlyGemini := requestedProvider == "gemini"
	buckets := make([]quotaWindowDetail, 0)
	groups := firstGJSONResult(body,
		"groups",
		"response.groups",
		"quotaSummary.groups",
		"quota_summary.groups",
		"response.quotaSummary.groups",
		"response.quota_summary.groups",
	)
	groups.ForEach(func(groupKey, group gjson.Result) bool {
		label := strings.ToLower(firstNonEmpty(group.Get("displayName").String(), group.Get("display_name").String(), groupKey.String()))
		if onlyGemini && !strings.Contains(label, "gemini") {
			return true
		}
		group.Get("buckets").ForEach(func(bucketKey, bucket gjson.Result) bool {
			remainingFraction, okFraction := quotaFraction(bucket.Get("remainingFraction"))
			if !okFraction {
				remainingFraction, okFraction = quotaFraction(bucket.Get("remaining_fraction"))
			}
			if !okFraction {
				return true
			}
			remaining := clampPercent(remainingFraction * 100)
			bucketID := firstNonEmpty(bucket.Get("bucketId").String(), bucket.Get("bucket_id").String(), bucketKey.String())
			bucketLabel := firstNonEmpty(bucket.Get("displayName").String(), bucket.Get("display_name").String())
			window := normalizeQuotaWindow(firstNonEmpty(
				bucket.Get("window").String(),
				bucket.Get("windowType").String(),
				bucket.Get("window_type").String(),
				bucketLabel,
				bucketID,
			))
			buckets = append(buckets, quotaWindowDetail{
				ID:        label + ":" + bucketID,
				Label:     bucketLabel,
				Window:    window,
				Remaining: round2(remaining),
				Used:      round2(100 - remaining),
				ResetAt:   firstNonEmpty(bucket.Get("resetTime").String(), bucket.Get("reset_time").String()),
			})
			return true
		})
		return true
	})
	return buckets
}

func normalizeQuotaWindow(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	normalized := strings.NewReplacer("_", " ", "-", " ").Replace(value)
	switch {
	case strings.Contains(value, "5h"), strings.Contains(normalized, "5 h"), strings.Contains(normalized, "5 hour"), strings.Contains(normalized, "five hour"):
		return "5h"
	case strings.Contains(normalized, "weekly"), strings.Contains(normalized, "7 day"), strings.Contains(value, "7d"), strings.Contains(value, "168h"), strings.Contains(normalized, "seven day"):
		return "weekly"
	default:
		return value
	}
}

func quotaWindow(windows []quotaWindowDetail, kind string) *QuotaWindow {
	kind = normalizeQuotaWindow(kind)
	for _, window := range windows {
		if normalizeQuotaWindow(window.Window) != kind {
			continue
		}
		return &QuotaWindow{
			Balance:     window.Remaining,
			Unit:        "%",
			ResetAt:     window.ResetAt,
			UsedPercent: window.Used,
		}
	}
	return nil
}

func (a *App) antigravityAccessToken(auth HostAuthFileEntry, hostCallbackID string) (string, json.RawMessage, error) {
	token, rawAuthJSON, errToken := a.tokenForAuth(auth.AuthIndex, []string{"access_token", "metadata.access_token"})
	if errToken != nil {
		if refreshed, errRefresh := a.refreshAntigravityAccessToken(rawAuthJSON, hostCallbackID); errRefresh == nil && refreshed != "" {
			return refreshed, rawAuthJSON, nil
		}
		return "", rawAuthJSON, errToken
	}
	if !antigravityTokenExpired(rawAuthJSON) {
		return token, rawAuthJSON, nil
	}
	if refreshed, errRefresh := a.refreshAntigravityAccessToken(rawAuthJSON, hostCallbackID); errRefresh == nil && refreshed != "" {
		return refreshed, rawAuthJSON, nil
	}
	return token, rawAuthJSON, nil
}

func (a *App) refreshAntigravityAccessToken(rawAuthJSON json.RawMessage, hostCallbackID string) (string, error) {
	cfg := a.loadedConfig()
	clientID := strings.TrimSpace(cfg.AntigravityOAuthClientID)
	clientSecret := strings.TrimSpace(cfg.AntigravityOAuthClientSecret)
	if clientID == "" || clientSecret == "" {
		return "", fmt.Errorf("antigravity oauth client id/secret not configured")
	}
	refreshToken := firstRawString(rawAuthJSON, "refresh_token", "metadata.refresh_token", "web.refresh_token", "installed.refresh_token")
	if refreshToken == "" {
		return "", fmt.Errorf("antigravity refresh_token not found")
	}
	form := url.Values{}
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)
	form.Set("grant_type", "refresh_token")
	form.Set("refresh_token", refreshToken)
	body, errHTTP := a.doHostHTTP(http.MethodPost, antigravityOAuthTokenURL, map[string]string{
		"Content-Type": "application/x-www-form-urlencoded",
	}, []byte(form.Encode()), hostCallbackID)
	if errHTTP != nil {
		return "", errHTTP
	}
	refreshed := strings.TrimSpace(gjson.GetBytes(body, "access_token").String())
	if refreshed == "" {
		return "", fmt.Errorf("antigravity oauth refresh returned empty access_token")
	}
	return refreshed, nil
}

func (a *App) doHostHTTP(method string, targetURL string, headers map[string]string, body []byte, hostCallbackID string) ([]byte, error) {
	httpHeaders := make(map[string][]string, len(headers))
	for key, value := range headers {
		if strings.TrimSpace(key) != "" {
			httpHeaders[key] = []string{value}
		}
	}
	result, errCall := a.callHost(MethodHostHTTPDo, HostHTTPRequest{
		HostCallbackID: hostCallbackID,
		Method:         method,
		URL:            targetURL,
		Headers:        httpHeaders,
		Body:           body,
	})
	if errCall != nil {
		return nil, errCall
	}
	var httpResp HostHTTPResponse
	if errUnmarshal := json.Unmarshal(result, &httpResp); errUnmarshal != nil {
		return nil, fmt.Errorf("decode host.http.do result: %w", errUnmarshal)
	}
	if httpResp.StatusCode < http.StatusOK || httpResp.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("upstream status %d: %s", httpResp.StatusCode, shortBody(httpResp.Body))
	}
	return httpResp.Body, nil
}

func codexChatGPTAccountID(raw json.RawMessage) string {
	for _, path := range []string{"id_token", "metadata.id_token", "attributes.id_token"} {
		if id := chatGPTAccountIDFromJWT(gjson.GetBytes(raw, path).String()); id != "" {
			return id
		}
	}
	return ""
}

func chatGPTAccountIDFromJWT(token string) string {
	token = strings.TrimSpace(token)
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return ""
	}
	payload, errDecode := base64.RawURLEncoding.DecodeString(parts[1])
	if errDecode != nil {
		return ""
	}
	return strings.TrimSpace(gjson.GetBytes(payload, "chatgpt_account_id").String())
}

func xaiUserID(auth HostAuthFileEntry, raw json.RawMessage) string {
	for _, candidate := range []string{auth.UserID, auth.Subject, auth.Sub} {
		if strings.TrimSpace(candidate) != "" {
			return strings.TrimSpace(candidate)
		}
	}
	return firstRawString(raw,
		"sub", "subject", "user_id", "userId",
		"metadata.sub", "metadata.subject", "metadata.user_id", "metadata.userId",
		"attributes.sub", "attributes.subject", "attributes.user_id", "attributes.userId",
		"oauth.sub", "oauth.subject", "metadata.oauth.sub", "metadata.oauth.subject",
		"user.sub", "user.id", "metadata.user.sub", "metadata.user.id",
	)
}

func antigravityProjectID(auth HostAuthFileEntry, raw json.RawMessage) string {
	if strings.TrimSpace(auth.ProjectID) != "" {
		return strings.TrimSpace(auth.ProjectID)
	}
	return firstRawString(raw,
		"project_id", "projectId",
		"metadata.project_id", "metadata.projectId",
		"attributes.project_id", "attributes.projectId", "attributes.gemini_virtual_project",
		"installed.project_id", "installed.projectId",
		"web.project_id", "web.projectId",
	)
}

func antigravityTokenExpired(raw json.RawMessage) bool {
	for _, path := range []string{"expired", "metadata.expired"} {
		value := strings.TrimSpace(gjson.GetBytes(raw, path).String())
		if value == "" {
			continue
		}
		ts, errParse := time.Parse(time.RFC3339, value)
		if errParse == nil {
			return !ts.After(time.Now().Add(30 * time.Second))
		}
	}
	expiresIn := gjson.GetBytes(raw, "expires_in").Int()
	timestampMs := gjson.GetBytes(raw, "timestamp").Int()
	if expiresIn == 0 {
		expiresIn = gjson.GetBytes(raw, "metadata.expires_in").Int()
		timestampMs = gjson.GetBytes(raw, "metadata.timestamp").Int()
	}
	if expiresIn > 0 && timestampMs > 0 {
		return !time.UnixMilli(timestampMs).Add(time.Duration(expiresIn) * time.Second).After(time.Now().Add(30 * time.Second))
	}
	return false
}

func firstRawString(raw json.RawMessage, paths ...string) string {
	for _, path := range paths {
		if value := strings.TrimSpace(gjson.GetBytes(raw, path).String()); value != "" {
			return value
		}
	}
	return ""
}

func firstGJSONFloat(root gjson.Result, paths ...string) (float64, bool) {
	for _, path := range paths {
		if value, okValue := gjsonFloat(root.Get(path)); okValue {
			return value, true
		}
	}
	return 0, false
}

func firstGJSONResult(raw []byte, paths ...string) gjson.Result {
	for _, path := range paths {
		value := gjson.GetBytes(raw, path)
		if value.Exists() {
			return value
		}
	}
	return gjson.Result{}
}

func firstResult(root gjson.Result, paths ...string) gjson.Result {
	for _, path := range paths {
		value := root.Get(path)
		if value.Exists() {
			return value
		}
	}
	return gjson.Result{}
}

func minimumRemaining(windows []quotaWindowDetail) (float64, bool) {
	remaining := math.Inf(1)
	for _, window := range windows {
		if window.Remaining < remaining {
			remaining = window.Remaining
		}
	}
	if math.IsInf(remaining, 1) {
		return 0, false
	}
	return remaining, true
}

func firstResetAt(windows []quotaWindowDetail) string {
	for _, window := range windows {
		if strings.TrimSpace(window.ResetAt) != "" {
			return window.ResetAt
		}
	}
	return ""
}

func unixSecondsString(resetAt int64, resetAfter int64) string {
	if resetAt <= 0 && resetAfter > 0 {
		resetAt = time.Now().Unix() + resetAfter
	}
	if resetAt <= 0 {
		return ""
	}
	return time.Unix(resetAt, 0).UTC().Format(time.RFC3339)
}

func quotaFraction(value gjson.Result) (float64, bool) {
	if value.Type == gjson.Number {
		raw := value.Float()
		if raw > 1 {
			return raw / 100, true
		}
		return raw, true
	}
	text := strings.TrimSpace(value.String())
	if text == "" {
		return 0, false
	}
	if strings.HasSuffix(text, "%") {
		parsed, errParse := strconv.ParseFloat(strings.TrimSpace(strings.TrimSuffix(text, "%")), 64)
		if errParse != nil {
			return 0, false
		}
		return parsed / 100, true
	}
	parsed, errParse := strconv.ParseFloat(text, 64)
	if errParse != nil {
		return 0, false
	}
	if parsed > 1 {
		return parsed / 100, true
	}
	return parsed, true
}

func parseNumber(value string) (float64, bool) {
	parsed, errParse := strconv.ParseFloat(strings.TrimSpace(value), 64)
	return parsed, errParse == nil
}

func clampPercent(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}

func round2(value float64) float64 {
	return math.Round(value*100) / 100
}
