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
- `syncapigw`：把 runtime 自带的 7 个标准资源（meta/detail/invoke/schedule/callback/plugin_api_dispatch/plugin_api）和插件提供的 `definition.yaml` 同步到 BlueKing API Gateway，并发布版本。详见下文「网关同步」。
- `fetch-apigw-public-key`：拉取网关 RSA 公钥并写入本地文件，供运行时校验 `X-Bkapi-JWT`。
- `collectstatics`：兼容旧命令，在新 runtime 中是 no-op。
- `version`：输出 runtime 版本。

## 网关同步

> 这一步对应 Python 框架的 `sync_apigateway_if_changed` + `fetch_apigw_public_key` 管理命令。Go 版 runtime 之前没有真正实现，会让生产环境的 callback 因为默认 `userVerifiedRequired: true` 被网关拦截。

runtime 内嵌了 `internal/apigwsync/resources.yaml`，声明了所有标准资源的鉴权配置。其中：

- `/callback/{token}`、`/invoke/{version}`、`/schedule/{trace_id}`、`/plugin_api_dispatch`：`userVerifiedRequired: false` + `appVerifiedRequired: true` + `resourcePermissionRequired: true`（允许第三方系统通过应用凭证调用，但仍要先申请权限）。
- `/meta`、`/detail/{version}`：`userVerifiedRequired: false` + `appVerifiedRequired: true`，只校验应用身份。
- `/bk_plugin/plugin_api/`：`userVerifiedRequired: true`，沿用 Python 版策略，由插件作者自行决定是否对外暴露。

插件应用需要在仓库根目录提供 `definition.yaml`，描述网关元数据（描述、stage、grant_permissions 等）；模板使用 SDK 自带的 pongo2 上下文，可访问 `settings.BK_APIGW_NAME`、`settings.BK_APP_CODE`、`environ.<ANY_ENV>`。最小示例：

```yaml
apigateway:
  description: "示例插件"
  is_public: true
  api_type: 10
  maintainers:
    - "{{ environ.BK_APIGW_MAINTAINER | default:settings.BK_APP_CODE }}"

stages:
  - name: prod
    proxy_http:
      timeout: 60
      upstreams:
        loadbalance: roundrobin
        hosts:
          - host: "{{ environ.BK_PLUGIN_BACKEND_HOST }}"
            weight: 100

grant_permissions:
  - bk_app_code: "bk_sops"
    grant_dimension: "api"
```

把同步挂到 PaaS preRelease 钩子（spec_version 2 写法）：

```yaml
modules:
  default:
    scripts:
      pre_release_hook: bash bin/sync_apigateway.sh
```

`bin/sync_apigateway.sh` 推荐写法：

```bash
#!/usr/bin/env bash
set -euo pipefail
new-go-plugin syncapigw --definition definition.yaml
new-go-plugin fetch-apigw-public-key --out apigw.pub
```

需要的环境变量：

- `BKPAAS_APP_ID` / `BKPAAS_APP_SECRET`（PaaS 自动注入）：调用 manager API 的应用凭证。
- `BK_API_URL_TMPL`：网关地址模板，例如 `http://bkapi.example.com/api/{api_name}`。
- `BK_APIGW_NAME`（可选）：网关名，默认与 `BKPAAS_APP_ID` 相同。
- `BK_APIGW_RELEASE_VERSION`（可选）：资源版本号，默认 `v<UTC时间戳>`。
- `BK_APIGW_MAINTAINER`、`BK_PLUGIN_BACKEND_HOST` 等由 `definition.yaml` 模板使用，按需自定义。

`syncapigw` 默认 *不会* 删除网关上多余的资源。如果你确认要把网关收敛到 runtime 声明的 7 个资源，加 `--delete-unknown`。

## 当前支持范围

- 同步插件。
- 使用 `ctx.WaitPoll` 的轮询插件。
- 使用 `ctx.WaitCallback` 的外部回调插件。
- 插件完成后回调插件使用系统。
- 基于 `allow_scope` 的业务域限制。
- `/bk_plugin/plugin_api_dispatch` 和 `/bk_plugin/plugin_api/*`。
- `/bk_plugin/meta`。
- `/bk_plugin/detail/:version`。
- `/bk_plugin/invoke/:version`。
- `/bk_plugin/schedule/:trace_id`。
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
