# 测试与验收

所有 HTTP、数据库、容器与页面流程使用实际依赖。运行完整检查前执行 make setup，并确认本机 Docker 可以访问。

| 命令 | 检查范围 |
| --- | --- |
| npm run lint:docs | Markdown、链接与入口文档长度 |
| npm run lint:config | JSON、YAML、Issue 表单与 Actions 约定 |
| npm run lint:tooling | 脚本语法、ShellCheck、配置文件与真实端口检查 |
| make test | Go race 测试、真实 HTTP、Gateway 转发与服务停止 |
| make test-integration | Docker 中的 PostgreSQL 迁移、账号、会话、项目与成员角色、操作记录、项目凭据、路由快照、容量预留、容器容量准入、项目容量查询与额度调整、就绪检查、workerd 执行、管理 API 容器创建、Gateway 访问与停止/再次启动/删除，以及容器启动、观测、HTTP 就绪探测、配额触发、停止与删除 |
| make web-check | TypeScript、ESLint、Prettier、Vite 构建与接口生成一致性 |
| make dev-check | 真实本机进程、数据库、热重载、完整停止、多实例与失败清理 |
| make test-e2e | 真实 Chromium 在桌面和手机尺寸下完成登录、刷新和退出 |
| make generate-check | Go 接口生成结果与契约的一致性 |
| make container-check | Go 镜像构建、实际启动、CLI、路由与正常退出 |
| make check | 执行格式、静态检查、测试、构建、依赖审计与上述本地集成检查 |

make generate 从 api/openapi.yaml 同时生成 Go 类型、Gin 接口、客户端和前端类型，生成文件随实现提交。Go 和前端的检查分别比较临时生成结果。

静态站点分页集成测试通过管理 API 上传真实 ZIP，验证站点和版本各读取两页、相同创建时间的版本排序、翻页期间新增版本，以及不同项目之间的隔离。

```sh
go test -race -count=1 -tags=integration -run '^TestStaticSiteListsHTTP$' ./internal/database
```

## 开发流程验证

Gateway 域名验收构建并启动实际管理服务，以真实站点文件作为 HTTP 上游。用例验证域名大小写、末尾句点与端口规范化，GET 与 HEAD 正常转发，未知域名返回 404。停止上游后，该域名返回 502 并记录失败域名，其他上游继续可用。

```sh
go test -race -count=1 -run TestGatewayDomainRoutingAcceptance -v ./internal/server
```

静态站点首次发布使用真实 PostgreSQL、管理 API 和 Gateway 验证。测试先上传真实 ZIP，再通过管理 API 激活版本并经 Gateway 读取首页和资源，同时检查未激活版本的资源、域名冲突、跨项目权限、重复请求和路由恢复。

```sh
go test -race -count=1 -tags=integration -run '^TestFirstStaticSitePublicationHTTP$' -v ./internal/database
```

开发流程测试创建独立配置与随机可用端口，通过与成员相同的开发入口运行 Go、Vite 和 PostgreSQL。测试验证同名控制器拒绝重复启动、错误的 reset 确认不会停止服务、真实 API 登录和 Gateway 转发。

热重载检查在测试期间创建独立 Go 源文件，验证实际后端进程更换、会话保留，以及受管理 PostgreSQL 执行容器保持原始启动时间。随后验证真实编译错误会停止旧后端，修复源码后服务恢复。测试完成后删除该源文件。

完整停止验证端口释放、执行容器停止、数据库数据保留和再次启动后的登录会话。多实例检查验证一个实例停止后另一个实例继续提供服务。迁移版本不兼容时验证启动失败与资源清理。

页面测试使用独立环境，验证错误密码、登录成功、刷新后的会话、退出和页面宽度。截图与失败记录位于 .local/playwright，环境日志与配置位于 .local/checks 和各测试实例目录。

测试结束后清理所属容器、网络与数据卷。镜像缓存继续复用。文档检查独立于代码、数据库和浏览器流程。

## 容量预留验收

容量预留使用真实 PostgreSQL 验证同一事务内的主机与项目额度检查。

```sh
go test -race -count=1 -tags=integration ./internal/capacity
```

用例并发申请最后一份容量，确认只有一个请求成功；同一执行标识重复申请只产生一条活动预留；不同项目共享主机预算；释放后其他项目可以重新申请；已释放标识不能再次预留；配额低于当前占用时返回冲突。容器绑定用例验证执行标识与归属双向唯一、不同归属复用执行标识返回冲突、容器标识记录与重复记录幂等、无绑定预留补充绑定，以及释放后归属可以由新执行重新使用。回归用例将数据库默认隔离级别设为 REPEATABLE READ，验证写事务仍显式按 READ COMMITTED 读取最新占用，并发申请最后一份容量仍只有一个请求成功。

