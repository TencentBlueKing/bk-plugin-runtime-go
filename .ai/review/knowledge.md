# bk-plugin-runtime-go 审查知识库

## 核验基线与职责

- 2026-09-09 核验远程 `main`：`3db91f59466149a4c87ab808b9ada1971698d431`；`internal/version/version.go` 为 `v0.2.9`。审查时重新核对 PR base/head 和有效依赖，不把快照当作最新版本。
- 根 `go.mod` 使用 Go 1.23.0，直接依赖 framework `v1.0.3`、blueapps-go `v1.6.2`、Gin、GORM、APIGW SDK；有 `gopkg v1.3.0 => v1.0.9` 的公开替换。runtime 承接 HTTP、数据库、worker、回调和 APIGW，framework 承接插件 SDK、注册、schema、执行分支与抽象接口。
- 独立 framework 本次 main `d4e2018958d5dfe47d4e6235324fb40ea2f609e5` 的模板默认 framework `v1.0.4` / runtime `v0.2.8`。生成项目的 pin、本仓库依赖、远程 main 和已部署版本是不同事实；不得因为本仓库改动就假设所有消费插件同时升级。
- `README.md`、`docs/migration/beego-runtime-to-runtime-go.md` 提供业务背景；实际路由、APIGW 模板、锁定依赖及测试优先于文档示例。历史已修问题仅作为防回归入口。

## 入口地图

| 路径 | 职责 |
| --- | --- |
| `runner/runner.go`、`cmd/` | CLI 启动；server 初始化平台配置/DB 并 AutoMigrate，worker 从共享数据库恢复任务；APIGW/公钥等部署命令。 |
| `internal/blueappsadapter/bootstrap.go` | Blueapps 配置、旧 MySQL 环境变量兼容、日志、MySQL/Redis 初始化及连接池寿命设置。 |
| `internal/server/router.go`、`handlers.go`、`internal/httpx/response.go` | 标准插件 HTTP 接口、scope、invoke 持久化、结果查询、外部 callback 和响应封装。 |
| `internal/auth/scope.go` | 调用方/操作者/request ID/tenant ID header 提取和 allow-scope 规则。 |
| `internal/server/plugin_api.go`、`plugin_api_router.go` | 自定义 API 路由适配、受限内部 dispatch、路径参数、GET/POST、JSON/multipart 与响应透传。 |
| `internal/runtimeadapter/` | 把 SDK 输入/上下文、输出存储、poll/callback/终态接口接到数据库；准备及复用 callback URL。 |
| `internal/store/model.go`、`gorm_store.go`、`json.go` | Schedule 模型、JSON 字段、状态写入、条件抢占、callback 幂等与过期回收、租约续期。 |
| `internal/scheduler/worker.go` | 获取到期任务、恢复插件执行、租约 heartbeat、完成通知。 |
| `internal/callback/token.go`、`internal/finishcallback/notifier.go` | 第三方入站 callback token 校验，与出站完成通知/重试两套不同机制。 |
| `internal/apigwsync/resources.yaml`、`definition.yaml`、`sync.go` | 嵌入 APIGW 资源、鉴权/后端路径及同步参数；需要与 Gin 路由逐项对应。 |

## HTTP 与表单协议

