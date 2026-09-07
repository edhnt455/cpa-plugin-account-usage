package plugin

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/tidwall/gjson"
	"gopkg.in/yaml.v3"
)

type App struct {
	host   HostCaller
	config atomic.Value
}

func NewApp(host HostCaller) *App {
	app := &App{host: host}
	app.config.Store(DefaultConfig())
	return app
}

func (a *App) HandleMethod(method string, request []byte) ([]byte, error) {
	switch method {
	case MethodPluginRegister, MethodPluginReconfigure:
		if err := a.configure(request); err != nil {
			return nil, err
		}
		return OKEnvelope(a.registration())
	case MethodManagementRegister:
		return OKEnvelope(a.managementRegistration())
	case MethodManagementHandle:
		return a.handleManagement(request)
	default:
		return ErrorEnvelope("unknown_method", "unknown method: "+method, http.StatusNotFound), nil
	}
}

func (a *App) Shutdown() {}

func (a *App) configure(raw []byte) error {
	var req LifecycleRequest
	if len(raw) > 0 {
		if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
			return errUnmarshal
		}
	}
	cfg, errDecode := DecodeConfig(req.ConfigYAML)
	if errDecode != nil {
		return errDecode
	}
	a.config.Store(cfg)
	return nil
}

func (a *App) loadedConfig() PluginConfig {
	raw := a.config.Load()
	if cfg, ok := raw.(PluginConfig); ok {
		return cfg
	}
	return DefaultConfig()
}

func (a *App) registration() Registration {
	return Registration{
		SchemaVersion: SchemaVersion,
		Metadata: Metadata{
			Name:             PluginName,
			Version:          Version,
			Author:           "cpa-account-usage",
			GitHubRepository: "https://github.com/edhnt455/cpa-plugin-account-usage",
			ConfigFields: []ConfigField{
				{Name: "enabled", Type: "boolean", Description: "Enable or disable the usage endpoint."},
				{Name: "aggregate", Type: "enum", EnumValues: []string{"sum", "min", "max", "first"}, Description: "How known balances are aggregated."},
				{Name: "default_unit", Type: "string", Description: "Default unit for provider HTTP quota responses."},
				{Name: "status_only_unit", Type: "string", Description: "Unit used when no provider quota endpoint is configured."},
				{Name: "antigravity_oauth_client_id", Type: "string", Description: "Optional OAuth client id used only when Antigravity access tokens must be refreshed."},
				{Name: "antigravity_oauth_client_secret", Type: "string", Description: "Optional OAuth client secret used only when Antigravity access tokens must be refreshed."},
				{Name: "include_providers", Type: "array", Description: "Optional provider allow-list."},
				{Name: "exclude_providers", Type: "array", Description: "Optional provider deny-list."},
				{Name: "providers", Type: "object", Description: "Per-provider quota endpoint definitions."},
			},
		},
		Capabilities: Capabilities{ManagementAPI: true},
	}
}

func (a *App) managementRegistration() ManagementRegistration {
	return ManagementRegistration{
		Resources: []ResourceRoute{{
			Path:        PublicUsageRoutePath,
			Description: "Unauthenticated cc-switch friendly account usage endpoint.",
		}},
	}
}

func (a *App) handleManagement(raw []byte) ([]byte, error) {
	var req ManagementRequest
	if len(raw) > 0 {
		if errUnmarshal := json.Unmarshal(raw, &req); errUnmarshal != nil {
			return nil, fmt.Errorf("decode management request: %w", errUnmarshal)
		}
	}
	response, statusCode := a.runUsageRequest(req)
	if isPublicUsageRequest(req) {
		response = publicUsageResponse(response)
	}
	return OKEnvelope(JSONResponse(statusCode, response))
}