## 容器容量准入验收

容量准入使用真实 PostgreSQL 和 Docker 验证预留先于容器启动，以及确认清理后才释放容量。

```sh
go test -race -count=1 -tags=integration ./internal/executions
```

用例在四核四 GiB 主机预算下预占三核三 GiB 与三个实例，再并发提交八个一核一 GiB、单实例的容器启动请求。只有一个请求成功，其余返回容量不足且没有创建容器；成功启动的固定镜像通过实际发布的回环端口返回预期页面。仅停止容器时预留保持活动；停止并删除容器后容量恢复，后续申请可以再次成功。无效镜像导致创建失败时释放预留且不留下容器；无效用户使 Docker 启动失败时清理容器并释放预留。同一执行标识的并发启动只创建一个容器，并只扣减一次容量。同一执行更换归属或镜像、不同执行复用同一归属都会被拒绝；错误容器标识不会释放容量，绑定容器被外部删除后按归属确认清理。尚未记录容器标识的预留可以在确认没有容器后释放；替换容器清理后释放失败时，原 Stop 请求仍可使用原容器标识重试。256 至 512 字节的摘要固定镜像可以通过准入；Docker 创建失败时正常释放预留。生命周期用例使用真实 PostgreSQL 与 Docker，纯逻辑单元测试只覆盖构造与请求校验、绑定映射和复用配置比较。

## 容器创建与生命周期验收

使用真实 PostgreSQL、Docker、管理 HTTP API 与 Gateway 验证容器创建、就绪、公开访问、停止、再次启动和删除。

```sh
export DOCKER_HOST="$(docker context inspect --format '{{.Endpoints.docker.Host}}')"
export TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE=/var/run/docker.sock
go test -race -count=1 -tags=integration -run TestContainerPublicationHTTP -v ./internal/containerpublish
```

用例创建真实项目与成员，验证查看者 403、非成员 404、未登录 401、缺少幂等键与无效请求 400。使用 configs/images.json 中固定镜像和摘要创建容器，核对 201 响应、Operation 成功、Docker 容器运行、归属标签、非 root 用户、CPU、内存、进程与文件句柄限制和回环端口。通过 Gateway 使用大小写与末尾句点变化的域名访问容器，核对实际 HTTP 响应。重复幂等请求返回 200 且不创建第二个容器；更换请求内容、容器名称或域名冲突返回 409，同名冲突和域名冲突使用同一幂等键重试时返回原失败结果。使用会立即退出的命令验证 422、失败原因、容器删除与容量释放；超额 CPU 返回 429 且不创建容器。查询接口验证项目成员可读、非成员返回 404。

生命周期用例停止活动容器，核对公开域名返回 404、Docker 容器已删除、执行和容器状态为 `stopped`、项目容量释放；重复同一幂等键停止返回相同状态。再次启动后核对新的执行序号和 Docker 容器、公开域名恢复响应、容量重新占用。删除后核对公开域名和查询均返回 404、Docker 容器不存在、容量释放；重复同一幂等键删除返回 204。查看者调用生命周期接口返回 403，非成员返回 404。

测试结束后清理所属容器。镜像缓存继续复用。

## 项目容量查询验收

项目容量查询使用真实 PostgreSQL 和 HTTP 管理 API，验证项目成员读取项目额度、活动预留占用与剩余量。

```sh
go test -race -count=1 -tags=integration ./internal/controlplane
```

用例创建两个实际预留，确认查询结果等于两项之和；释放其中一项后再次查询，确认占用减少且剩余量增加；查看者可以读取，非成员、未知项目和未配置配额返回 404，未登录返回 401。接口在 REPEATABLE READ 只读事务中读取配额和占用，两个值来自同一快照。剩余量只表示项目配额内的余量，不表示主机当前余量。

## 项目额度调整验收

项目额度调整使用真实 PostgreSQL、HTTP 管理 API 和实际 Docker 容器，验证所有者提高或降低额度、拒绝低于活动占用的设置，以及拒绝非法单位。

```sh
go test -race -count=1 -tags=integration -run 'TestProjectCapacityAdjustment' -v ./internal/controlplane
```

