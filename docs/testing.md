# 测试与验收

所有 HTTP、数据库、容器与页面流程使用实际依赖。运行完整检查前执行 make setup，并确认本机 Docker 可以访问。

| 命令 | 检查范围 |
| --- | --- |
| npm run lint:docs | Markdown、链接与入口文档长度 |
| npm run lint:config | JSON、YAML、Issue 表单与 Actions 约定 |
| npm run lint:tooling | 脚本语法、ShellCheck、配置文件与真实端口检查 |
| make test | Go race 测试、真实 HTTP、Gateway 转发与服务停止 |
| make test-integration | Docker 中的 PostgreSQL 迁移、账号、会话、就绪检查和 workerd 执行 |
| make web-check | TypeScript、ESLint、Prettier、Vite 构建与接口生成一致性 |
| make dev-check | 真实本机进程、数据库、热重载、完整停止、多实例与失败清理 |
| make test-e2e | 真实 Chromium 在桌面和手机尺寸下完成登录、刷新和退出 |
| make generate-check | Go 接口生成结果与契约的一致性 |
| make container-check | Go 镜像构建、实际启动、CLI、路由与正常退出 |
| make check | 执行格式、静态检查、测试、构建、依赖审计与上述本地集成检查 |

make generate 从 api/openapi.yaml 同时生成 Go 类型、Gin 接口、客户端和前端类型，生成文件随实现提交。Go 和前端的检查分别比较临时生成结果。

## 开发流程验证

开发流程测试创建独立配置与随机可用端口，通过与成员相同的开发入口运行 Go、Vite 和 PostgreSQL。测试验证同名控制器拒绝重复启动、错误的 reset 确认不会停止服务、真实 API 登录和 Gateway 转发。

热重载检查在测试期间创建独立 Go 源文件，验证实际后端进程更换、会话保留，以及受管理 PostgreSQL 执行容器保持原始启动时间。随后验证真实编译错误会停止旧后端，修复源码后服务恢复。测试完成后删除该源文件。

完整停止验证端口释放、执行容器停止、数据库数据保留和再次启动后的登录会话。多实例检查验证一个实例停止后另一个实例继续提供服务。迁移版本不兼容时验证启动失败与资源清理。

页面测试使用独立环境，验证错误密码、登录成功、刷新后的会话、退出和页面宽度。截图与失败记录位于 .local/playwright，环境日志与配置位于 .local/checks 和各测试实例目录。

测试结束后清理所属容器、网络与数据卷。镜像缓存继续复用。文档检查独立于代码、数据库和浏览器流程。

## workerd 集成验证

使用当前 Docker 连接单独验证 Worker 运行配置。

```sh
export DOCKER_HOST="$(docker context inspect --format '{{.Endpoints.docker.Host}}')"
export TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE=/var/run/docker.sock
go test -race -count=1 -tags=integration -v ./internal/workers
```

在 Docker Engine 29.5.2 的 `linux/arm64` 环境中，workerd `1.20261012.1` 执行 `POST /echo`，返回 HTTP 200、`X-Worker: workerd` 和响应正文 `POST /echo cloudlet`。无效兼容日期 `2099-01-01` 使进程以状态码 1 退出，并报告运行时支持的最晚日期 `2026-10-19`。入口用例验证了缺失文件、目录和越界路径的诊断。镜像内的 `linux/arm64` 二进制文件与 Cloudflare 发布文件解压后的 SHA256 一致。

## 执行模块的业务验收

以下为目标验收条件，随对应模块接入真实运行时完成。资源和状态依据 [平台设计](platform-design.md) 与 [执行环境设计](runtime-design.md)。

| 范围 | 必须观察的结果 |
| --- | --- |
| 资源权限 | 两个项目的用户访问、绑定、日志、产物和会话均受到项目权限限制 |
| 部署产物 | 实际上传包的摘要和运行内容一致，异常归档、路径和大小被拒绝，有效引用保护产物 |
| 版本激活 | 新版本通过预检与就绪检查后切换路由，预检失败保留原活动版本，过期激活请求返回冲突，已提交但未生效的快照在恢复后补齐 |
| 原生 Worker | 真实 workerd 执行声明的 HTTP、流、绑定与 WebSocket 行为，不支持的能力在部署时明确失败 |
| DO 连续性 | 同一对象跨 Worker 版本更新、Go 重启和运行时交接保持标识与数据，实际事务和并发符合声明 |
| 存储所有权 | 旧执行未确认结束时，新执行无法取得同一 DO 或独占数据卷的写入权 |
| 重启接管 | Go 重启后接管原执行实例、恢复路由，原容器可访问，未过期浏览器会话保留原页面并可重新连接 |
| 中断恢复 | 在资源创建、激活、交接和删除阶段实际终止控制器，重启后得到明确状态和可检查的恢复结果 |
| 配额与超时 | 验证实际 CPU、内存和并发限制，未知执行结果继续占用容量，超限和截止时间产生明确终态 |
| 浏览器生命周期 | 保留 Chromium sandbox，验证会话隔离、真实截图、后端不可用时的期限回收和完整退出后的容器释放 |
| 数据与产物清理 | 活动数据与历史版本按保留规则存在，删除确认后清理所属资源，其他项目的数据保持可用 |

隔离与资源限制按能力声明逐项验证，记录实际版本、平台、命令和结果。Docker 总配额与 Worker 级限制分别验收。跨 macOS、Linux、WSL2 的结果分别记录，已有的开发流程测试作为业务验收的基础。
