# cpa-account-usage

CLIProxyAPI plugin that exposes a cc-switch friendly account quota endpoint.

Repository: <https://github.com/edhnt455/cpa-plugin-account-usage>

The plugin registers:

```text
GET  /v0/resource/plugins/cpa-account-usage/api/usage
```

The endpoint is unauthenticated and returns only aggregate usage fields, without per-account email, file name, or auth index details.

## cc-switch

Set `baseUrl` to:

```text
http://127.0.0.1:8317/v0/resource/plugins/cpa-account-usage
```

Then use:

```js
({
  request: {
    url: "{{baseUrl}}/api/usage?provider=codex",
    method: "GET",
    headers: {
      "User-Agent": "cc-switch/1.0"
    }
  },
  extractor: function(response) {
    return {
      isValid: !response.error,
      remaining: response.balance,
      unit: response.unit || "accounts",
      resetAt: response.reset_at,
      usedPercent: response.used_percent
    };
  }
})
```

## Behavior

By default, the plugin automatically checks official quota endpoints for supported providers:

- Codex: `https://chatgpt.com/backend-api/wham/usage`
- xAI/Grok: `https://cli-chat-proxy.grok.com/v1/billing?format=credits`
- Kimi: `https://api.kimi.com/coding/v1/usages`
- Antigravity/Gemini: Google Antigravity quota summary endpoints

For these providers, `balance` is the remaining percentage and `unit` is `%`. Top-level responses include provider-specific details such as `used_percent`, `reset_at`, `reset_credits`, and `raw_balance` when available.

Grok responses use only the rolling 7-day quota returned by the credits endpoint. The same values are exposed at the top level and under `weekly`; the legacy 30-day billing quota is not requested or returned.

Codex and Gemini responses expose both quota windows as `five_hour` and `weekly`, including each window's remaining balance, used percentage, and reset time. For backward compatibility, the top-level `balance`, `used_percent`, and `reset_at` use the 5-hour window by default.

Quota probing matches the CPA management center: only explicitly disabled credentials are excluded. A credential that is temporarily unavailable for request routing due to an error or cooldown can still report a balance when the official quota request succeeds; `available_count` continues to report credentials currently eligible for CPA routing.

Antigravity/Gemini token refresh is optional. If CPA already exposes a valid access token, no extra config is needed. If the plugin must refresh an expired Antigravity token, set `antigravity_oauth_client_id` and `antigravity_oauth_client_secret` in the plugin config.

When a provider has no built-in quota probe, or the official probe fails:

- `balance` is the number of currently available CPA auth accounts.
- `unit` is `accounts`.
- `isValid` is true when an official quota probe succeeds or at least one matching account is available.

When a custom provider endpoint is configured, it overrides the built-in probe for that provider. The plugin reads a numeric `balance_path` from the upstream JSON response and aggregates known balances.

Response shape:

```json
{
  "isValid": true,
  "balance": 67,
  "unit": "%",
  "available_count": 1,
  "known_count": 1,
  "unknown_count": 0,
  "reset_at": "2026-07-25T08:31:56Z",
  "used_percent": 33,
  "accounts": []
}
```

Codex and Gemini response shape:

```json
{
  "isValid": true,
  "balance": 74,
  "unit": "%",
  "reset_at": "2026-08-03T12:00:00Z",
  "used_percent": 26,
  "five_hour": {
    "balance": 74,
    "unit": "%",
    "reset_at": "2026-08-03T12:00:00Z",
    "used_percent": 26
  },
  "weekly": {
    "balance": 31,
    "unit": "%",
    "reset_at": "2026-08-09T12:00:00Z",
    "used_percent": 69
  },
  "accounts": []
}
```

You may filter a check by request body or query:

```json
{"provider":"codex","auth_index":"abc123"}
```

## Build

```bash
make test
make build-linux
```

On macOS arm64:

```bash
make build-darwin
```

Copy the built shared library into CPA's `plugins.dir` with the plugin ID as basename:

```text
plugins/cpa-account-usage.so
plugins/cpa-account-usage.dylib
```

## Config

```yaml
plugins:
  enabled: true
  dir: "plugins"
  configs:
    cpa-account-usage:
      enabled: true
      priority: 10
      aggregate: "max"
      default_unit: "USD"
      status_only_unit: "accounts"
      # Optional. Only needed when refreshing expired Antigravity/Gemini tokens.
      # antigravity_oauth_client_id: ""
      # antigravity_oauth_client_secret: ""
      include_providers: ["codex", "xai", "kimi", "gemini", "antigravity"]
      # Optional custom overrides. Built-in Codex, xAI/Grok, Kimi, and
      # Antigravity/Gemini quota probes are used when no custom URL is set.
      # providers:
      #   custom-provider:
      #     method: "GET"
      #     url: "https://example.invalid/v1/billing"
      #     balance_path: "balance"
      #     unit: "USD"
      #     error_path: "error"
      #     headers:
      #       Authorization: "Bearer $TOKEN$"
```

Provider config fields:

| Field | Meaning |
|---|---|
| `url` | Absolute provider quota URL. `$TOKEN$` is replaced before calling. |
| `method` | HTTP method, default `GET`. |
| `headers` | HTTP headers. `$TOKEN$` is replaced in values. |
| `body` | Optional request body. `$TOKEN$` and `$AUTH_JSON$` are supported. |
| `balance_path` | gjson path for numeric balance in upstream JSON. |
| `unit` / `unit_path` | Static or JSON-derived unit. |
| `valid_path` | Optional JSON path that must be truthy. |
| `error_path` | Optional JSON path that indicates an upstream error if present. |
| `token_paths` | Optional auth JSON token lookup paths. |
| `token_header` / `token_prefix` | Defaults to `Authorization` / `Bearer `. |

Tokens are only used for upstream requests and are never returned in plugin responses.
