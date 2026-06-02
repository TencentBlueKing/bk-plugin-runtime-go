# bk-plugin-runtime-go

蓝鲸标准运维（bk-sops）Go 版插件运行时。负责插件进程的 HTTP 服务、异步调度、外部回调、完成回调和 API 网关同步，是 [bk-plugin-framework-go](https://github.com/TencentBlueKing/bk-plugin-framework-go) 的配套运行时。

> Python 版对应项目：[bk-plugin-framework-python](https://github.com/TencentBlueKing/bk-plugin-framework-python)（其运行时内嵌在框架中）。

## 功能特性

| 特性 | 说明 |
|---|---|
| 同步插件 | 插件 `Execute` 直接返回，runtime 同步响应调用方 |
| 轮询插件 | `ctx.WaitPoll(interval)` — worker 按间隔重复拉起插件 |
| 外部回调插件 | `ctx.WaitCallback(timeout)` — 等待第三方系统 POST callback URL |
| 完成回调 | 插件终态后自动向调用方提供的 URL 发送通知 |
| allow scope | 限制可调用插件的业务域 |
| plugin_api_dispatch | 将请求转发到插件自定义 API 端点 |
| APIGW 同步 | 内嵌标准资源配置，一键同步到蓝鲸 API 网关 |

## 与 bk-plugin-framework-go 的关系

```
┌─────────────────────────────────┐
│   插件业务代码（用户编写）         │
│   import bk-plugin-framework-go │  ← 插件 SDK：kit.Context / hub / executor
└────────────┬────────────────────┘
             │ runner.Run()
┌────────────▼────────────────────┐
│   bk-plugin-runtime-go          │  ← 本项目：HTTP server / worker / APIGW sync
│   import bk-plugin-runtime-go   │
└─────────────────────────────────┘
```

- **bk-plugin-framework-go**：插件开发 SDK，提供 `kit.Context`、`hub`、`executor` 等。
- **bk-plugin-runtime-go**：运行时，负责 HTTP 服务、数据库持久化、worker 调度、APIGW 同步。插件 `main.go` 只需调用 `runner.Run()` 即可启动全部能力。

## 快速开始

### 1. 在插件项目中引入

```go
// main.go
import (
    "github.com/TencentBlueKing/bk-plugin-framework-go/hub"
    "github.com/TencentBlueKing/bk-plugin-runtime-go/runner"
)

func init() {
    hub.MustInstall(MyPlugin{}, ContextInputs{}, Outputs{}, inputsForm)
    // 可选：开启完成回调
    hub.Configure(hub.Options{EnablePluginCallback: true})
}

func main() {
    runner.Run()
}
```

### 2. 将 third_party 副本放入项目

由于本项目目前以源码形式分发，推荐将 `bk-plugin-runtime-go` 目录放入插件项目的 `third_party/` 并在 `go.mod` 中用 `replace` 指令引用：

```
replace github.com/TencentBlueKing/bk-plugin-runtime-go => ./third_party/bk-plugin-runtime-go
```

### 3. 配置进程

`app_desc.yml`（PaaS spec_version 3）：

```yaml
processes:
  web:
    command: ./plugin server
  worker:
    command: ./plugin worker

scripts:
  pre_release_hook: bash bin/sync_apigateway.sh
```

## 进程与命令

| 命令 | 说明 |
|---|---|
| `server` | 启动插件 HTTP 服务，监听端口由平台环境变量决定 |
| `worker` | 启动 poll/callback 调度 worker，每秒轮询一次数据库 |
| `syncapigw` | 将 runtime 内嵌的标准资源与 `definition.yaml` 同步到蓝鲸 API 网关 |
| `fetch-apigw-public-key` | 拉取网关 RSA 公钥，写入 `bin/apigw.pub`，供运行时验证 JWT |
| `collectstatics` | 兼容旧命令，当前为 no-op |
| `version` | 输出 runtime 版本号 |

## HTTP API

所有路由挂载在 `/bk_plugin/` 前缀下：

| 方法 | 路径 | 说明 |
|---|---|---|
| `GET` | `/bk_plugin/meta` | 返回插件元数据（版本列表、allow scope 等） |
| `GET` | `/bk_plugin/detail/:version` | 返回指定版本的输入/输出 schema |
| `POST` | `/bk_plugin/invoke/:version` | 调用插件，返回 trace_id + state + outputs |
| `GET` | `/bk_plugin/schedule/:trace_id` | 查询异步任务状态 |
| `POST` | `/bk_plugin/callback/:token` | 第三方系统回调入口 |
| `POST` | `/bk_plugin/plugin_api_dispatch` | 将请求转发到插件自定义 API |
| `*` | `/bk_plugin/plugin_api/*` | 插件自定义 API（由插件注册路由） |

## 插件执行模式

### 同步

插件 `Execute` 直接完成，invoke 同步返回 `state=4`（SUCCESS）。

### 轮询（Poll）

```go
func (p MyPlugin) Execute(ctx *kit.Context) error {
    if ctx.State() == constants.StateEmpty {
        // 提交任务...
        ctx.WaitPoll(5 * time.Second)  // 5 秒后再次拉起
        return nil
    }
    // 轮询检查任务状态...
    return ctx.WriteOutputs(result)
}
```

invoke 返回 `state=2`（POLL），调用方继续轮询 `/schedule/:trace_id`，直到 state 变为 SUCCESS/FAIL。

### 外部回调（Callback）

```go
func (p MyPlugin) Execute(ctx *kit.Context) error {
    if ctx.State() == constants.StateEmpty {
        preparation, err := ctx.PrepareCallback(30 * time.Minute)
        if err != nil {
            return err
        }
        // 把 preparation.URL 传给第三方系统，任务完成后 POST 该 URL
        ctx.WaitCallback(30 * time.Minute)
        return nil
    }
    // 读取第三方系统 POST 的 body
    var payload MyCallbackPayload
    if err := ctx.ReadCallback(&payload); err != nil {
        return err
    }
    return ctx.WriteOutputs(result)
}
```

invoke 返回 `state=3`（CALLBACK）和 `callback_url`，第三方系统向该 URL POST JSON 后 worker 自动重新拉起插件。

### 完成回调（Finish Callback）

无需修改插件代码。调用方在 context 中传入：

```json
{
  "plugin_callback_info": {
    "url": "https://caller.example.com/finish_callback",
    "data": {"task_id": "123"}
  }
}
```

插件进入 `SUCCESS` 或 `FAIL` 后，runtime 将 `data` POST 到 `url`，通知调用方任务已完成。需在 `main.go` 中开启：

```go
hub.Configure(hub.Options{EnablePluginCallback: true})
```

## API 网关同步

runtime 内嵌了 `internal/apigwsync/resources.yaml`，包含 5 个标准资源的鉴权配置（对齐 Python 框架）：

| 资源 | userVerified | appVerified | 说明 |
|---|---|---|---|
| `callback` | ✗ | ✓ | 允许第三方系统回调 |
| `invoke` | ✗ | ✓ | 标准运维调用入口 |
| `plugin_api` | ✓ | ✓ | 插件自定义 API |
| `openapi` | ✓ | ✓ | openapi 路径 |
| `plugin_api_dispatch` | ✗ | ✓ | dispatch 转发 |

`bin/sync_apigateway.sh` 参考写法：

```bash
#!/usr/bin/env bash
set -euo pipefail
./plugin syncapigw
./plugin fetch-apigw-public-key
```

`syncapigw` 会自动从环境变量读取网关名和应用凭证，默认使用内嵌的 `definition.yaml` 模板（`grant_permissions` 默认授权给 `bk_sops`），也支持插件项目在根目录提供自定义的 `definition.yaml` 覆盖默认值。

相关环境变量：

| 变量 | 说明 |
|---|---|
| `BKPAAS_APP_ID` | 应用 ID，PaaS 自动注入，同时作为默认网关名 |
| `BKPAAS_APP_SECRET` | 应用密钥，PaaS 自动注入 |
| `BKPAAS_BK_PLUGIN_APIGW_NAME` | 网关名（优先级高于 `BKPAAS_APP_ID`） |
| `BK_API_URL_TMPL` | 网关 API 地址模板，例如 `http://bkapi.example.com/api/{api_name}` |
| `BKPAAS_DEFAULT_PREALLOCATED_URLS` | PaaS 注入的 stage 访问地址，用于推导 backend host |
| `BK_APIGW_MAINTAINERS` | 网关维护人员，逗号分隔 |
| `BK_APIGW_RELEASE_VERSION` | 资源版本号，默认 `1.0.0+<UTC时间戳>` |

## 外部回调相关环境变量

| 变量 | 说明 |
|---|---|
| `BK_PLUGIN_CALLBACK_TOKEN_SECRET` | callback token HMAC 签名密钥 |
| `BK_PLUGIN_CALLBACK_BASE_URL` | callback URL 前缀；未设置时从 invoke 请求的 Host 推导 |

## 日志

runtime 使用 [logrus](https://github.com/sirupsen/logrus) 输出结构化 JSON 日志，字段名遵循蓝鲸日志平台规范（`time` / `levelname` / `message`）。日志文件由 blueapps-go 配置：

- `v3logs/default.log`：runtime 应用日志（handler、executor、worker）
- `v3logs/gorm.log`：数据库慢查询日志
- `v3logs/gin.log`：HTTP 访问日志

关键日志示例：

```
[invoke]  plugin invoke request accepted   context_inputs={...}  req_json={...}  trace_id=xxx
[invoke]  plugin execute start             plugin_version=1.0.0  trace_id=xxx
[invoke]  plugin execute wait callback     callback_expires_at=2026-...  trace_id=xxx
[worker]  [schedule] run task              state=3  invoke_count=2  trace_id=xxx
[worker]  [schedule] callback data snapshot  callback_data={...}  trace_id=xxx
[worker]  [schedule] plugin execute schedule done  state=4  finished=true  trace_id=xxx
```

## 从 beego-runtime 迁移

详见 [docs/migration/beego-runtime-to-runtime-go.md](docs/migration/beego-runtime-to-runtime-go.md)。

最小改动只需修改 `main.go` 的 import：

```diff
- "github.com/TencentBlueKing/beego-runtime/runner"
+ "github.com/TencentBlueKing/bk-plugin-runtime-go/runner"
```

## 依赖

| 依赖 | 用途 |
|---|---|
| bk-plugin-framework-go | 插件 SDK |
| blueapps-go | 配置加载、数据库、日志 |
| gin | HTTP 路由 |
| gorm | 数据库 ORM（MySQL） |
| bk-apigateway-sdks | APIGW 同步 |
| pongo2 | definition.yaml 模板渲染 |
| logrus | 结构化日志 |
| cobra | CLI 命令 |

## License

MIT License
