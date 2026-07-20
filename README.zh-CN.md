# cpa-account-usage

面向 [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI) 的账号可用性 / 余额查询插件，接口格式兼容 cc-switch。

仓库地址：<https://github.com/edhnt455/cpa-plugin-account-usage>

插件注册接口：

```text
POST /v0/management/plugins/cpa-account-usage/api/usage
GET  /v0/management/plugins/cpa-account-usage/api/usage
```

该接口仍使用 CPA Management API 鉴权。

## cc-switch 配置

`baseUrl` 填：

```text
http://127.0.0.1:8317/v0/management/plugins/cpa-account-usage
```

然后使用：

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

## 行为说明

默认不配置上游余额接口时：

- `balance` 表示当前可用账号数量。
- `unit` 为 `accounts`。
- 至少有一个匹配账号可用时 `isValid=true`。

配置 provider 余额接口后，插件会读取上游 JSON 的 `balance_path`，并聚合已知余额。

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