数据库用例先创建实际预留，确认所有者可以调高额度并取得同一事务内的额度、占用与剩余量；把 CPU、内存或实例额度分别降到占用以下时返回 409，额度与活动预留保持不变；额度等于占用时允许设置且剩余量为零；释放预留后可以继续降低。未配置配额的项目可以由所有者首次设置；查看者、开发者、非成员和未登录请求分别返回 403、403、404 和 401；零值、负值、缺失字段、小数、字符串、未知字段和多余 JSON 内容返回 400。并发降低额度与申请容量的用例确认两者恰好一个成功，最终占用不会超过额度。真实容器用例通过 `internal/executions` 启动固定 nginx 镜像，把额度调高后调低到容器占用以下，确认接口返回 409 且容器继续运行；停止并删除容器后才允许继续降低额度。

## 项目凭据验收

项目凭据使用真实 PostgreSQL 和 HTTP 管理接口验证。

```sh
go test -race -count=1 -tags=integration -run TestProjectSecrets ./internal/database
```

用例保存同一凭据的两个版本，检查元数据接口只返回名称与版本信息，数据库密文不包含明文，日志不包含凭据值。所有者通过值接口读取明文，其他成员收到 403，非成员收到 404。重新连接数据库和重启管理接口后使用同一平台密钥读取版本。交换版本密文后认证失败，非规范项目 UUID 可以规范读取。错误平台密钥阻止写入和服务启动，原版本序列保持不变。不存在或仅属于其他项目的凭据名称与版本返回 404。请求覆盖 ASCII、JSON 转义和多字节字符的 65536 字节边界。并发创建同一凭据的版本时，版本号保持唯一且连续。

## 路由快照验收

项目路由使用真实 PostgreSQL、HTTP 上游和 Gateway 验证。

```sh
go test -race -count=1 -tags=integration ./internal/routes
```

用例发布两个项目的路由并分别访问，移除一个项目后另一个继续可用。跨项目在同一域名下发布不同路径前缀时返回冲突，失败发布保留原路由与快照版本。并发申请同一域名时只有一个项目成功，全部路由移除后其他项目可以使用该域名。启动配置域名不能用于动态路由，拒绝后快照版本不变化。新建 Publisher 从数据库恢复路由。并发发布多个项目后全部路由可访问。

进程恢复用例构建并启动实际管理服务，访问两个项目的域名。停止并重启进程后，两条路由继续可用。撤销一个项目的路由后再次启动，另一个项目仍可访问。

## workerd 集成验证

Worker 首发验收使用真实 PostgreSQL、Docker、workerd、管理 HTTP 接口与 Gateway。

```sh
export DOCKER_HOST="$(docker context inspect --format '{{.Endpoints.docker.Host}}')"
export TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE=/var/run/docker.sock
go test -race -count=1 -tags=integration -run TestWorkerPublicationHTTP -v ./internal/workerpublish
```

用例通过项目会话发布真实 ES Module，核对公开 HTTP 响应、不可变产物摘要、容器归属与资源限制。验证所有者与开发者发布、查看者只读、跨项目拒绝、共享客户端调用和并发幂等。未完成发布的 409 响应提供已登记版本的 `Location`。另一项目在候选 Worker 准备期间实际发布同域路由，验证激活冲突的 409 响应可通过 `Location` 查询失败版本，容器与容量已经释放，已发布的项目路由继续响应。版本登记前的冲突省略查询地址。语法、初始化、就绪、容量与真实存储权限错误保留原始原因，确认失败执行停止后释放容量。重建控制器后查询原版本并访问原执行。真实终止 PostgreSQL 控制连接后，新发布被拒绝，已有 Worker 继续响应。

通过管理 API 发布一个带 SQLite Durable Object 的 Worker，用 Gateway 分别写入和读取两个对象，验证对象隔离与事务回滚。通过 Docker 重启同一执行后，从 Docker 观测到的实际回环端点读取对象，验证对象标识和已提交值保持一致。

使用当前 Docker 连接单独验证 Worker 运行配置。

预检用例使用真实 workerd 验证模块成功、语法错误、初始化错误和缺失静态依赖。顶层输出超过 5 MiB 后，成功模块仍返回 ready，初始化错误保留对应阶段和末尾诊断。

```sh
export DOCKER_HOST="$(docker context inspect --format '{{.Endpoints.docker.Host}}')"
export TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE=/var/run/docker.sock
go test -race -count=1 -tags=integration -v ./internal/workers
```