func (a *App) runUsageRequest(req ManagementRequest) (UsageResponse, int) {
	cfg := a.loadedConfig()
	if !cfg.Enabled {
		return UsageResponse{IsValid: false, Unit: cfg.StatusOnlyUnit, Error: "cpa-account-usage plugin disabled"}, http.StatusOK
	}
	filter := UsageRequest{}
	if len(req.Body) > 0 {
		_ = json.Unmarshal(req.Body, &filter)
	}
	if req.Query != nil {
		if provider := strings.TrimSpace(req.Query.Get("provider")); provider != "" {
			filter.Provider = provider
		}
		if authIndex := strings.TrimSpace(req.Query.Get("auth_index")); authIndex != "" {
			filter.AuthIndex = authIndex
		}
	}
	filter.Provider = strings.ToLower(strings.TrimSpace(filter.Provider))
	filter.AuthIndex = strings.TrimSpace(filter.AuthIndex)

	auths, errList := a.callHostAuthList()
	if errList != nil {
		return UsageResponse{IsValid: false, Unit: cfg.StatusOnlyUnit, Error: errList.Error()}, http.StatusOK
	}

	accounts := make([]AccountUsage, 0, len(auths.Files))
	for _, auth := range auths.Files {
		if !authMatchesFilter(cfg, filter, auth) {
			continue
		}
		accounts = append(accounts, a.inspectAuthUsage(cfg, filter, auth, req.HostCallbackID))
	}
	resp := AggregateAccounts(cfg, accounts)
	if len(accounts) == 0 {
		resp.Error = "no matching accounts"
	}
	if !resp.IsValid && resp.Error == "" {
		resp.Error = "no available accounts"
	}
	return resp, http.StatusOK
}

func authMatchesFilter(cfg PluginConfig, filter UsageRequest, auth HostAuthFileEntry) bool {
	if filter.AuthIndex != "" && auth.AuthIndex != filter.AuthIndex {
		return false
	}
	provider := normalizeProvider(firstNonEmpty(auth.Provider, auth.Type))
	if filter.Provider != "" && !providerMatchesFilter(provider, filter.Provider) {
		return false
	}
	if len(cfg.IncludeProviders) > 0 && !providerInList(provider, cfg.IncludeProviders) {
		return false
	}
	if providerInList(provider, cfg.ExcludeProviders) {
		return false
	}
	return true
}

func (a *App) inspectAuthUsage(cfg PluginConfig, filter UsageRequest, auth HostAuthFileEntry, hostCallbackID string) AccountUsage {
	provider := normalizeProvider(firstNonEmpty(auth.Provider, auth.Type))
	result := AccountUsage{
		AuthIndex:      auth.AuthIndex,
		Name:           auth.Name,
		Provider:       provider,
		Email:          auth.Email,
		Status:         auth.Status,
		StatusMessage:  auth.StatusMessage,
		Available:      AuthAvailable(auth),
		Known:          false,
		Unit:           cfg.StatusOnlyUnit,
		NextRetryAfter: timeString(auth.NextRetryAfter),
	}
	providerCfg, okProvider := cfg.Providers[provider]
	if okProvider && !providerConfigEnabled(providerCfg) {
		return result
	}
	// Match the management center's quota behavior: a credential may be
	// temporarily unavailable for request routing while its quota endpoint is
	// still readable. Only credentials that are explicitly disabled should be
	// excluded from usage probes.
	if !AuthUsageProbeAllowed(auth) {
		return result
	}
	if !okProvider || providerCfg.URL == "" {
		official, okOfficial := a.fetchOfficialBalance(provider, filter.Provider, auth, hostCallbackID)
		if !okOfficial {
			return result
		}
		if official.Error != "" {
			result.Error = official.Error
			return result
		}
		result.Known = true
		result.Balance = official.Balance
		result.Unit = official.Unit
		result.Source = official.Source
		result.ResetAt = official.ResetAt
		result.UsedPercent = official.UsedPercent
		result.RawBalance = official.RawBalance
		result.ResetCredits = official.ResetCredits
		result.FiveHour = official.FiveHour
		result.Weekly = official.Weekly
		result.Details = official.Details
		return result
	}
	balance, unit, errCheck := a.fetchProviderBalance(providerCfg, auth, hostCallbackID, cfg.DefaultUnit)
	if errCheck != nil {
		result.Error = errCheck.Error()
		return result
	}
	result.Known = true
	result.Balance = balance
	result.Unit = unit
	return result
}

