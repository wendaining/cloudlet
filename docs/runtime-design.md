# 执行环境设计

本文规定 Workers、Durable Objects、Containers 和 Browser Run 的目标执行规则。资源、部署与操作模型见 [平台设计](platform-design.md)。目前已验证无状态 Worker 和具备 SQLite 绑定的 Worker 运行配置。

## 执行单元与控制边界

| 类型 | 执行单元 | 持久身份 |
| --- | --- | --- |
| Workers 与 DO | 每个项目一个受管理的 workerd 容器，承载该项目的 Worker 和命名空间 | Project、Worker、DurableNamespace |
| 用户容器 | 每个 Container 对应受管理的 Docker 执行 | Container 与声明的数据卷 |
| Browser Run | 每个 BrowserSession 对应独立的 Playwright、Chromium 容器 | 有期限的 BrowserSession |

workerd、用户容器和浏览器由 Docker Engine 持有，与 Go 管理进程分别管理生命周期。Go 热重载后根据稳定标识和执行序号重新接管已有实例。部署配置变更可能更换执行实例，遵循各模块的交接规则。

资源创建、启动、停止、观测和删除通过 Docker 官方 Go SDK 执行。运行时容器携带环境、项目、资源、执行序号与模块归属标签。已确认元数据和标签共同确定操作范围。

本地 published 模式使用 Docker 实际发布的回环端口，容器部署的 network 模式使用平台提供的内部端点。network 模式加入已有的用户定义 bridge 网络，不发布主机端口；适配器按归属生成稳定的网络别名，使同一网络中的调用方可以通过该别名访问服务。每次接管重新读取实际连接地址，公开访问地址独立生成。

内部控制端点验证执行单元身份和允许的操作。凭据保存在独立私有配置中，控制器按需读取。用户代码、浏览器页面与用户容器只取得获授权的资源能力。

## Worker 版本与运行时

`workers.Prepare` 读取已打包的单个 UTF-8 JavaScript ES Module。入口使用包目录内的相对 `.js` 或 `.mjs` 路径，路径字符支持 ASCII 字母、数字、句点、下划线、连字符和斜杠。兼容日期使用 `YYYY-MM-DD` 格式，支持 2000-01-01 至 2026-10-19，并以 UTC 当天为上限。

