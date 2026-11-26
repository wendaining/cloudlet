# 容器创建与公开访问

项目所有者和 developer 成员可以通过共享管理 API 从不可变镜像摘要创建首个 HTTP 容器。平台完成容量准入、真实 Docker 启动与 HTTP 就绪检查后，才为项目域名注册 Gateway 路由。项目成员可以查询容器配置、执行与 Operation 结果。

## 运行配置

`make dev` 为每个实例生成 `containers` 配置，并与 Worker 共享主机预算和项目初始配额。直接运行 Go 服务时，按 [本地开发与服务器部署](development.md) 配置管理数据库、实例、平台密钥和静态站点目录，并在 JSON 配置中声明：

```json
{
  "containers": {
    "host_id": "local",
    "host_budget": {
      "cpu_millis": 4000,
      "memory_bytes": 4294967296,
      "instances": 4
    },
    "project_quota": {
      "cpu_millis": 4000,
      "memory_bytes": 4294967296,
      "instances": 4
    }
  }
}
```

预算须包含至少 1000 CPU 毫核、256 MiB 内存与一个实例。项目首次创建容器时登记配置中的初始配额，已有配额保持原值。同时配置 Worker 与容器运行时，两者的 `host_id`、主机预算和项目配额必须一致，否则服务终止启动。

初始化验证配置、Docker Linux 引擎、PostgreSQL 控制锁与路由快照。依赖失败时终止启动。控制连接失效时拒绝新请求，已有容器继续运行。容器执行固定使用 256 个进程和 1024 个文件句柄上限，CPU 与内存按请求声明申请并限制。

## 管理 API

```text
POST /api/v1/projects/{projectID}/containers
Content-Type: application/json
Idempotency-Key: first-container
```

```json
{
  "name": "web",
  "image": "nginxinc/nginx-unprivileged:1.29.3-alpine@sha256:5aea7cc516b419e3526f47dd1531be31a56a046cfe44754d94f9383e13e2ee99",
  "port": 8080,
  "hostname": "container.localhost",
  "cpu_millis": 500,
  "memory_bytes": 134217728,
  "user": "101:101",
  "health_check_path": "/",
  "health_check_timeout_ms": 15000
}
```

镜像必须包含 `repository@sha256:digest` 形式的不可变摘要，仅使用标签的引用返回 400。容器名称由小写字母、数字和单个连字符分隔的名称段组成，最多 63 个字符。域名不包含端口，平台注册根路径路由。`user` 必须是非 root 身份，省略时使用 `1000:1000`。`command` 覆盖镜像命令，`env` 使用 `KEY=value` 形式，请求体总大小最多 64 KiB。

创建流程先登记幂等 Operation 与不可变执行配置，再按摘要准备镜像、预留主机和项目容量、创建并启动真实容器。容器实际发布的回环端点在声明的截止时间内返回 200 至 399 后，平台在同一事务中登记活动执行、容器标识和域名路由。Gateway 应用已提交快照后返回 201，响应包含 `id`、`execution_id`、`operation_id`、镜像摘要、容器标识、实际端点与操作终态。`Location` 指向容器查询地址。

同一容器名称的相同幂等键与请求返回同一结果，已结束操作的重试返回 200，并保留成功或失败终态。更换请求内容时返回 409。容器已有活动执行或未完成创建时返回 409。已提交活动执行后的快照同步或收尾失败，可以通过重复原请求完成。

请求格式无效返回 400，未登录返回 401，查看者创建返回 403，非项目成员返回 404。域名冲突返回 409。镜像准备、容器启动或 HTTP 就绪失败返回 422，容量不足返回 429。确定失败时先停止并删除已创建的容器，确认清理后释放容量，再记录失败原因。清理失败时保留容量和待处理执行，避免平台超额分配。名称或域名冲突发生在容器执行记录写入前时，失败原因同样按 Operation 保存，同一幂等键重试返回原冲突结果。

```text
GET /api/v1/projects/{projectID}/containers/{containerID}
```

项目成员可以查询容器配置、最近执行与操作结果。非成员返回 404。

## 验收

运行 `make dev`，使用项目所有者或 developer 会话发送上述创建请求。响应成功后通过 Gateway 访问声明的域名，核对容器返回的 HTTP 内容；读取 `Location`，用 Docker inspect 核对容器标识、归属标签、镜像摘要、资源限制、非 root 用户和回环端口。

使用仅含标签的镜像引用确认返回 400；使用不存在的镜像摘要确认镜像准备失败返回 422、Operation 失败且没有留下 Docker 容器。再使用有效镜像和会立即退出的命令创建另一个容器，确认返回 422、Operation 失败、容器被删除且项目容量恢复。使用超过项目配额的 CPU 创建容器，确认返回 429 且没有创建 Docker 容器。

自动验证命令与覆盖范围见 [测试说明](testing.md#容器创建验收)。PR 审查者亲自执行创建、公开访问、Docker 核对和失败清理，并记录环境、实际操作和结果后批准。

## 当前限制

- 本任务只交付首个容器的创建、就绪和公开路由。停止、再次启动与删除由 #42 交付，数据卷持久化与独占写入由 #41 交付。
- 创建请求不接受数据卷声明；当前容器使用镜像自身的可写层。
- 容器端口发布在宿主回环地址，由同一主机上的 Gateway 转发。容器管理 API 尚未切换到项目网络；项目网络生命周期、归属标签校验和删除保护已在 `internal/containers` 提供，接入和容器接管、异常退出恢复由后续任务完成。
- 修改镜像或配置产生新执行、活动版本切换和旧执行排空尚未接入。