- 路由基准不带末尾 `/`：GET `/bk_plugin/meta`、GET `/bk_plugin/detail/:version`、POST `/bk_plugin/invoke/:version`、GET `/bk_plugin/schedule/:trace_id`、POST `/bk_plugin/callback/:token`、POST `/bk_plugin/plugin_api_dispatch`。自定义 API 在 `/bk_plugin/plugin_api/` 下注册。
- invoke JSON 是 `inputs` 与 `context` 两个字段；`context` 存入 `ContextInputs`。handler 生成 UUID trace，先创建 EMPTY/调用次数 1 的 Schedule，再调用锁定 framework 的 executor。插件 error 可以是 HTTP 200、`result=true` 但 `data.state=FAIL`；不能把传输成功或 envelope 成功当作插件成功。
- invoke 兼容保留顶层和 `data` 内的 `trace_id`；返回 `state/outputs/err`，成功执行路径还返回 `callback_url`。schedule 是读取已保存结果，保留 `version/plugin_version`、`create_at/created_at`、`finish_at`、`err/error`。时间格式为 `2006-01-02 15:04:05`，无时间时空字符串。依据：`handlers.go`、`httpx/response.go`。
- schema/detail 由有效 framework 的 `protocol.BuildDetail` 生成，runtime 传入 `EnablePluginCallback`。旧 `MustInstall` 的 JSON 表单放在 `inputs`，不进入 `forms.renderform`，见 `internal/server/handlers_test.go::TestDetailKeepsLegacyInputsFormOutOfRenderForm`。对 V2、`form.json/form.js` 的改变应继续追到 framework 和 BK-SOPS/Python 消费者；不能把 JSON 对象与原始 JS 字符串视为同一渲染合同，也不能认为当前 runtime 会发现或执行 `form.js`。
- `plugin_api_dispatch` 限定 `/bk_plugin/plugin_api/` 前缀及 GET/POST，将 `dumped_data` 合并入 data；复制请求上下文/header，非空 username 覆盖操作者 header。multipart 保留字段和文件，路由参数由 SDK `pluginapi.WithParams` 转交。成功响应透传，不能重复套 envelope 或改变业务 JSON/状态码。
- APIGW 的 POST backend.path 已对齐无末尾斜杠的 Gin 路由，避免 307 改变调用行为。`resources.yaml` 的鉴权是逐资源配置，不能从 README 表格推断完全相同；声明 openapi 资源也不证明 `router.go` 自动实现该路由。

## 身份、租户与可观测性边界

- `auth.TenantID` 优先 `X-Bk-Tenant-Id`，回退 `X-Bkapi-Tenant-Id`；调用方 app、操作者也有兼容 header。此优先级有测试，应作为防回归契约。表单 data API 的 header 与 invoke context 中的 `tenant_id` 是不同入口，不能互相替代。
- `AllowRequest` 的既有语义：空规则放行，未列出的 app code 放行，命中 app 时比对 scope type/value。它是业务域限制，不等于网关签名认证或逐资源租户鉴权。网关信任前提必须与具体部署入口核对。
- Schedule 保存 `CallerApp/Operator/RequestID/TenantID`，但当前 `Get` 和多项更新以 `trace_id` 定位，保存 TenantID 本身不能证明 SQL 查询有租户过滤。审查租户相关 diff 时应验证真实入口认证、对象归属与返回数据，不把这一基线边界自动生成为与 PR 无关的漏洞评论。
- 输入、ContextInputs、持久化 ContextData、Outputs、CallbackData 各有不同用途；worker 从已保存输入恢复，invokeCount 加 1。插件 trace ID、网关 request ID、业务 task ID 和租户不可混用。handler 日志包含请求身份，worker 当前主要关联 trace/version；审查新增异步路径时逐段检查上下文是否实际传递。
- token/业务 payload 可能出现在 URL 或 DEBUG 日志中；DEBUG 不是脱敏。审查日志 diff 应关注泄露路径并保留必要的 trace/状态/错误证据，不粘贴真实 token、secret 或完整业务输入。

## 数据库、状态与异步执行

- 状态数值继承 framework：EMPTY=1、POLL=2、CALLBACK=3、SUCCESS=4、FAIL=5。终态同时写 FinishedAt 并释放锁；`MarkPoll/MarkCallback` 保存下一次恢复所需状态。外部副作用与这些数据库写入不是跨系统原子事务。
- `Schedule.TraceID` 唯一；每次 invoke 新建 UUID，因此并没有按业务请求键自动去重。对象存储以 trace 读写完整 JSON map，检查数字/空值转换、错误传播及多次写入覆盖语义。
- `ClaimDue` 分别查询到期 POLL 与已收到回调的 CALLBACK，合并排序后限制批量；抢占 UPDATE 再检查状态、到期、未结束与锁过期条件，只有 RowsAffected=1 才取得任务。模型中有对应复合索引；删除条件或索引可能造成重复执行或热查询退化。
- worker 默认批量 10、租约 5 分钟、检查间隔 1 秒；当前一批任务串行 `runItem`。执行中的 heartbeat 每 LockFor/3 续约，`RenewLock` 核对 worker 所属。审查长任务、多 worker、批次后部等待、续约失败、进程退出时的重复副作用及旧 worker 写入边界，不能只测试单 worker 顺利完成。
- `ReceiveCallback` 只接受匹配 trace/token hash、CALLBACK、未终结、未接收且未过期的记录；首次 payload 生效。仍等待完成的重复回调返回 `ErrCallbackAlreadyReceived`，handler 回成功，不能覆盖已保存 payload 或清除 worker 锁。已终结/不匹配回调不是同一幂等分支。
- `ExpireCallbacks` 回收未接收且超时的 CALLBACK，写 `CALLBACK_TIMEOUT`；更新时再次校验未接收/未终结以处理并发回调。既有幂等、超时与租约能力不能描述为尚未实现。
- 入站 token 采用 HMAC、随机 nonce、过期时间及存储 hash；空 `BK_PLUGIN_CALLBACK_TOKEN_SECRET` 明确报错，无默认生产密钥。PrepareCallback 先生成 URL，SetCallback 才持久化；审查第三方极速回调和重复准备时的顺序。URL 优先使用环境配置，否则 invoke 从 Host/forwarded header 推导，需考虑受信代理与 worker 无 HTTP 请求的场景。
- 出站 finish callback 来自 `context.plugin_callback_info`，仅在启用且任务终态时异步发送 `data`。`NotifyWithRetry` 最多 3 次、间隔 500ms、每次 HTTP 超时 10s；失败记录日志，不回滚终态。它使用后台 goroutine，不能把它描述为有持久化队列或进程退出后的必达保证。修改应检查取消、重试重复投递及 URL 信任边界。

