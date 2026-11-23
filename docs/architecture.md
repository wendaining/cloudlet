# 架构

本文概述目标架构与当前实现。详细规则见 [平台资源与部署设计](platform-design.md) 和 [执行环境设计](runtime-design.md)。

Cloudlet 在一台 Linux 服务器上管理计算资源。本地开发在成员电脑上运行 Go 与前端开发服务器，通过本机 Docker 启动 PostgreSQL 和执行环境。环境边界见 [本地开发与服务器部署](development.md)。

管理 API 和 Gateway 属于同一 Go 工程，分别监听独立端口。外部运行时由对应模块管理。

管理 API 采用 Gin，元数据通过 GORM 访问 PostgreSQL，管理界面采用 React 与 TypeScript。各项技术的职责、接口契约和工程约定见 [技术栈](technology-stack.md)。

## 核心约定

| 范围 | 约定 |
| --- | --- |
| 业务归属 | Project 管理成员、资源与配额，CLOUDLET_INSTANCE 标识开发或部署环境 |
| 资源身份 | Worker、Container 和命名空间使用稳定标识，Deployment 与 Execution 分别记录版本和实际执行 |
| 发布 | 代码和绑定形成不可变部署，验证就绪后激活，并更新内存路由快照 |
| 数据 | PostgreSQL 保存平台元数据，本地产物目录保存部署文件，workerd 持有 DO SQLite 状态 |
| 运行时 | workerd、用户容器和浏览器独立于 Go 进程运行，控制器按元数据重新接管 |
| DO 所有权 | 命名空间身份跨代码版本保持稳定，同一持久存储只授予一个执行所有者 |
| 能力声明 | 运行时版本与执行能力通过真实验证后登记，所有调用方使用同一管理 API |

应用服务负责权限、部署事务、操作记录和资源准入。执行模块负责真实运行时操作并返回观测结果。Gateway 读取已经提交的路由快照，执行公开请求转发。

## 模块边界

| 路径 | 职责 |
| --- | --- |
| cmd/server | 配置加载、信号处理和服务启动 |
| cmd/cloudlet | 基于共享客户端的命令行入口 |
| internal/config | 严格读取 JSON 配置并验证字段 |
| internal/controlplane | 管理 HTTP 接口 |
| internal/database | PostgreSQL 连接、迁移版本、账号与项目数据 |
| internal/operations | 操作记录的幂等登记、按项目查询与阶段推进 |
| internal/capacity | 主机与项目容量的事务预留、确认释放与占用查询 |
| internal/staticdeploy | 静态站点 ZIP 的保存、解包和不可变版本登记 |
| internal/apigen | OpenAPI 生成的类型、Gin 接口和客户端 |
| migrations | 内嵌 SQL 迁移文件与当前版本 |
| internal/gateway | 按域名选择上游并转发 HTTP 请求 |
| internal/server | 监听端口与服务生命周期 |
| internal/version | 构建版本和提交标识 |
| internal/workers | ES Module 与 Durable Objects 的 workerd 配置、启动参数和镜像引用 |
| internal/durableobjects | SQLite 命名空间、类绑定和存储挂载路径 |
| internal/containers | Docker Engine 容器生命周期与回环端口观测 |
| internal/browser | 复用容器管理的 Playwright 会话 |
| pkg/client | CLI 与其他客户端共享的 API 客户端 |

PostgreSQL 当前保存账号、登录会话、项目、操作记录、容量预留和静态站点版本。Workers 模块已提供独立的运行配置，资源元数据和生命周期的开发目标见 [开发任务](roadmap.md)。Durable Objects 的存储能力随 workerd 集成验证。

项目拥有稳定的 project_id。创建项目在事务内写入项目和创建者的 owner 成员记录。项目列表和单项目读取同时校验当前会话账号的项目成员关系。项目成员列表对所有成员开放；所有者可以按邮箱把已有账号添加为 developer 或 viewer，或更新已有成员的角色，创建者的 owner 身份保持不变。非成员访问返回 404，成员越权修改返回 403。

## 当前接口

| 地址 | 方法 | 行为 |
| --- | --- | --- |
| /healthz | GET | 返回进程健康状态 |
| /readyz | GET | 返回服务就绪状态 |
| /api/v1/version | GET | 返回 version 与 commit |
| /api/v1/auth/login | POST | 验证账号并建立持久化会话 |
| /api/v1/auth/logout | POST | 清除当前会话 |
| /api/v1/auth/session | GET | 返回当前登录账号与可见项目 |
| /api/v1/environment | GET | 返回当前实例与数据库版本，需要登录 |
| /api/v1/projects | GET、POST | 列出当前用户可见项目，创建项目并授予创建者 owner 角色 |
| /api/v1/projects/{projectID} | GET | 读取当前用户可访问的项目 |
| /api/v1/projects/{projectID}/operations | GET | 按时间升序读取项目内操作 |
| /api/v1/projects/{projectID}/members | GET、POST | 列出项目成员；所有者添加已有账号或更新 developer/viewer 角色 |
| /api/v1/projects/{projectID}/sites/{siteName}/versions | POST | 所有者上传 ZIP 并登记待激活版本 |
| /api/v1/projects/{projectID}/sites/{siteName}/versions/{versionID} | GET | 项目成员查询指定版本及其产物摘要 |

