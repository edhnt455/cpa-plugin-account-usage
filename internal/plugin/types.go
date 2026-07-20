package plugin

import (
	"encoding/json"
	"net/http"
	"net/url"
	"time"
)

const (
	ABIVersion    uint32 = 1
	SchemaVersion uint32 = 1

	MethodPluginRegister    = "plugin.register"
	MethodPluginReconfigure = "plugin.reconfigure"

	MethodManagementRegister = "management.register"
	MethodManagementHandle   = "management.handle"

	MethodHostHTTPDo         = "host.http.do"
	MethodHostAuthList       = "host.auth.list"
	MethodHostAuthGet        = "host.auth.get"
	MethodHostAuthGetRuntime = "host.auth.get_runtime"
)

const (
	PluginID             = "cpa-account-usage"
	PluginName           = "cpa-account-usage"
	Version              = "0.3.2"
	UsageRoutePath       = "/plugins/cpa-account-usage/api/usage"
	PublicUsageRoutePath = "/api/usage"
)

type HostCaller interface {
	Call(method string, payload []byte) ([]byte, error)
}

type Envelope struct {
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *EnvelopeError  `json:"error,omitempty"`
}

type EnvelopeError struct {
	Code       string `json:"code"`
	Message    string `json:"message"`
	Retryable  bool   `json:"retryable,omitempty"`
	HTTPStatus int    `json:"http_status,omitempty"`
}

type LifecycleRequest struct {
	ConfigYAML []byte `json:"config_yaml"`
}

type Registration struct {
	SchemaVersion uint32       `json:"schema_version"`
	Metadata      Metadata     `json:"metadata"`
	Capabilities  Capabilities `json:"capabilities"`
}

type Metadata struct {
	Name             string        `json:"Name"`
	Version          string        `json:"Version"`
	Author           string        `json:"Author"`
	GitHubRepository string        `json:"GitHubRepository"`
	Logo             string        `json:"Logo,omitempty"`
	ConfigFields     []ConfigField `json:"ConfigFields"`
}

type ConfigField struct {
	Name        string   `json:"Name"`
	Type        string   `json:"Type"`
	EnumValues  []string `json:"EnumValues,omitempty"`
	Description string   `json:"Description"`
}

type Capabilities struct {
	ManagementAPI bool `json:"management_api"`
}

type ManagementRegistration struct {
	Routes    []ManagementRoute `json:"routes,omitempty"`
	Resources []ResourceRoute   `json:"resources,omitempty"`
}

type ManagementRoute struct {
	Method      string `json:"Method"`
	Path        string `json:"Path"`
	Description string `json:"Description,omitempty"`
}

type ResourceRoute struct {
	Path        string `json:"Path"`
	Menu        string `json:"Menu,omitempty"`
	Description string `json:"Description,omitempty"`
}

type ManagementRequest struct {
	Method         string
	Path           string
	Headers        http.Header
	Query          url.Values
	Body           []byte
	HostCallbackID string `json:"host_callback_id,omitempty"`
}

type ManagementResponse struct {
	StatusCode int
	Headers    http.Header
	Body       []byte
}

type PluginConfig struct {
	Enabled                       bool                      `yaml:"enabled"`
	Aggregate                     string                    `yaml:"aggregate"`
	DefaultUnit                   string                    `yaml:"default_unit"`
	StatusOnlyUnit                string                    `yaml:"status_only_unit"`
	AntigravityOAuthClientID     string                    `yaml:"antigravity_oauth_client_id"`
	AntigravityOAuthClientSecret string                    `yaml:"antigravity_oauth_client_secret"`
	IncludeProviders              []string                  `yaml:"include_providers"`
	ExcludeProviders              []string                  `yaml:"exclude_providers"`
	Providers                     map[string]ProviderConfig `yaml:"providers"`
}