在 Docker Engine 29.5.2 的 `linux/arm64` 环境中，workerd `1.20261012.1` 执行 `POST /echo`，返回 HTTP 200、`X-Worker: workerd` 和响应正文 `POST /echo cloudlet`。无效兼容日期 `2099-01-01` 使进程以状态码 1 退出，并报告运行时支持的最晚日期 `2026-10-19`。入口用例验证了缺失文件、目录和越界路径的诊断。镜像内的 `linux/arm64` 二进制文件与 Cloudflare 发布文件解压后的 SHA256 一致。

同一入口中的 `TestWorkerdPersistsDurableObjectSQLite` 通过 HTTP 调用名为 `alpha` 和 `beta` 的两个对象。两个对象使用相同的 SQL 表和键保存各自的值。测试停止第一个容器，用原数据卷启动新容器，然后确认两个对象的标识和各自的值都保持不变。

`TestWorkerPreflightReportsRuntimePhase` 使用真实 workerd 分别载入可运行模块、语法错误模块和初始化报错模块，检查返回阶段为 `ready`、`load` 和 `initialize`。

## Worker 构建验证

`runtimes/workers` 提供 TypeScript Worker 构建命令。默认入口为脚本目录下的 `src/index.ts`，默认输出为 `dist/index.mjs`。显式传入的相对路径按调用目录解释。`--entry` 必须属于 `--tsconfig` 的实际程序，未包含时终止构建。命令先执行 tsc 类型检查，再用 esbuild 打包为单文件 ES Module。

```sh
npm run check --workspace runtimes/workers
npm run build --workspace runtimes/workers
```

`TestWorkerBuildRunsInWorkerd` 使用同一示例源码构建两次并比较 SHA-256，再用固定 workerd 调用构建结果。`TestWorkerBuildReportsTypeErrors` 修改类型后验证构建返回 TS2322 诊断。

Node 测试验证不同调用目录的产物字节一致，配置外入口在写入产物前被拒绝，参数中的等号完整保留，未知参数和缺少参数值时终止构建。Workers CI 执行这些测试。

## Browser Run 验证

在 Node.js 24 和本机 Linux Docker Engine 下执行：

```sh
npm --prefix runtimes/browser ci --ignore-scripts
npm --prefix runtimes/browser run check
docker build -t cloudlet-browser:check runtimes/browser
npm --prefix runtimes/browser run test:integration
```

集成测试自行构建镜像，启动非 root Chromium 容器，并使用测试进程提供的真实 HTTP 页面验证导航地址和标题。测试解码截图并检查 1280 × 720 的尺寸与页面像素，也覆盖无效凭据、没有可截图页面和超出 2 MiB 上限的结果。测试让 Chromium 等待未响应的页面或字体，分别验证导航和截图返回 504 并退出，并检查收到 SIGTERM 后正常退出。期限用例验证 RFC 3339 时间边界、纳秒格式下容器自行退出以及 Chromium 启动失败时保留实际错误。容器通过 `host-gateway` 访问测试页面，内部接口只发布至主机回环地址。Docker 主机需支持 Chromium sandbox 使用的用户命名空间。

## 容器执行适配器验收

使用真实 Docker 启动 configs/images.json 中的固定镜像，读取适配器观测到的回环端口并请求预期响应。network 模式通过 `EnsureNetwork` 创建项目网络，核对容器没有发布主机端口，读取容器在该网络中的实际地址与稳定别名，并从同一网络的客户端容器通过 IP 与 DNS 名称分别发起真实 HTTP 请求。Docker Desktop 与 WSL2 中宿主机不能直接访问用户定义网络的容器地址，因此内部端点由同网络客户端验证。网络在创建与启动之间被删除时，启动返回 `ErrNetworkNotFound` 且容器保持 `created`；Docker 把网络 ID 前缀解析为其他网络时同样归类为 `ErrNetworkNotFound`。名称或归属标签不匹配的容器不会被启动、观测、停止或删除。停止和删除后容器、网络端点与端口释放。

项目网络验收为两个项目分别创建网络，核对确定性名称、bridge/local 形状和全部归属标签，并从同一项目的客户端通过 IP 与稳定别名访问服务。另一个项目的客户端访问相同端点失败。容器加入另一项目的网络、未管理网络或标签不匹配的网络时返回 `ErrNetworkNotOwned`，不会创建容器。同一项目已存在其他名称的管理网络时 `EnsureNetwork` 返回冲突。网络仍有运行或已停止容器时删除返回 `ErrNetworkInUse`，删除容器后删除成功并确认网络不存在，重复删除返回 `ErrNetworkNotFound`。