两组监听端口完成初始化后才开始接受请求。管理环境启动前验证数据库连接和迁移版本，就绪接口同时检查数据库连接，数据库不可用时返回 503。

Gateway 使用配置中的 `host` 匹配请求域名，匹配时忽略大小写、末尾句点和请求端口。同域路由按 `path_prefix` 匹配最长的完整路径段前缀，省略前缀等同于 `/`。域名存在路径前缀路由时，包含 `.` 或 `..` 段的请求路径返回 400。前缀使用绝对路径，非根前缀省略末尾斜杠。`target` 支持 HTTP 与 HTTPS，转发时保留匹配的请求路径和查询参数。没有匹配路由时返回 404，上游连接失败时返回 502。

启动配置独占其中声明的域名。由运维通过配置文件为这些域名设置根路由和路径前缀。部署控制器等内部调用方可以通过 Gateway 的 Go 接口 `ReplaceRoutes` 为其他域名整体替换动态路由。同一域名和路径前缀仅允许配置一次。新路由通过校验后才整体生效。每个请求使用开始转发时选定的上游，在途响应继续传输。

`internal/sitehttp` 可通过 HTTP 提供已验证的只读站点文件系统，支持 GET、HEAD 和目录首页。缺少首页的目录返回 404。处理器可以独立使用，域名绑定和版本切换由发布流程接入。

静态站点以项目和名称确定身份。所有者使用 `Content-Type: application/zip` 上传，服务保存原始 ZIP，再展开并登记版本。上传体上限为 64 MiB，展开上限为 256 MiB 和 10,000 个 ZIP 成员。成功响应返回版本标识、SHA-256 摘要、`ready` 状态和用于查询的 `Location`。`ready` 表示文件已准备就绪，等待后续激活。

管理环境通过 PostgreSQL 保存账号与会话，使用 bcrypt 校验密码，使用 SCS 管理 HttpOnly 会话 Cookie。Cookie 名称包含实例名称，写入请求执行来源检查。开发账号经显式初始化流程创建，重复初始化保留已有密码。

默认 JSON 配置可独立运行静态 Gateway、健康检查和版本接口。完整管理环境通过 environment、instance、public_origin 和 CLOUDLET_DATABASE_URL 配置，缺少数据库或迁移版本不匹配时终止启动。账号接口需要已配置的管理数据库。

## 配置与生命周期

配置文件在启动时读取，未知字段、重复路由、无效地址或目标协议会阻止启动。使用 -config 指定配置文件。

API 与 Gateway 使用 HTTP 头读取超时和空闲连接超时。收到 SIGTERM 或 SIGINT 后停止接收连接，在十秒内等待已有请求完成。进程输出 JSON 日志。

## 容量预留

`internal/capacity` 在 PostgreSQL 事务中同时检查主机预算和项目配额。`Reserve` 以执行标识为全局幂等键，登记 CPU 毫核、内存字节与实例数；主机容量行和项目配额行按固定顺序加锁，因此多个项目共享同一主机预算时不会超额。`Release` 在调用方确认执行与副作用清理完成后释放，重复调用保持幂等，已释放的执行标识不能再次预留。`Usage` 汇总活动预留，执行结果不明时预留继续占用容量。写事务显式使用 READ COMMITTED，确保获得行锁后读取已提交的最新占用；`Usage` 使用 REPEATABLE READ 读取一致快照。降低主机容量或项目配额低于当前占用时返回冲突。Docker 准入由 #52 接入。

## 执行环境

Workers 集成 workerd，支持原生 Durable Object SQLite 绑定。真实运行时测试验证对象隔离和重启后的持久化。容器管理使用 Docker Engine API。internal/containers 提供创建、启动、观测、停止和删除受管理容器的适配器，按环境、项目、资源和执行序号写入归属标签与确定性名称，在启动、观测、停止和删除前核对名称和标签，启动后读取实际发布的回环端口。管理 API、Gateway 注册与配额由上层任务接入。Browser Run 复用容器生命周期，并通过 Playwright 实现浏览器操作。

执行单元、配额、隔离、状态恢复与独占交接遵循执行环境设计。对应模块完成时，同步提供 [真实业务验收](testing.md)。