func (a *App) fetchProviderBalance(cfg ProviderConfig, auth HostAuthFileEntry, hostCallbackID string, defaultUnit string) (float64, string, error) {
	token, rawAuthJSON, errToken := a.tokenForAuth(auth.AuthIndex, cfg.TokenPaths)
	if errToken != nil {
		return 0, "", errToken
	}
	headers := map[string][]string{}
	for key, value := range cfg.Headers {
		headers[key] = []string{replaceToken(value, token)}
	}
	if token != "" && headerValue(headers, cfg.TokenHeader) == "" {
		headers[cfg.TokenHeader] = []string{cfg.TokenPrefix + token}
	}
	requestURL := replaceToken(cfg.URL, token)
	requestBody := []byte(replaceToken(cfg.Body, token))
	if strings.Contains(requestURL, "$AUTH_JSON$") || strings.Contains(string(requestBody), "$AUTH_JSON$") {
		encoded := url.QueryEscape(string(rawAuthJSON))
		requestURL = strings.ReplaceAll(requestURL, "$AUTH_JSON$", encoded)
		requestBody = []byte(strings.ReplaceAll(string(requestBody), "$AUTH_JSON$", string(rawAuthJSON)))
	}

	result, errCall := a.callHost(MethodHostHTTPDo, HostHTTPRequest{
		HostCallbackID: hostCallbackID,
		Method:         cfg.Method,
		URL:            requestURL,
		Headers:        headers,
		Body:           requestBody,
	})
	if errCall != nil {
		return 0, "", errCall
	}
	var httpResp HostHTTPResponse
	if errUnmarshal := json.Unmarshal(result, &httpResp); errUnmarshal != nil {
		return 0, "", fmt.Errorf("decode host.http.do result: %w", errUnmarshal)
	}
	if httpResp.StatusCode < http.StatusOK || httpResp.StatusCode >= http.StatusMultipleChoices {
		return 0, "", fmt.Errorf("upstream status %d: %s", httpResp.StatusCode, shortBody(httpResp.Body))
	}
	if cfg.ErrorPath != "" {
		if value := gjson.GetBytes(httpResp.Body, cfg.ErrorPath); value.Exists() && !isEmptyGJSON(value) {
			return 0, "", fmt.Errorf("upstream error: %s", value.String())
		}
	}
	if cfg.ValidPath != "" {
		if value := gjson.GetBytes(httpResp.Body, cfg.ValidPath); value.Exists() && !gjsonTruthy(value) {
			return 0, "", fmt.Errorf("upstream marked account invalid")
		}
	}
	if cfg.BalancePath == "" {
		return 0, "", fmt.Errorf("balance_path is required for provider endpoint")
	}
	value := gjson.GetBytes(httpResp.Body, cfg.BalancePath)
	if !value.Exists() {
		return 0, "", fmt.Errorf("balance path %q not found", cfg.BalancePath)
	}
	balance, okBalance := gjsonFloat(value)
	if !okBalance {
		return 0, "", fmt.Errorf("balance path %q is not numeric", cfg.BalancePath)
	}
	unit := firstNonEmpty(cfg.Unit, defaultUnit)
	if cfg.UnitPath != "" {
		if unitValue := strings.TrimSpace(gjson.GetBytes(httpResp.Body, cfg.UnitPath).String()); unitValue != "" {
			unit = unitValue
		}
	}
	return balance, unit, nil
}

func (a *App) tokenForAuth(authIndex string, configuredPaths []string) (string, json.RawMessage, error) {
	resp, errGet := a.callHostAuthGet(authIndex)
	if errGet != nil {
		return "", nil, errGet
	}
	paths := configuredPaths
	if len(paths) == 0 {
		paths = []string{
			"access_token",
			"api_key",
			"token",
			"id_token",
			"cookie",
			"metadata.access_token",
			"metadata.api_key",
			"metadata.token",
			"metadata.id_token",
			"attributes.api_key",
		}
	}
	for _, path := range paths {
		if token := strings.TrimSpace(gjson.GetBytes(resp.JSON, strings.TrimSpace(path)).String()); token != "" {
			return token, resp.JSON, nil
		}
	}
	return "", resp.JSON, fmt.Errorf("auth token not found for auth_index %s", authIndex)
}