type ProviderConfig struct {
	Enabled     *bool             `yaml:"enabled"`
	Method      string            `yaml:"method"`
	URL         string            `yaml:"url"`
	Headers     map[string]string `yaml:"headers"`
	Body        string            `yaml:"body"`
	BalancePath string            `yaml:"balance_path"`
	Unit        string            `yaml:"unit"`
	UnitPath    string            `yaml:"unit_path"`
	ValidPath   string            `yaml:"valid_path"`
	ErrorPath   string            `yaml:"error_path"`
	TokenPaths  []string          `yaml:"token_paths"`
	TokenHeader string            `yaml:"token_header"`
	TokenPrefix string            `yaml:"token_prefix"`
}

type UsageRequest struct {
	Provider  string `json:"provider"`
	AuthIndex string `json:"auth_index"`
}

type UsageResponse struct {
	IsValid        bool           `json:"isValid"`
	Balance        float64        `json:"balance"`
	Unit           string         `json:"unit"`
	Accounts       []AccountUsage `json:"accounts"`
	AvailableCount int            `json:"available_count"`
	KnownCount     int            `json:"known_count"`
	UnknownCount   int            `json:"unknown_count"`
	ResetAt        string         `json:"reset_at,omitempty"`
	UsedPercent    float64        `json:"used_percent,omitempty"`
	RawBalance     string         `json:"raw_balance,omitempty"`
	ResetCredits   int            `json:"reset_credits,omitempty"`
	Error          string         `json:"error,omitempty"`
}

type AccountUsage struct {
	AuthIndex      string  `json:"auth_index,omitempty"`
	Name           string  `json:"name,omitempty"`
	Provider       string  `json:"provider,omitempty"`
	Email          string  `json:"email,omitempty"`
	Status         string  `json:"status,omitempty"`
	StatusMessage  string  `json:"status_message,omitempty"`
	Available      bool    `json:"available"`
	Known          bool    `json:"known"`
	Balance        float64 `json:"balance,omitempty"`
	Unit           string  `json:"unit,omitempty"`
	Source         string  `json:"source,omitempty"`
	ResetAt        string  `json:"reset_at,omitempty"`
	UsedPercent    float64 `json:"used_percent,omitempty"`
	RawBalance     string  `json:"raw_balance,omitempty"`
	ResetCredits   int     `json:"reset_credits,omitempty"`
	NextRetryAfter string  `json:"next_retry_after,omitempty"`
	Error          string  `json:"error,omitempty"`
	Details        any     `json:"details,omitempty"`
}

type HostAuthListResponse struct {
	Files []HostAuthFileEntry `json:"files"`
}

type HostAuthFileEntry struct {
	ID             string    `json:"id,omitempty"`
	AuthIndex      string    `json:"auth_index,omitempty"`
	Name           string    `json:"name"`
	Type           string    `json:"type,omitempty"`
	Provider       string    `json:"provider,omitempty"`
	Label          string    `json:"label,omitempty"`
	Status         string    `json:"status,omitempty"`
	StatusMessage  string    `json:"status_message,omitempty"`
	Disabled       bool      `json:"disabled,omitempty"`
	Unavailable    bool      `json:"unavailable,omitempty"`
	Email          string    `json:"email,omitempty"`
	AccountType    string    `json:"account_type,omitempty"`
	Account        string    `json:"account,omitempty"`
	ProjectID      string    `json:"project_id,omitempty"`
	UserID         string    `json:"user_id,omitempty"`
	Subject        string    `json:"subject,omitempty"`
	Sub            string    `json:"sub,omitempty"`
	NextRetryAfter time.Time `json:"next_retry_after,omitempty"`
}

type HostAuthGetRequest struct {
	AuthIndex string `json:"auth_index"`
}

type HostAuthGetResponse struct {
	AuthIndex string          `json:"auth_index"`
	Name      string          `json:"name,omitempty"`
	Path      string          `json:"path,omitempty"`
	JSON      json.RawMessage `json:"json"`
}

type HostHTTPRequest struct {
	HostCallbackID string              `json:"host_callback_id,omitempty"`
	Method         string              `json:"method,omitempty"`
	URL            string              `json:"url,omitempty"`
	Headers        map[string][]string `json:"headers,omitempty"`
	Body           []byte              `json:"body,omitempty"`
}

type HostHTTPResponse struct {
	StatusCode int
	Headers    map[string][]string
	Body       []byte
}
