# cpa-account-usage

CLIProxyAPI plugin that exposes a cc-switch friendly account availability / balance endpoint.

Repository: <https://github.com/edhnt455/cpa-plugin-account-usage>

The plugin registers:

```text
POST /v0/management/plugins/cpa-account-usage/api/usage
GET  /v0/management/plugins/cpa-account-usage/api/usage
```

Management API authentication is still required.

## cc-switch

Set `baseUrl` to:

```text
http://127.0.0.1:8317/v0/management/plugins/cpa-account-usage
```

Then use:

```js
({
  request: {
    url: "{{baseUrl}}/api/usage",
    method: "POST",
    headers: {
      "Authorization": "Bearer {{apiKey}}",
      "User-Agent": "cc-switch/1.0"
    }
  },
  extractor: function(response) {
    return {
      isValid: !response.error,
      remaining: response.balance,
      unit: response.unit || "accounts"
    };
  }
})
```

## Behavior

By default, no upstream quota endpoint is configured. In that mode:

- `balance` is the number of currently available CPA auth accounts.
- `unit` is `accounts`.
- `isValid` is true when at least one matching account is available.

When a provider endpoint is configured, the plugin reads a numeric `balance_path` from the upstream JSON response and aggregates known balances.

Response shape:

```json
{
  "isValid": true,
  "balance": 2,
  "unit": "accounts",
  "available_count": 2,
  "known_count": 0,
  "unknown_count": 2,
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
      aggregate: "sum"
      default_unit: "USD"
      status_only_unit: "accounts"
      include_providers: ["codex", "xai", "gemini", "antigravity"]
      providers:
        xai:
          method: "GET"
          url: "https://example.invalid/v1/billing"
          balance_path: "balance"
          unit: "USD"
          error_path: "error"
          headers:
            Authorization: "Bearer $TOKEN$"
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