func (a *App) callHostAuthList() (HostAuthListResponse, error) {
	result, errCall := a.callHost(MethodHostAuthList, map[string]any{})
	if errCall != nil {
		return HostAuthListResponse{}, errCall
	}
	var resp HostAuthListResponse
	if errUnmarshal := json.Unmarshal(result, &resp); errUnmarshal != nil {
		return HostAuthListResponse{}, fmt.Errorf("decode host.auth.list result: %w", errUnmarshal)
	}
	return resp, nil
}

func (a *App) callHostAuthGet(authIndex string) (HostAuthGetResponse, error) {
	result, errCall := a.callHost(MethodHostAuthGet, HostAuthGetRequest{AuthIndex: authIndex})
	if errCall != nil {
		return HostAuthGetResponse{}, errCall
	}
	var resp HostAuthGetResponse
	if errUnmarshal := json.Unmarshal(result, &resp); errUnmarshal != nil {
		return HostAuthGetResponse{}, fmt.Errorf("decode host.auth.get result: %w", errUnmarshal)
	}
	return resp, nil
}

func (a *App) callHost(method string, payload any) (json.RawMessage, error) {
	if a == nil || a.host == nil {
		return nil, fmt.Errorf("host callback unavailable")
	}
	rawPayload, errMarshal := json.Marshal(payload)
	if errMarshal != nil {
		return nil, fmt.Errorf("marshal host callback payload %s: %w", method, errMarshal)
	}
	rawResponse, errCall := a.host.Call(method, rawPayload)
	if errCall != nil {
		return nil, errCall
	}
	var env Envelope
	if errUnmarshal := json.Unmarshal(rawResponse, &env); errUnmarshal != nil {
		return nil, fmt.Errorf("decode host callback envelope %s: %w", method, errUnmarshal)
	}
	if !env.OK {
		if env.Error != nil {
			return nil, fmt.Errorf("%s: %s", env.Error.Code, env.Error.Message)
		}
		return nil, fmt.Errorf("host callback %s failed", method)
	}
	return append(json.RawMessage(nil), env.Result...), nil
}

func DefaultConfig() PluginConfig {
	return PluginConfig{
		Enabled:        true,
		Aggregate:      "sum",
		DefaultUnit:    "USD",
		StatusOnlyUnit: "accounts",
	}
}

func DecodeConfig(raw []byte) (PluginConfig, error) {
	cfg := DefaultConfig()
	if len(raw) > 0 {
		if errUnmarshal := yaml.Unmarshal(raw, &cfg); errUnmarshal != nil {
			return PluginConfig{}, errUnmarshal
		}
	}
	cfg.Aggregate = strings.ToLower(strings.TrimSpace(cfg.Aggregate))
	if cfg.Aggregate == "" {
		cfg.Aggregate = "sum"
	}
	cfg.DefaultUnit = strings.TrimSpace(cfg.DefaultUnit)
	if cfg.DefaultUnit == "" {
		cfg.DefaultUnit = "USD"
	}
	cfg.StatusOnlyUnit = strings.TrimSpace(cfg.StatusOnlyUnit)
	if cfg.StatusOnlyUnit == "" {
		cfg.StatusOnlyUnit = "accounts"
	}
	cfg.IncludeProviders = normalizeProviderList(cfg.IncludeProviders)
	cfg.ExcludeProviders = normalizeProviderList(cfg.ExcludeProviders)
	normalizedProviders := make(map[string]ProviderConfig, len(cfg.Providers))
	for provider, providerCfg := range cfg.Providers {
		providerCfg.Method = strings.ToUpper(strings.TrimSpace(providerCfg.Method))
		if providerCfg.Method == "" {
			providerCfg.Method = http.MethodGet
		}
		providerCfg.URL = strings.TrimSpace(providerCfg.URL)
		providerCfg.BalancePath = strings.TrimSpace(providerCfg.BalancePath)
		providerCfg.Unit = strings.TrimSpace(providerCfg.Unit)
		providerCfg.UnitPath = strings.TrimSpace(providerCfg.UnitPath)
		providerCfg.ValidPath = strings.TrimSpace(providerCfg.ValidPath)
		providerCfg.ErrorPath = strings.TrimSpace(providerCfg.ErrorPath)
		providerCfg.TokenHeader = strings.TrimSpace(providerCfg.TokenHeader)
		if providerCfg.TokenHeader == "" {
			providerCfg.TokenHeader = "Authorization"
		}
		if providerCfg.TokenPrefix == "" {
			providerCfg.TokenPrefix = "Bearer "
		}
		provider = strings.ToLower(strings.TrimSpace(provider))
		if provider != "" {
			normalizedProviders[provider] = providerCfg
		}
	}
	cfg.Providers = normalizedProviders
	return cfg, nil
}

