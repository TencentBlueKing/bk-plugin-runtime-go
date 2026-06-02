# 从 beego-runtime 迁移到 bk-plugin-runtime-go

## 目标

本文说明已有 Go 插件如何从 `beego-runtime` 迁移到基于 blueapps-go 的 `bk-plugin-runtime-go`。

Phase 1 的目标是优先兼容已有同步插件和 `WaitPoll` 轮询插件，尽量不改插件业务逻辑。Phase 2/3 继续补齐 Python 插件开发框架已有的 callback、完成回调、allow scope、plugin API dispatch 能力，并将旧 `beego-runtime` 标记为废弃。

## 最小代码改动

修改 `main.go` 里的 runtime import：

```diff
- "github.com/TencentBlueKing/beego-runtime/runner"
+ "github.com/TencentBlueKing/bk-plugin-runtime-go/runner"
```

插件业务代码保持不变：

```go
func (p MyPlugin) Execute(ctx *kit.Context) error {
    return nil
}
```

旧的插件注册方式继续可用：

```go
hub.MustInstall(MyPlugin{}, ContextInputs{}, Outputs{}, inputsForm)
```

## 依赖调整

添加新的 runtime module：

```bash
go get github.com/TencentBlueKing/bk-plugin-runtime-go@v0.1.2
go mod tidy
```

`bk-plugin-framework-go` 仍然是插件开发 SDK，`bk-plugin-runtime-go` 只负责运行时。

## 进程命令

现有 `app_desc.yml` 里的命令形态保持兼容：

```yaml
processes:
  web:
    command: ./plugin server
  worker:
    command: ./plugin worker
```

Phase 1 runtime 支持这些命令：

- `server`：启动插件 HTTP 服务。
- `worker`：启动 poll 调度 worker。
- `syncapigw`：把 runtime 自带的 5 个标准资源（callback/invoke/plugin_api/openapi/plugin_api_dispatch）和插件提供的 `definition.yaml` 同步到 BlueKing API Gateway，并发布版本。详见下文「网关同步」。
- `fetch-apigw-public-key`：拉取网关 RSA 公钥并写入本地文件，供运行时校验 `X-Bkapi-JWT`。
- `collectstatics`：兼容旧命令，在新 runtime 中是 no-op。
- `version`：输出 runtime 版本。

## 网关同步

> 这一步对应 Python 框架的 `sync_apigateway_if_changed` + `fetch_apigw_public_key` 管理命令。Go 版 runtime 之前没有真正实现，会让生产环境的 callback 因为默认 `userVerifiedRequired: true` 被网关拦截。
>
> v0.2.2 起 runtime 的资源清单、环境变量、stage 推导逻辑全部对齐 `bk-plugin-framework-python`，迁移现有 Python 插件运行时无需重新学一套约定，PaaS 注入的环境变量也能直接复用。

### 内嵌资源清单（5 个，与 Python 完全一致）

runtime 内嵌了 `internal/apigwsync/resources.yaml`，对应 Python 框架的 `bk_plugin_framework/services/bpf_service/management/commands/support-files/resources.yaml`：

| 路径 | 鉴权 | 说明 |
| --- | --- | --- |
| `/callback/{token}/` | `userVerifiedRequired=false`、`appVerifiedRequired=true`、`resourcePermissionRequired=true` | 第三方系统通知插件异步任务已完成 |
| `/invoke/{version}/` | 同上 | 调用方调用指定版本插件 |
| `/bk_plugin/plugin_api/` | `userVerifiedRequired=true`，`matchSubpath=true` | 插件自定义 API（前端调用） |
| `/bk_plugin/openapi/` | `userVerifiedRequired=false`、`appVerifiedRequired=true`，`matchSubpath=true` | 插件 OpenAPI（第三方系统调用） |
| `/plugin_api_dispatch` | `userVerifiedRequired=false`、`appVerifiedRequired=true`、`resourcePermissionRequired=true` | 插件 API 分发 |

> Plugin runtime 的 `/meta`、`/detail/{version}`、`/schedule/{trace_id}` 是 SOPS / 平台的内部直连接口，**不会**注册到网关，与 Python 版策略保持一致。

资源 YAML 内嵌的是 pongo2 模板，runtime 在调用 SDK 之前会用同一份 `settings`/`environ` 上下文先渲染再上送，因此当应用部署在子路径下（`BKPAAS_DEFAULT_PREALLOCATED_URLS` 的 path 段非空）时，`backend.path` 会自动加上前缀。

### 环境变量约定

完全对齐 `bk_plugin_runtime/config/default.py`：