## 连接池与依赖事实

`internal/blueappsadapter/bootstrap.go` 要求 MySQL 配置，显式 `MYSQL_*` 优先于旧 `GCS_MYSQL_*`；初始化后设置 `ConnMaxLifetime=3min`、`ConnMaxIdleTime=30s`。锁定的 blueapps-go `v1.6.2` 在 `pkg/infras/database/init.go` 设 max idle 20、max open 100、启动 Ping 5s，并启用预编译语句、关闭 GORM 默认事务；runtime 覆盖其中的连接寿命。更改连接池需核对 DB/proxy 空闲超时与应用实例总连接量，不能仅凭 `invalid connection` 日志把原因归为 MySQL 版本或未设置寿命。

`go.mod` 中 MySQL driver 为 `go-sql-driver/mysql v1.8.1`、GORM MySQL `v1.5.7`；这些是依赖事实，不是目标 MySQL 的验收结果。数据库/driver/GORM 变更须验证实际服务器的迁移、JSON/索引、连接回收与并发 SQL 行为。

## 测试地图与证据层级

| 变更区域 | 现有入口 |
| --- | --- |
| HTTP/旧协议/错误/回调 URL | `internal/server/handlers_test.go`：`TestMetaAndDetail`、`TestInvokeSyncAndScheduleRead`、`TestInvokeFailIncludesErrForSOPSCompatibility`、`TestInvokePreparedCallbackUsesRequestBaseURL`、scope/finish callback。 |
| 租户 header、scope、dispatch | `internal/auth/scope_test.go` 的标准 header/优先级/旧 header；`internal/server/plugin_api_test.go` 的 header、响应、尾斜杠、路径参数、multipart、越界 URL。 |
| 抢占、幂等、超时、续约 | `internal/store/gorm_store_test.go` 的 ClaimDue/索引/幂等/ExpireCallbacks/RenewLock；`internal/scheduler/worker_test.go` 的到期 poll 和 callback 超时。 |
| token、准备 callback、完成通知 | `internal/callback/token_test.go`、`internal/runtimeadapter/execute_runtime_test.go::TestPrepareCallbackIsReusedBySetCallback`、`internal/finishcallback/notifier_test.go`。 |
| DB 配置、APIGW 路由 | `internal/blueappsadapter/bootstrap_test.go` 的连接寿命和环境变量优先级；`internal/apigwsync/sync_test.go` 的资源路径/鉴权声明/模板配置。 |
| 组合协议 | `internal/e2e/sops_execute_test.go` 的 `TestSOPSInvokeSyncPluginFlow`、`TestSOPSInvokePollPluginFlow`、`TestSOPSInvokeCallbackPluginFlow`。 |

2026-09-09 在该基线以有效 framework `v1.0.3` 运行 `GOWORK=off go test -mod=readonly ./... -count=1` 通过。这些 store/server/e2e 用例使用内存 SQLite 和模拟 HTTP，是 local protocol simulation；不等于真实 BK-SOPS、目标 MySQL、多实例故障恢复或生产部署验收。当前测试名称也不能代替对断言和并发条件的检查。将后续 PR 的实际测试结果与此基线分开记录。