func AggregateAccounts(cfg PluginConfig, accounts []AccountUsage) UsageResponse {
	resp := UsageResponse{Accounts: accounts, Unit: cfg.StatusOnlyUnit}
	units := map[string]struct{}{}
	for _, account := range accounts {
		if account.Available {
			resp.AvailableCount++
			resp.IsValid = true
		}
		if account.Known {
			resp.KnownCount++
			resp.IsValid = true
			units[account.Unit] = struct{}{}
			mode := cfg.Aggregate
			if account.Unit == "%" && (mode == "" || mode == "sum") {
				mode = "max"
			}
			if aggregateSelectsAccount(mode, resp.Balance, account.Balance, resp.KnownCount) {
				applyAccountSummary(&resp, account)
			}
			resp.Balance = aggregateValue(mode, resp.Balance, account.Balance, resp.KnownCount)
			continue
		}
		resp.UnknownCount++
	}
	if resp.KnownCount == 0 {
		resp.Balance = float64(resp.AvailableCount)
		resp.Unit = cfg.StatusOnlyUnit
		return resp
	}
	if len(units) == 1 {
		for unit := range units {
			resp.Unit = unit
		}
	} else {
		resp.Unit = "mixed"
	}
	return resp
}

func aggregateSelectsAccount(mode string, current, next float64, count int) bool {
	switch mode {
	case "min":
		return count == 1 || next < current
	case "max":
		return count == 1 || next > current
	default:
		return count == 1
	}
}

func applyAccountSummary(resp *UsageResponse, account AccountUsage) {
	resp.ResetAt = account.ResetAt
	resp.UsedPercent = account.UsedPercent
	resp.RawBalance = account.RawBalance
	resp.ResetCredits = account.ResetCredits
	resp.FiveHour = account.FiveHour
	resp.Weekly = account.Weekly
}

func isPublicUsageRequest(req ManagementRequest) bool {
	path := strings.TrimRight(strings.TrimSpace(req.Path), "/")
	return strings.Contains(path, "/v0/resource/plugins/") && strings.HasSuffix(path, PublicUsageRoutePath)
}

func publicUsageResponse(resp UsageResponse) UsageResponse {
	resp.Accounts = []AccountUsage{}
	return resp
}

func AuthAvailable(auth HostAuthFileEntry) bool {
	if auth.Disabled || auth.Unavailable {
		return false
	}
	if !auth.NextRetryAfter.IsZero() && auth.NextRetryAfter.After(time.Now()) {
		return false
	}
	status := strings.ToLower(strings.TrimSpace(auth.Status))
	return status == "" || status == "active" || status == "ok"
}

func AuthUsageProbeAllowed(auth HostAuthFileEntry) bool {
	if auth.Disabled {
		return false
	}
	return strings.ToLower(strings.TrimSpace(auth.Status)) != "disabled"
}