| 用途 | 环境变量 | 默认值 |
| --- | --- | --- |
| 网关名 | `BKPAAS_BK_PLUGIN_APIGW_NAME`（首选）→ `BK_APIGW_NAME` → `BKPAAS_APP_ID` | 空（必须有一个） |
| 应用凭证 | `BKPAAS_APP_ID` / `BKPAAS_APP_SECRET` | 空 |
| Manager 端点模板 | `BK_APIGW_MANAGER_URL_TMPL` → `BK_APIGW_MANAGER_URL_TEMPL` → `BK_API_URL_TMPL` | 空 |
| Stage 名 | 由 `BKPAAS_ENVIRONMENT` 推：`stag` → `stag`，其他 → `prod`；可被 `BK_APIGW_STAGE_NAME` 覆盖 | `prod` |
| Stage 后端 | 解析 `BKPAAS_DEFAULT_PREALLOCATED_URLS[<env>]` 得到 host / sub_path / scheme | 空（host 由 `definition.yaml` 兜底） |
| 维护人列表 | `BK_APIGW_MAINTAINERS`（逗号分隔） | `admin` |
| 是否公开 | `BK_APIGW_IS_PUBLIC` | `true` |
| api_type | `BK_APIGW_IS_OFFICIAL`：`true` → `1`，否则 `10` | `10` |
| 资源版本 | `BK_APIGW_RELEASE_VERSION` + `+<UTC时间戳>` | `1.0.0+20260527181542`（每次部署自动唯一，避免"版本已存在"4xx；想要确定性版本号可让 `BK_APIGW_RELEASE_VERSION` 自带 build metadata，例如 `1.2.3+abc123`） |
| Stage 默认超时 | `BK_APIGW_DEFAULT_TIMEOUT`（秒） | `60` |

`definition.yaml` / `resources.yaml` 的 pongo2 上下文会注入：

- `settings.BK_APIGW_NAME`、`settings.BK_APP_CODE`、`settings.BK_APP_SECRET`
- `settings.BK_APIGW_STAGE_NAME`
- `settings.BK_APIGW_MAINTAINERS`（`[]string`，可直接 `{% for %}`）
- `settings.BK_APIGW_IS_PUBLIC`（bool）
- `settings.BK_APIGW_IS_OFFICIAL`（int 1 / 10）
- `settings.BK_APIGW_DEFAULT_TIMEOUT`（int）
- `settings.BK_PLUGIN_APIGW_BACKEND_HOST` / `BACKEND_NETLOC` / `BACKEND_SUB_PATH` / `BACKEND_SCHEME`
- `environ.<ANY_ENV_VAR>`（任意环境变量原值）

### `definition.yaml`：默认走内嵌模板

runtime 内嵌了一份默认 `definition.yaml`（位于 `internal/apigwsync/definition.yaml`），默认行为与 Python 框架的 `apigw_manager` 一致：

- 维护人取自 `BK_APIGW_MAINTAINERS`
- `is_public` / `api_type` / `stage` / 后端 host 全部从环境变量推导
- `grant_permissions` 默认放行 `bk_sops`，保证 SOPS 调用插件的链路开箱即用

**绝大多数插件不需要在仓库里写 `definition.yaml`。** 只有当你需要对网关元数据做定制（例如多个 stage、给 `bk_itsm` 等其它调用方授权、改 description）时，才在仓库根目录提供 `definition.yaml`，runtime 会用它**完全覆盖**内嵌模板。该文件可以使用同一套 `settings.*` / `environ.*` 上下文：

```yaml
apigateway:
  description: "示例插件"
  is_public: {% if settings.BK_APIGW_IS_PUBLIC %}true{% else %}false{% endif %}
  api_type: {{ settings.BK_APIGW_IS_OFFICIAL }}
  maintainers:
{% for m in settings.BK_APIGW_MAINTAINERS %}    - "{{ m }}"
{% endfor %}

stages:
  - name: "{{ settings.BK_APIGW_STAGE_NAME }}"
    proxy_http:
      timeout: {{ settings.BK_APIGW_DEFAULT_TIMEOUT }}
      upstreams:
        loadbalance: roundrobin
        hosts:
          - host: "{{ settings.BK_PLUGIN_APIGW_BACKEND_HOST }}"
            weight: 100

grant_permissions:
  - bk_app_code: "bk_sops"
    grant_dimension: "api"
  - bk_app_code: "bk_itsm"
    grant_dimension: "api"
```

> Stage backend host 强制从 `BKPAAS_DEFAULT_PREALLOCATED_URLS` 推导。如果该变量不存在或不可解析，runtime 会在调用 manager API **之前**直接报错 `stage backend host is empty`，避免 server 端给出不清晰的 4xx。

### PaaS 钩子

```yaml
modules:
  default:
    scripts:
      pre_release_hook: bash bin/sync_apigateway.sh
```

`bin/sync_apigateway.sh`：

```bash
#!/usr/bin/env bash
set -euo pipefail
new-go-plugin syncapigw                    # 默认走内嵌 definition.yaml；插件根目录有 definition.yaml 时自动覆盖
new-go-plugin fetch-apigw-public-key       # 默认输出 bin/apigw.pub，与 Python 框架一致
```

