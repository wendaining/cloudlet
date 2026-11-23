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
| internal/capacity | 主机与项目容量的事务预留、容器绑定、确认释放与占用查询 |
| internal/executions | 容量准入后的容器启动、绑定校验、失败清理与确认释放 |
| internal/apigen | OpenAPI 生成的类型、Gin 接口和客户端 |
| migrations | 内嵌 SQL 迁移文件与当前版本 |
| internal/gateway | 按域名选择上游并转发 HTTP 请求 |
| internal/server | 监听端口与服务生命周期 |
| internal/version | 构建版本和提交标识 |
| internal/workers | ES Module 与 Durable Objects 的 workerd 配置、启动参数和镜像引用 |
| internal/durableobjects | SQLite 命名空间、类绑定和存储挂载路径 |
| internal/containers | Docker Engine 镜像摘要准备、容器生命周期与回环端口或内部网络端点观测 |
| internal/browser | 复用容器管理的 Playwright 会话 |
| pkg/client | CLI 与其他客户端共享的 API 客户端 |

PostgreSQL 当前保存账号、登录会话、项目、操作记录与容量预留。Workers 模块已提供独立的运行配置，资源元数据和生命周期的开发目标见 [开发任务](roadmap.md)。Durable Objects 的存储能力随 workerd 集成验证。

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

两组监听端口完成初始化后才开始接受请求。管理环境启动前验证数据库连接和迁移版本，就绪接口同时检查数据库连接，数据库不可用时返回 503。

Gateway 使用配置中的 `host` 匹配请求域名，匹配时忽略大小写、末尾句点和请求端口。同域路由按 `path_prefix` 匹配最长的完整路径段前缀，省略前缀等同于 `/`。域名存在路径前缀路由时，包含 `.` 或 `..` 段的请求路径返回 400。前缀使用绝对路径，非根前缀省略末尾斜杠。`target` 支持 HTTP 与 HTTPS，转发时保留匹配的请求路径和查询参数。没有匹配路由时返回 404，上游连接失败时返回 502。

启动配置独占其中声明的域名。由运维通过配置文件为这些域名设置根路由和路径前缀。部署控制器等内部调用方可以通过 Gateway 的 Go 接口 `ReplaceRoutes` 为其他域名整体替换动态路由。同一域名和路径前缀仅允许配置一次。新路由通过校验后才整体生效。每个请求使用开始转发时选定的上游，在途响应继续传输。

`internal/sitehttp` 可通过 HTTP 提供已验证的只读站点文件系统，支持 GET、HEAD 和目录首页。缺少首页的目录返回 404。处理器可以独立使用，域名绑定和版本切换由发布流程接入。

创建单个站点处理器时可通过 `Options.SPAFallback` 启用导航回退，默认关闭。启用后，接受 `text/html` 的页面导航访问不存在且末段无扩展名的路径时，返回站点根目录的 `index.html`。静态文件请求和已有但缺少首页的目录仍返回 404。回退候选路径通过 `Vary` 声明导航相关请求头。

管理环境通过 PostgreSQL 保存账号与会话，使用 bcrypt 校验密码，使用 SCS 管理 HttpOnly 会话 Cookie。Cookie 名称包含实例名称，写入请求执行来源检查。开发账号经显式初始化流程创建，重复初始化保留已有密码。

默认 JSON 配置可独立运行静态 Gateway、健康检查和版本接口。完整管理环境通过 environment、instance、public_origin 和 CLOUDLET_DATABASE_URL 配置，缺少数据库或迁移版本不匹配时终止启动。账号接口需要已配置的管理数据库。

## 配置与生命周期

配置文件在启动时读取，未知字段、重复路由、无效地址或目标协议会阻止启动。使用 -config 指定配置文件。

API 与 Gateway 使用 HTTP 头读取超时和空闲连接超时。收到 SIGTERM 或 SIGINT 后停止接收连接，在十秒内等待已有请求完成。进程输出 JSON 日志。

## 静态文件验证与缓存