func OKEnvelope(v any) ([]byte, error) {
	raw, errMarshal := json.Marshal(v)
	if errMarshal != nil {
		return nil, errMarshal
	}
	return json.Marshal(Envelope{OK: true, Result: raw})
}

func ErrorEnvelope(code, message string, httpStatus int) []byte {
	raw, _ := json.Marshal(Envelope{OK: false, Error: &EnvelopeError{Code: code, Message: message, HTTPStatus: httpStatus}})
	return raw
}

func JSONResponse(statusCode int, body any) ManagementResponse {
	raw, errMarshal := json.Marshal(body)
	if errMarshal != nil {
		raw = []byte(`{"isValid":false,"balance":0,"unit":"accounts","error":"failed to encode response"}`)
	}
	return ManagementResponse{
		StatusCode: statusCode,
		Headers: http.Header{
			"content-type": []string{"application/json; charset=utf-8"},
		},
		Body: raw,
	}
}

func normalizeProviderList(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = normalizeProvider(value)
		if value != "" {
			out = append(out, value)
		}
	}
	return out
}

func providerConfigEnabled(cfg ProviderConfig) bool {
	return cfg.Enabled == nil || *cfg.Enabled
}

func aggregateValue(mode string, current, next float64, count int) float64 {
	switch mode {
	case "min":
		if count == 1 || next < current {
			return next
		}
		return current
	case "max":
		if count == 1 || next > current {
			return next
		}
		return current
	case "first":
		if count == 1 {
			return next
		}
		return current
	default:
		return current + next
	}
}

func replaceToken(value string, token string) string {
	return strings.ReplaceAll(value, "$TOKEN$", token)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func stringInSlice(value string, list []string) bool {
	for _, item := range list {
		if item == value {
			return true
		}
	}
	return false
}

func normalizeProvider(provider string) string {
	key := strings.ToLower(strings.TrimSpace(provider))
	key = strings.ReplaceAll(key, "_", "-")
	switch key {
	case "grok", "x-ai":
		return "xai"
	default:
		return key
	}
}

func providerMatchesFilter(provider string, filter string) bool {
	provider = normalizeProvider(provider)
	filter = normalizeProvider(filter)
	if provider == filter {
		return true
	}
	if filter == "gemini" && provider == "antigravity" {
		return true
	}
	return false
}

func providerInList(provider string, list []string) bool {
	for _, item := range list {
		if providerMatchesFilter(provider, item) || providerMatchesFilter(item, provider) {
			return true
		}
	}
	return false
}

func timeString(ts time.Time) string {
	if ts.IsZero() {
		return ""
	}
	return ts.UTC().Format(time.RFC3339)
}

func shortBody(body []byte) string {
	text := strings.TrimSpace(string(body))
	if len(text) > 240 {
		return text[:240]
	}
	return text
}

func headerValue(headers map[string][]string, key string) string {
	for candidate, values := range headers {
		if strings.EqualFold(candidate, key) && len(values) > 0 {
			return values[0]
		}
	}
	return ""
}

func isEmptyGJSON(value gjson.Result) bool {
	switch value.Type {
	case gjson.Null, gjson.False:
		return true
	case gjson.String:
		return strings.TrimSpace(value.String()) == ""
	default:
		return false
	}
}

func gjsonTruthy(value gjson.Result) bool {
	switch value.Type {
	case gjson.True:
		return true
	case gjson.False, gjson.Null:
		return false
	case gjson.Number:
		return value.Float() != 0
	case gjson.String:
		raw := strings.ToLower(strings.TrimSpace(value.String()))
		return raw == "true" || raw == "ok" || raw == "valid" || raw == "active" || raw == "1"
	default:
		return value.Exists()
	}
}

func gjsonFloat(value gjson.Result) (float64, bool) {
	if value.Type == gjson.Number {
		return value.Float(), true
	}
	raw := strings.TrimSpace(value.String())
	if raw == "" {
		return 0, false
	}
	parsed, errParse := strconv.ParseFloat(raw, 64)
	return parsed, errParse == nil
}
