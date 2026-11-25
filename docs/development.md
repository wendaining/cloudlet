# 本地开发与服务器部署

开发环境独立运行在每位成员的电脑上。PostgreSQL 和执行环境通过本机 Docker 启动，Go 与前端开发服务器作为本机进程运行。服务器使用同一套技术、接口和数据库迁移，通过环境配置指定连接地址与运行参数。

本地开发入口已经接入数据库、账号与管理界面。服务器部署配置按后文约定实施，执行范围见 [实施清单](development-deployment-checklist.md)。

## 组件放置

| 组件 | 本地开发 | 服务器部署 |
| --- | --- | --- |
| PostgreSQL | 本机 Docker 容器，每位成员拥有独立数据卷 | Linux 服务器上的数据库容器，使用独立持久化数据卷 |
| Go 管理服务与 Gateway | 本机进程，便于调试与重新编译 | 运行已发布的应用镜像 |
| React 管理界面 | 本机 Vite 开发服务器，启用热更新 | 独立 Nginx 镜像，提供静态文件并代理管理 API |
| workerd、用户容器与浏览器 | 本机 Docker 管理的执行环境 | 服务器 Docker 管理的执行环境 |
| 数据库迁移 | goose 连接本机 PostgreSQL | 部署任务中的 goose 连接服务器 PostgreSQL |

macOS 上的 Docker 通过本机 Linux 虚拟机提供容器环境。涉及 Linux 运行环境的功能在这些容器中执行。

## PostgreSQL 连接

数据库连接通过 CLOUDLET_DATABASE_URL 配置。每个环境使用自己的连接信息、账户和数据。

| 项目 | 本地开发 | 服务器部署 |
| --- | --- | --- |
| Go 访问数据库的地址 | 127.0.0.1:15432 | postgres:5432 |
| 数据库容器端口 | 5432 | 5432 |
| 主机端口发布 | 绑定 127.0.0.1:15432 | 通过项目私有 Docker 网络访问 |
| 结构管理 | 执行仓库中的 SQL 迁移 | 执行同一套 SQL 迁移 |

本机进程通过发布的主机端口访问数据库。服务器上的应用容器通过 Compose 服务名 postgres 访问数据库容器。容器中的 127.0.0.1 指向该容器自身。

数据库与持久化目录按环境分别配置。停止开发服务保留本地数据，清理数据需要显式操作。服务器数据卷使用持久化存储并配置备份。

## 开发入口

安装 Go、Node.js、Docker Compose、Git、Make 和 ShellCheck。Node.js 版本文件为 .node-version。macOS、Linux 和 WSL2 在各自本机终端运行项目命令，WSL2 将源码保存在 Linux 文件系统中。

```sh
make setup
make doctor
make dev
```

make setup 安装 npm 与 Go 锁定依赖、提交检查和 Chromium，生成权限为 0600 的 .env.local。重复执行保留已有配置和凭据。Linux 页面测试需要 Chromium 的系统依赖，可通过 npx playwright install-deps chromium 安装。

make dev 检查配置与端口，使用 compose.yaml 和 compose.dev.yaml 启动本实例数据库，等待健康，执行 goose 迁移，准备开发账号，再启动 Air、Vite 与 TypeScript 检查。所有服务就绪后显示访问地址。初始化失败时停止本次启动的环境资源。

开发账号使用 CLOUDLET_DEV_EMAIL 与 CLOUDLET_DEV_PASSWORD，走正常登录流程。后续启动保留数据库中已有的账号与密码。生产环境禁止执行开发账号初始化。

| 命令 | 行为 |
| --- | --- |
| make doctor | 验证配置、Go、Node.js、ShellCheck、Docker、Compose 与端口 |
| make dev | 运行完整本机开发环境 |
| make dev DEBUG=api | 使用 Delve 运行后端，调试端口默认 2345 |
| make dev-status | 查看控制器、进程信息、地址与容器状态 |
| make dev-logs | 跟随当前实例的开发日志 |
| make db-status | 查询正在运行的数据库中的迁移状态 |
| make db-migrate | 对当前配置的数据库执行仓库迁移 |
| make dev-stop | 停止当前实例全部服务与受管理执行容器，保留数据 |
| make dev-reset CONFIRM=实例名称 | 停止实例并删除数据库、执行容器与相关数据卷 |

配置或迁移版本变化后，执行 make dev-stop，再运行 make dev，按新配置完成初始化。数据库版本高于当前代码要求时，启动直接返回错误。新增迁移使用新的 SQL 文件，同时更新 migrations/embed.go 中的版本。

## 配置与多实例

.env.example 列出本地配置字段。进程环境中的同名字段优先于配置文件，未知配置字段会使检查失败。Go、Compose 和管理命令使用同一份配置，前端进程只接收公开的端口与代理地址。

| 配置 | 用途 |
| --- | --- |
| CLOUDLET_INSTANCE | 实例名称，默认 default |
| CLOUDLET_API_PORT、CLOUDLET_GATEWAY_PORT、CLOUDLET_WEB_PORT | 本机服务端口，默认 8080、8081、5173 |
| CLOUDLET_DEBUG_PORT | Delve 监听端口，默认 2345 |
| CLOUDLET_DATABASE_URL | PostgreSQL 地址，包含数据库账号、密码与端口 |
| CLOUDLET_SECRET_KEY | 64 个十六进制字符的平台密钥，用于加密项目凭据版本 |
| CLOUDLET_DEV_EMAIL、CLOUDLET_DEV_PASSWORD | 首次准备开发账号时使用的凭据 |
| DOCKER_HOST、DOCKER_CONTEXT | 可选 Docker 连接配置，两项互斥 |