调用方将返回的 `Config` 与 `Module` 分别放到容器内的 `ConfigPath` 和 `ModulePath`，使用 `RuntimeImage` 和 `ServeArgs()` 启动。配置提供一个 HTTP 监听，端口为 `HTTPPort`。`ghcr.io/ryanhecht/workerd` 镜像封装 Cloudflare 发布的 workerd 二进制文件，使用版本和多架构镜像摘要固定来源。实际命令及验证结果见 [workerd 集成验证](testing.md#workerd-集成验证)。

部署清单固定运行时版本及摘要、ES Module 入口、模块内容、compatibility date、flags 和绑定。TypeScript 经 tsc 检查并通过 esbuild 生成部署包，运行时执行已验证产物。构建命令位于 `runtimes/workers/build.mjs`，默认入口为该目录下的 `src/index.ts`，默认输出为 `dist/index.mjs`，TypeScript 与 esbuild 使用锁定版本。入口必须属于所选 TypeScript 配置的实际程序，验证成功后才打包。默认路径和打包目录固定在脚本目录，相同入口的产物字节不受调用目录影响。

`workers.Preflight` 使用固定 workerd 的 `test` 命令在独立容器中载入并初始化模块，返回 `load`、`initialize` 或 `ready` 阶段。预检容器不发布端口，不加入网络，结束后删除。模块解析和依赖解析失败归入 `load`，顶层代码执行失败归入 `initialize`。阶段标记在完整日志流中识别，诊断仅保留末尾 16 KiB，成功退出返回 `ready`。

项目的运行配置包含各 Worker 的活动版本和 DO 命名空间归属。更改代码产生新的 Deployment，更换进程产生新的 Execution。发布、版本激活和运行时重启分别记录。

workerd 常驻执行 Worker isolate。请求复用已准备的运行环境。运行时异常和资源超限记录到所属资源，按已配置的恢复预算处理。

运行时配置需要整体更换时，操作影响范围为所属项目。没有 DO 存储交接的替换可在候选执行就绪后切换入口，旧执行保留已有请求，按期限排空。

同一项目存在 DO 时，配置更新同时遵循命名空间所有权交接。运行时适配器必须报告实际支持的版本更新机制和影响范围，部署操作据此选择对应的交接流程。

## DO 命名空间与存储

`workers.PrepareWithDurableObjects` 接收一个或多个 `durableobjects.Namespace`。每个命名空间声明稳定的 UUID、模块中的类导出名称和至少一个 Worker 绑定名称。类与绑定名称使用 ASCII JavaScript 标识符。UUID、类名称和绑定名称在同一个 Worker 配置中分别保持唯一。同一命名空间的多个绑定指向同一个类和存储。

配置使用命名空间 UUID 作为 workerd 的 `uniqueKey`，通过 `enableSql` 开启 `ctx.storage.sql`，通过 `localDisk` 写入持久化存储。调用方将专用的可写数据卷挂载到容器内的 `durableobjects.StoragePath`（`/durable-objects`）。每次启动只将该卷交给一个运行中的执行。重启时先停止原执行，再将原数据卷挂载到新执行，并保留命名空间 UUID。

namespace_id 是稳定业务标识。Worker 的绑定名称和类声明引用该标识，代码版本变更保留命名空间身份。对象标识、对象调度、storage 事务和输出门由 workerd 执行。

每个命名空间关联独立存储标识、项目和唯一执行所有者。存储路径由平台根据标识生成。对象 SQLite 文件置于持久化数据卷，Go 服务保存归属与迁移信息。

| 交接阶段 | 规则 |
| --- | --- |
| 预检 | 验证类导出、命名空间绑定、迁移声明与数据兼容性 |
| 停止新调用 | 禁止向待交接命名空间派发新请求，相关入口报告交接状态 |
| 排空与关闭 | 在截止时间内结束旧调用并停止旧执行，确认存储所有权已释放 |
| 启动与迁移 | 授予新执行独占访问权，执行已声明迁移，核对就绪结果 |
| 激活 | 提交新的执行所有者和活动版本，恢复入口 |

旧执行未确认停止时拒绝授予新的存储所有权。超过排空期限的连接按声明结束，操作结果保留具体原因。后端在交接中断后根据持久化阶段与实际执行状态继续处理。

迁移失败时保留数据和迁移结果，报告资源不可用。恢复历史代码要求当前数据兼容，涉及数据恢复时使用已验证备份。部署记录和对象存储变更分别保存。

goose 管理 PostgreSQL 的平台结构迁移。DO 类声明与对象存储迁移由部署和 workerd 管理，使用独立的迁移记录与验收。

命名空间删除先验证全部部署和执行引用，阻止新调用，确认所有者退出后按明确的数据删除请求执行。命名空间标识删除后保持退役状态。

## 用户容器

Container 保存镜像 digest、启动参数、端口、资源要求、数据卷与允许的网络访问。修改镜像或配置产生新的不可变配置版本，实际执行保留所属配置版本标识。镜像引用在准备时必须包含不可变摘要，准备时由 Docker Engine 按摘要拉取并核对本地镜像报告的摘要，拉取或核对失败时停止创建。

容器健康检查通过后才注册可用路由。适配器按 Docker 实际发布的回环端点、声明的接受状态码与截止时间执行 HTTP 就绪探测；探测超时、容器退出或端点未发布时返回失败观测，不注册路由。停止和删除先停止接收新请求，再结束执行并更新可用性。数据卷按照资源声明保留或删除。

持久数据卷由一个声明的执行所有者写入，更换执行时完成排空与独占交接。只读共享卷按声明授权。执行器观测失败时保留所有权和容量预留，直到确认结果。

用户容器使用非 root 身份、明确的 CPU、内存、进程与打开文件数量限制。宿主路径、挂载和网络由平台策略约束，运行时访问自身获得授权的数据卷和端点。适配器通过 `ProbeHostLimits` 报告宿主对这些限制的支持范围；真实 Docker 验收在受限容器中运行有限负载，核对 CPU 节流、内存 OOM、PIDs 与 nofile 触发结果和清理，不支持的项记录原因。

## 浏览器会话

会话容器包含固定版本的 Node.js、Playwright 与匹配 Chromium。每个会话拥有独立的配置目录、下载目录和协议端点，容器使用非 root 用户并保留 Chromium sandbox。缺少所需宿主能力时拒绝创建。

`runtimes/browser` 提供独立容器入口。Chromium 和会话页面启动后，内部 `GET /readyz` 才返回就绪。每个容器配置独立的 `CLOUDLET_BROWSER_TOKEN`，内部命令以 `Authorization: Bearer <token>` 认证，凭据无效返回 401。`POST /navigate` 读取 JSON 中的 `url`，返回跳转后的 `url` 和 `title`。导航仅接受 HTTP(S)，打开页面和读取标题共用同一个截止时间。

`POST /screenshot` 截取最近一次成功导航后仍打开的 HTTP(S) 页面。成功时返回当前视口的设备像素 PNG，默认 1280 × 720，大小上限为 2 MiB。没有可截图页面返回 409，超限返回 422。请求体可以为空，也可像导航命令一样在 JSON 中传入 `timeout_ms`。两种命令均默认十秒，最长三十秒。命令在同一会话中串行执行。超时后返回 504 并关闭会话，避免复用可能无响应的 Chromium。执行接口仅供控制器的内部网络使用。

启动时可设置 `CLOUDLET_BROWSER_VIEWPORT_WIDTH`（320–1920 CSS 像素）、`CLOUDLET_BROWSER_VIEWPORT_HEIGHT`（240–1080 CSS 像素）和 `CLOUDLET_BROWSER_DEVICE_SCALE_FACTOR`（1–2）。默认值为 1280、720 和 1。宽高分别乘像素比向上取整后的总面积不得超过 4,194,304 设备像素。无效配置阻止启动。经过同一会话凭据认证的 `GET /viewport` 从 Playwright 的视口状态和会话配置返回 `width`、`height` 和 `device_scale_factor`。

会话镜像以 `pwuser` 运行并要求 Chromium sandbox。`runtimes/browser/seccomp.json` 源于 [Playwright 1.65.1 的 Apache-2.0 seccomp 配置](https://github.com/microsoft/playwright/blob/v1.65.1/utils/docker/seccomp_profile.json)，增加了对 Chromium 在嵌套用户命名空间内调用 `chroot` 的支持，仍由内核校验权限。容器使用独立共享内存、移除 Linux capabilities，并启用 `no-new-privileges`。关闭服务时等待 Chromium 退出。构建与验证命令见 [Browser Run 验证](testing.md#browser-run-验证)。

会话记录包括项目、session_id、Execution、绝对过期时间和最近状态。Go 重启后，只有身份、执行序号与期限均有效的原执行能够重新连接。连接断开与会话关闭分别处理。

浏览器执行服务通过 `CLOUDLET_BROWSER_DEADLINE` 接收未来的 RFC 3339 UTC 时间。时间以 `Z` 结尾，可带一至九位小数秒（例如 `2030-01-01T00:00:00.123456789Z`），计时精度向下截断至毫秒。在控制器断开后也会在到期时关闭 Chromium 并退出，正在执行的请求会断开。格式无效或期限已过会阻止启动。独立启动时可省略该变量，管理层接入时应传入会话期限。会话容器启用退出后自动删除，关闭自动重启，由 Docker Engine 完成容器回收。控制器确认原执行容器已删除后释放容量，再记录会话终态。清理失败时保留容量，后续检查可以重试。Go 不可用期间容量继续保留，控制器恢复运行后会重新检查到期会话。

管理 API 在项目下提供创建、按标识读取、导航与关闭。所有者和开发者可以创建、导航和关闭，项目成员可以读取会话元数据。创建返回的 endpoint 是需要账号会话认证的管理 API 相对路径。Chromium 控制端点仅发布到宿主回环地址，容器凭据仅用于管理进程和容器间的请求。会话期限默认为五分钟。控制器在后台检查到期会话，客户端也可以通过 DELETE 关闭会话。重复关闭返回已记录的终态。管理进程重启后接管未到期会话由后续任务接入。

项目成员可以通过会话地址下的 GET /console 读取控制台消息和未捕获的页面错误。记录只在原 Chromium 会话中保留，最多返回最近 100 条，每条消息最多 512 个 UTF-16 单元。返回的 truncated 和 dropped 分别标记长消息截断和移除的旧记录数量。会话结束后记录随容器释放。

会话保留默认页面，并可向会话地址下的 POST /pages 打开最多七个额外页面。页面返回独立标识，可查询和关闭，导航请求可选 page_id 指向额外页面。额外页面共享会话的 Chromium 上下文，关闭其中一页不影响其他页面。已关闭的标识返回 404。页面详情的地址上限为 4096 个 UTF-8 字节，标题上限为 512 个 UTF-16 单元，响应标明字段是否截断。

浏览器进程意外退出后关闭原会话。完成整个本地环境停止后，后续启动通过独立命令创建新会话。

截图和需要保留的下载先写入 Artifact，再回收临时目录。协议连接、产物下载和会话命令都验证项目与会话权限。Go、CLI 与 MCP 使用实际浏览器接口，记录命令超时与执行结果。

四核四 GiB 部署预算下浏览器默认最多一个并发会话。增加并发需要满足环境与项目的整体准入限制，使用实际负载验证资源需求。

## 隔离与容量

| 边界 | 强制规则 |
| --- | --- |
| 项目 | 项目执行网络和持久化数据分别隔离，资源绑定验证项目归属 |
| 管理服务 | 数据库凭据与 Docker 管理连接仅供控制模块使用 |
| 用户执行 | 用户容器与浏览器不挂载 Docker socket，不加入管理网络 |
| Worker 能力 | 仅开放已声明绑定，阻止访问运行时内部控制端点与平台数据 |
| 网络出口 | 由宿主及运行时执行出口策略，内部服务通过获授权的绑定访问 |
| 资源准入 | 同时预留环境和项目预算，清理确认后释放容量 |

Docker 配额约束整个执行单元。Worker isolate 的 CPU、内存、startup 和 subrequest 限制分别验证执行能力。请求超时与实际终止执行分别记录，运行时按已验证机制结束超限执行。

项目运行时隔离、浏览器 sandbox 与用户容器策略需要实际验证。发布说明声明已验证的隔离与资源限制范围。

## 运行时验证与兼容范围

| 验证范围 | 必需证据 |
| --- | --- |
| 模块执行 | 真实 module Worker 的 fetch、Request、Response、Streams、Crypto 与错误路径 |
| 网络行为 | 流式请求、响应、WebSocket、取消与超时的已声明行为 |
| 绑定 | DO 与服务引用的解析、项目权限、错误分类和注销 |
| DO | 对象身份、事务、并发语义、类迁移与执行所有权 |
| 资源限制 | 每项声明限制的实际执行结果、超限隔离与恢复范围 |
| 浏览器 | Playwright 版本匹配、会话连接、sandbox、超时与退出 |
| 跨环境 | macOS、Linux 与 WSL2 上的端点、数据卷、启动和完整停止 |

运行时依赖固定版本、来源与摘要，构建准备完成后才用于部署。验证结果绑定对应版本和平台，生产启动使用已准备的运行环境。

采用 workerd 上游公开能力。需要下游扩展时，通过独立架构决策记录用途、维护来源、上游关联、升级验证与退出条件。未通过验证的行为在能力查询中标记不可用，要求该行为的部署明确失败。

管理 API 使用 Cloudlet 的 OpenAPI。Worker 编程模型的兼容范围逐项声明，外部部署客户端的协议兼容单独定义。真实业务验收见 [测试说明](testing.md)。

## 设计参考

Open Compute 的 [运行时职责](https://open-compute.dev/docs/project/)与[产品能力边界](https://open-compute.dev/docs/products/)用于核对运行时、平台和外部执行组件的分工。[DO 标识模型](https://github.com/elliothux/open-compute/blob/bca30aa3c6da51e9e3520727e41b59c2dd379cf9/crates/core/src/durable_objects.rs)展示了对象身份与命名空间的关联。