镜像准备使用真实 Docker Engine 和本地 registry。测试从空缓存按摘要拉取导入的测试镜像，核对返回的镜像标识与摘要；停止 registry 后再次准备，确认直接复用已核对摘要的本地镜像；再以不存在的摘要请求拉取，确认返回包含具体镜像引用的拉取失败。registry 镜像按 digest 固定，缺失时由 Docker Engine 拉取。

HTTP 就绪探测使用真实 Docker 启动延迟就绪、持续返回 503 和启动后退出的服务。延迟服务在探测期间连接失败，随后返回 200；持续 503 的服务在截止时间后返回 `ErrNotReady` 并保留最后状态；启动后退出的容器在截止时间前快速失败并报告退出码。未发布端口与不匹配归属分别返回明确错误；端口在探测期间暂时消失时，保留最近一次 HTTP 状态并继续报告端口缺失原因。单元测试还覆盖状态码已返回但响应体保持打开的流式服务，确认收到状态码后立即结束本次尝试，不等待响应体；100–199 的 1xx 状态码在访问 Docker 前被拒绝，避免配置后等待到超时。

```sh
make test-integration
# 只运行容器适配器
go test -race -count=1 -tags=integration ./internal/containers
# 只运行项目网络生命周期、隔离与归属限制
go test -race -count=1 -tags=integration -run TestAdapterProjectNetwork ./internal/containers
# 只运行内部网络端点
go test -race -count=1 -tags=integration -run TestAdapterNetworkAccess ./internal/containers
# 只运行启动时的网络缺失与网络 ID 前缀回归
go test -race -count=1 -tags=integration -run 'TestAdapterStartReportsMissingNetwork|TestAdapterCreateRejectsNetworkIDPrefix' ./internal/containers
# 只运行镜像准备
go test -race -count=1 -tags=integration -run TestAdapterPrepareImage ./internal/containers
# 只运行 HTTP 就绪探测
go test -race -count=1 -tags=integration -run TestAdapterWaitReady ./internal/containers
```

测试结束清理所属容器、本地 registry 与拉取的测试镜像。

## 容器配额验收

使用真实 Docker 和 configs/images.json 中的固定镜像，在受限容器内运行有限负载，核对 Docker 实际设置与触发结果。负载依赖固定镜像中的 busybox 工具（sh、timeout、head、tr、awk 与 ulimit），镜像工具集变化时需要同步调整。`containers.ProbeHostLimits` 读取 Docker Engine 报告的主机信息，按 CPU CFS 配额与周期、内存限制、PIDs 限制和 Linux nofile ulimit 报告宿主支持范围；不支持的项在验收中跳过并记录 Docker 给出的原因。

```sh
go test -race -count=1 -tags=integration -run 'TestProbeHostLimits|TestAdapterLimitsAcceptance' -v ./internal/containers
```

用例先用 docker inspect 核对创建时的 NanoCPUs、Memory、PidsLimit 与 nofile 软硬限制，再观察实际负载。内存负载的分配量超过 Docker 实际设置的 MemorySwap 上限；宿主没有限制内存交换（SwapLimit 为 false 或 MemorySwap 不大于 0）时，该用例跳过并记录原因：

| 限制 | 受限负载 | 预期触发结果 | 对照负载 |
| --- | --- | --- | --- |
| CPU | 500 毫核，忙循环约 2.5 秒 | CPU 用量低于单核满负荷，throttled_periods 大于 0 | — |
| 内存 | 64 MiB，分配并写入 200 MB | 容器被 OOM 终止，State.OOMKilled 为 true 或退出码为 137 | 1 GiB 时完成分配 |
| 进程 | 16 个 PIDs，循环创建 100 个进程 | 创建失败并以非零状态退出 | 128 个 PIDs 时创建 100 个进程成功 |
| 打开文件 | 64 个文件句柄，打开 200 个文件 | 打开失败并以非零状态退出，ulimit -n 为 64 | 1024 个句柄时打开 200 个文件成功 |

测试结束停止并删除容器，逐个确认容器不存在，再按本次验收的环境标签确认没有遗留容器。宿主支持范围随测试日志输出，实际结果按环境记录：

| 环境 | Docker Engine | cgroup | CPU | 内存 | PIDs | 打开文件 | 交换限制 |
| --- | --- | --- | --- | --- | --- | --- | --- |
| WSL2 / Docker Desktop 4.99.0 | 29.9.3 linux/amd64 | v2 / cgroupfs | 支持 | 支持 | 支持 | 支持 | 支持 |

CI 的 Containers 任务在标准 Linux Docker 中执行同一用例；其他平台的结果需要单独记录。

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