数据库保存平台密钥的 SHA-256 标识。已有凭据数据的环境必须继续使用对应密钥，密钥不匹配时服务终止启动。

未指定 Docker 连接时使用当前 Docker context。本地开发支持 unix:// 连接，Go SDK 和 Testcontainers 使用同一引擎。数据库镜像版本与 digest 锁定在 configs/images.json。数据库端口和应用监听端口均绑定回环地址。

配置管理数据库时，服务 JSON 需要设置 `site_storage_directory` 为平台独占的可写绝对路径。本地开发入口将其设为 `.local/实例名称/site-storage`。服务器部署需要为该路径提供持久化存储，并与数据库一起保留。目录下的 `archives` 保存原始 ZIP，`versions` 保存已解包文件。

创建另一个实例时执行 make setup ENV_FILE=.env.feature.local，编辑该文件中的实例名称、数据库端口和四个服务端口，再运行：

```sh
make dev ENV_FILE=.env.feature.local
make dev-stop ENV_FILE=.env.feature.local
```

每个实例使用独立的 Compose 项目、数据卷、端口、会话 Cookie 和 .local 子目录。同一用户的同名实例只允许一个活动开发控制器。

## 热更新与资源归属

Go 源码变化后由 Air 重新编译并启动后端。编译失败时停止旧后端并显示错误，修复后重新启动。Vite 提供页面热更新，TypeScript 检查持续显示类型错误。日志与调试产物位于 .local/实例名称。

Ctrl+C、SIGTERM 和 make dev-stop 停止本实例的应用进程、执行容器与数据库。停止保留数据库、数据卷与本地配置。reset 需要 CONFIRM 与实例名称完全一致，保留配置、日志和镜像缓存。

执行模块创建容器、网络和数据卷时使用以下标签。管理数据库使用 component=database，执行资源使用 component=runtime。容器适配器按环境、项目、资源和执行序号写入归属标签与确定性名称，并在启动、观测、停止和删除前核对。

| 标签 | 值 |
| --- | --- |
| io.cloudlet.managed | true |
| io.cloudlet.instance | 当前实例名称 |
| io.cloudlet.component | runtime |
| io.cloudlet.project | 项目标识 |
| io.cloudlet.resource | 资源标识，由对应模块设置 |
| io.cloudlet.generation | 执行序号 |

后端进程退出时，执行容器继续运行。完整停止由独立开发控制器根据标签执行。Worker、DO 与浏览器的元数据恢复和会话重新接管随各自模块完成，验收要求见实施清单。

make run 使用 configs/cloudlet.json 独立验证静态 Gateway 与基础接口。完整账号与前后端联调使用 make dev。测试入口见 [测试说明](testing.md)。

## 服务器部署

Compose 部署已发布的应用镜像和 PostgreSQL，注入该环境的数据库连接配置。应用、数据库和执行环境使用服务器上的网络、账户与数据卷。

发布前验证迁移，部署时执行对应版本的迁移，启动应用后检查就绪状态和业务接口。开发环境与服务器使用相同的 PostgreSQL 版本、运行时版本和迁移文件。

具体技术见 [技术栈](technology-stack.md)，镜像与版本发布流程见 [交付与发布](releases.md)。Docker 的连接与环境配置说明见 [PostgreSQL 指南](https://docs.docker.com/guides/postgresql/) 和 [Compose 部署指南](https://docs.docker.com/compose/how-tos/production/)。

## 本地启用 Browser Run 管理 API

在仓库根目录构建并配置 Chromium 会话镜像，然后启动开发环境：

```sh
export CLOUDLET_BROWSER_IMAGE="$(docker build --quiet runtimes/browser)"
export CLOUDLET_BROWSER_SECCOMP="$PWD/runtimes/browser/seccomp.json"
make dev
```

使用登录会话在项目下向 `POST /api/v1/projects/{projectID}/browser-sessions` 发出请求。收到 201 后，响应的 endpoint 可用于查询会话。向 endpoint 后追加 `/navigate` 并提交 JSON 正文 `{"url":"http://可从容器访问的测试地址/"}`，核对响应中的页面地址和标题。使用另一项目的账号查询同一会话应返回 404。

开发时管理进程需要连接本地 Docker Unix socket。测试页面需要监听容器能访问的接口。Linux 默认 Docker 网桥的网关地址可用 `docker network inspect bridge` 查看。不要把测试页面只绑定到宿主的 127.0.0.1。

发起 `DELETE` 到会话 endpoint 可以主动关闭会话，响应状态为 `closed`，重复调用仍返回终态。要检查自动回收，可在启动 `make dev` 的终端执行 `export CLOUDLET_BROWSER_SESSION_SECONDS=15`。该值只影响随后创建的会话，有效范围为 5 到 300 秒，默认 300 秒。

验收时分别创建一个主动关闭和一个等到期的会话。在会话运行时通过 `docker ps` 的 `io.cloudlet.resource=browser-<会话 ID>` 标签查到容器；关闭或到期后，对原容器执行 `docker inspect` 应查不到。在管理 API 查询会话应返回 `closed`，随后可在同一默认配额下创建新会话。