`syncapigw` 默认 *不会* 删除网关上多余的资源。如果你确认要把网关收敛到 runtime 声明的 5 个资源，加 `--delete-unknown`。

## 当前支持范围

- 同步插件。
- 使用 `ctx.WaitPoll` 的轮询插件。
- 使用 `ctx.WaitCallback` 的外部回调插件。
- 插件完成后回调插件使用系统。
- 基于 `allow_scope` 的业务域限制。
- `/bk_plugin/plugin_api_dispatch` 和 `/bk_plugin/plugin_api/*`、`/bk_plugin/openapi/*`。
- `/bk_plugin/meta`、`/bk_plugin/detail/:version`、`/bk_plugin/invoke/:version`、`/bk_plugin/schedule/:trace_id`（runtime 内部接口，不走网关）。
- 基于数据库持久化的 schedule 状态。

## 外部 callback 插件

插件希望等待第三方系统回调后继续执行时，可以在第一次执行中调用：

```go
func (p MyPlugin) Execute(ctx *kit.Context) error {
    if ctx.InvokeCount() == 1 {
        callback, err := ctx.PrepareCallback(30 * time.Minute)
        if err != nil {
            return err
        }
        // 将 callback.URL 传给第三方系统，第三方任务完成后 POST 这个地址。
        _ = callback.URL
        ctx.WaitCallback(30 * time.Minute)
        return nil
    }

    var payload struct {
        Result bool `json:"result"`
    }
    if err := ctx.ReadCallback(&payload); err != nil {
        return err
    }
    return ctx.WriteOutputs(payload)
}
```

`ctx.PrepareCallback` 会提前生成 callback URL，便于插件传给第三方系统；runtime 也会在 `invoke` 响应中返回同一个 `callback_url`。第三方系统向该 URL `POST` JSON 后，worker 会重新拉起插件，并通过 `ctx.ReadCallback` 读取回调数据。

相关环境变量：

- `BK_PLUGIN_CALLBACK_TOKEN_SECRET`：callback token 签名密钥。
- `BK_PLUGIN_CALLBACK_BASE_URL`：对外展示的 callback URL 前缀；未配置时会优先根据本次 `invoke` 请求的 Host 和协议推导。

## 插件完成回调

如果插件使用系统希望减少轮询，可以在 context inputs 中传入：

```json
{
  "plugin_callback_info": {
    "url": "https://example.com/plugin/finish_callback",
    "data": {
      "task_id": "123"
    }
  }
}
```

插件应用需要在启动时打开完成回调：

```go
hub.Configure(hub.Options{
    EnablePluginCallback: true,
})
```

插件进入 `SUCCESS` 或 `FAIL` 后，runtime 会向 `plugin_callback_info.url` `POST` 其中的 `data`。回调失败只记录日志，不改变插件任务终态。

## allow scope

如果只允许某个插件使用系统的特定业务域调用插件，可以配置：

```go
hub.Configure(hub.Options{
    AllowScope: hub.AllowScope{
        "bk_sops": {Type: "project", Value: []string{"1", "2"}},
    },
})
```

当调用方 app code 命中 `AllowScope` 时，runtime 会检查请求头：

- `X-Bkapi-App-Code`：调用方 app code。
- `Bkplugin-Scope-Type`：业务域类型。
- `Bkplugin-Scope-Value`：业务域值。

未配置 allow scope，或调用方 app code 未出现在 allow scope 中时，默认放行，保持 Python 版语义。

## plugin API dispatch

插件应用可以通过 framework 的 `pluginapi` 注册自定义 API，业务代码不需要依赖 Gin：

```go
import (
    "encoding/json"
    "net/http"

    "github.com/TencentBlueKing/bk-plugin-framework-go/pluginapi"
)

func init() {
    pluginapi.Register(func(router pluginapi.Router) {
        router.POST("/echo", func(w http.ResponseWriter, r *http.Request) {
            _ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
        })
        router.GET("/tasks/:id", func(w http.ResponseWriter, r *http.Request) {
            _ = json.NewEncoder(w).Encode(map[string]string{"id": pluginapi.Param(r, "id")})
        })
    })
}
```

调用方通过 `/bk_plugin/plugin_api_dispatch` 分发：

```json
{
  "url": "/bk_plugin/plugin_api/echo",
  "method": "post",
  "username": "admin",
  "data": {
    "value": 1
  }
}
```

dispatch 只允许转发到 `/bk_plugin/plugin_api/` 前缀下的路由，并复用 allow scope 鉴权。

## 需要手动迁移的情况

以下用法不能自动无缝迁移：

- 插件代码直接 import Beego。
- 插件代码直接 import `beego-runtime` 内部包。
- 使用 Beego controller 实现自定义 plugin API。
- 依赖旧 debug panel 的页面细节。
- 依赖旧 runtime 未文档化的响应字段或副作用。

这些场景需要按新 runtime 的公开 API 逐步迁移。
