# cpa-account-usage

面向 [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) 的账号剩余额度查询插件，接口格式兼容 cc-switch。

仓库地址：<https://github.com/edhnt455/cpa-plugin-account-usage>

插件注册接口：

```text
GET  /v0/resource/plugins/cpa-account-usage/api/usage
```

该接口不需要鉴权，只返回聚合后的余额结果，不返回账号邮箱、文件名、auth_index 等明细。

## cc-switch 配置

`baseUrl` 填：

```text
http://127.0.0.1:8317/v0/resource/plugins/cpa-account-usage
```

然后使用：

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

## 行为说明

默认会按账号类型自动请求官方额度接口：

- Codex：`https://chatgpt.com/backend-api/wham/usage`
- xAI/Grok：`https://cli-chat-proxy.grok.com/v1/billing`
- Kimi：`https://api.kimi.com/coding/v1/usages`
- Antigravity/Gemini：Google Antigravity quota summary 接口

这些 provider 的 `balance` 表示剩余百分比，`unit` 为 `%`。响应顶层会尽量返回 `reset_at`、`used_percent`、`reset_credits`、`raw_balance` 等字段。

Antigravity/Gemini 刷新 token 是可选能力。如果 CPA 已经能给插件有效 access token，不需要额外配置；只有插件需要刷新过期 Antigravity token 时，才需要在插件配置里设置 `antigravity_oauth_client_id` 和 `antigravity_oauth_client_secret`。

当 provider 没有内置官方额度接口，或官方查询失败时：

- `balance` 表示当前可用账号数量。
- `unit` 为 `accounts`。
- 至少有一个匹配账号可用时 `isValid=true`。

配置自定义 provider 余额接口后，会覆盖该 provider 的内置查询。插件会读取上游 JSON 的 `balance_path`，并聚合已知余额。

示例请求：

```bash
curl "http://127.0.0.1:8317/v0/resource/plugins/cpa-account-usage/api/usage?provider=codex"
```

`provider=grok` 会匹配 xAI 账号；`provider=gemini` 会匹配 Antigravity 账号里的 Gemini 模型额度。

## 构建

```bash
make test
make build-linux
```

macOS arm64：

```bash
make build-darwin
```

将产物复制到 CPA 的 `plugins.dir`，文件 basename 需要是插件 ID：

```text
plugins/cpa-account-usage.so
plugins/cpa-account-usage.dylib
```

配置示例见 [config.example.yaml](./config.example.yaml)。