静态文件通过内容的 SHA-256 摘要生成强 `ETag`，文件修改时间可用时提供 `Last-Modified`。GET 和 HEAD 由 Go 标准库按 HTTP 顺序检查前置条件。`If-Match` 或 `If-Unmodified-Since` 的条件不满足时返回 412。通过这些前置条件后，`If-None-Match` 或 `If-Modified-Since` 命中时返回 304。412 响应声明 `Cache-Control: no-store`。HTML 每次使用前重新验证，其他静态文件可公开缓存 300 秒。处理器在文件元数据可识别变更时复用内容摘要，文件替换或内容写入会触发重新计算。提供不可变文件系统的调用方也可通过 `Options.Immutable` 启用摘要复用。

## 容量预留

`internal/capacity` 在 PostgreSQL 事务中同时检查主机预算和项目配额。`Reserve` 以执行标识为全局幂等键，登记 CPU 毫核、内存字节、实例数以及容器归属（环境、项目、资源、执行序号和镜像）；主机容量行和项目配额行按固定顺序加锁，因此多个项目共享同一主机预算时不会超额。活动预留的容器归属唯一，同一执行标识只能使用原归属重试，其他执行不能占用同一归属。`SetContainerID` 在容器创建后记录 Docker 容器标识。`Release` 在调用方确认执行与副作用清理完成后释放，重复调用保持幂等，已释放的执行标识不能再次预留；释放后的归属可以由新执行重新使用。`Usage` 汇总活动预留，执行结果不明时预留继续占用容量。写事务显式使用 READ COMMITTED，确保获得行锁后读取已提交的最新占用；`Usage` 使用 REPEATABLE READ 读取一致快照。降低主机容量或项目配额低于当前占用时返回冲突。

`internal/executions` 把容量预留接入 Docker 启动流程。`Start` 先按容器 CPU、内存与单个实例申请主机和项目容量，并在同一预留中持久化容器归属，预留成功后才创建并启动容器；容量不足时不会执行任何 Docker 操作。同一执行标识重试只能恢复原归属；更换归属、镜像或资源上限返回冲突，不会为同一预留创建第二个容器。创建失败时按确定性名称核对容器，没有容器才释放预留；存在本执行的容器时核对镜像与资源上限后恢复或复用。启动失败时先停止并删除已创建的容器，确认删除后才释放。清理或释放失败时保留预留并返回具体错误，避免平台超额分配。`Stop` 校验预留记录的主机、项目、容器归属和容器标识；记录标识存在而调用方标识不匹配时不会操作 Docker，也不会释放容量。尚未记录容器标识的预留按绑定归属查找，确认没有容器后释放。记录标识的容器已经不存在时同样按归属确认；找到其他标识时按归属清理但保留原绑定标识，使同一个 Stop 请求可以在释放失败后重试。重复停止保持幂等；已释放预留的重复停止直接返回，不按归属清理可能属于新执行的容器。确定性名称被归属标签不匹配的容器占用时，`Start` 和 `Stop` 返回归属错误并保留预留；运维需要先确认并移除该容器，再重试 `Stop` 释放容量。同一执行标识的并发启动和停止在进程内串行，执行结果不明时由调用方重试停止完成清理。主机预算由部署配置或上层管理接口扣除操作系统与固定服务后提供，容器按声明的上限申请剩余容量。

## 执行环境

Workers 集成 workerd，支持原生 Durable Object SQLite 绑定。真实运行时测试验证对象隔离和重启后的持久化。容器管理使用 Docker Engine API。internal/containers 按不可变摘要准备镜像，核对本地镜像报告的摘要后才用于创建容器，已缓存且摘要一致的镜像直接复用。适配器提供创建、启动、观测、停止和删除受管理容器的操作，按环境、项目、资源和执行序号写入归属标签与确定性名称，在启动、观测、停止和删除前核对名称和标签，并可按确定性名称查找已有容器。published 模式在启动后读取实际发布的回环端口。network 模式加入已有的用户定义 bridge 网络，为执行注册稳定的网络别名，并读取容器在该网络中的实际地址与端口。internal/executions 在该适配器之上完成容量准入、失败清理与确认释放。管理 API 与 Gateway 注册由上层任务接入。Browser Run 复用容器生命周期，并通过 Playwright 实现浏览器操作。

执行单元、配额、隔离、状态恢复与独占交接遵循执行环境设计。对应模块完成时，同步提供 [真实业务验收](testing.md)。
