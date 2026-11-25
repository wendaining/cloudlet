# Worker 发布

首个无状态 Worker 通过共享管理 API 发布，项目所有者和 developer 成员可以调用。项目成员可以查询版本、执行和 Operation。输入为已经打包的单文件 JavaScript ES Module，入口固定为 `index.mjs`，模块 UTF-8 字节上限为一 MiB。

## 运行配置

`make dev` 为每个实例生成产物目录、主机预算与项目初始配额。直接运行 Go 服务时，按 [本地开发与服务器部署](development.md) 配置管理数据库、实例、平台密钥和静态站点目录，并在 JSON 配置中声明 `workers`。

```json
{
  "workers": {
    "directory": "/srv/cloudlet/workers",
    "host_id": "local",
    "host_budget": {
      "cpu_millis": 4000,
      "memory_bytes": 4294967296,
      "instances": 4
    },
    "project_quota": {
      "cpu_millis": 2000,
      "memory_bytes": 1073741824,
      "instances": 2
    }
  }
}
```

预算须包含至少 1000 CPU 毫核、256 MiB 内存与一个实例。项目首次发布时登记配置中的初始配额，已有配额保持原值。每个执行使用固定的 1000 CPU 毫核、256 MiB 内存、128 个进程和 1024 个文件句柄上限。部署机器需要让 Go 服务能够访问 Docker 发布的回环端点。

初始化验证配置、目录、Docker Linux 引擎、固定镜像摘要与 PostgreSQL 控制锁。依赖失败时终止启动。控制连接失效时，取消正在执行的发布并拒绝新发布，已有容器继续运行。

## 管理 API

```text
POST /api/v1/projects/{projectID}/workers/{name}/deployments
Content-Type: application/json
Idempotency-Key: first-publication
```

```json
{
  "module": "export default { fetch() { return new Response('cloudlet') } };",
  "compatibility_date": "2026-10-12",
  "hostname": "worker.localhost"
}
```

Worker 名称由小写字母、数字和单个连字符分隔的名称段组成，最多 63 个字符。域名不包含端口。兼容日期使用固定 workerd 支持的日期，当前最晚为 2026-10-19。模块及配置按 SHA-256 保存为独立不可变产物。

发布先预留容量，再预检模块并启动实际 workerd。真实回环端点的 GET `/` 返回 200 至 399 后，在同一事务中登记活动版本、执行标识和域名路由。项目内其他路由保持有效。Gateway 应用已提交快照后返回 201，响应包含 `id`、`worker_id`、`execution_id`、`operation_id`、产物摘要、容器标识和操作终态。`Location` 指向版本查询地址。

同一 Worker 的相同幂等键与请求返回同一结果，已结束操作的重试返回 200，并保留成功或失败终态。更换请求内容时返回 409。存在未完成发布时，新的发布请求返回 409。已提交活动版本后的快照同步或收尾失败，可以通过重复原请求完成。显式切换已有活动版本由 #35 交付。

请求格式无效返回 400，未登录返回 401，查看者发布返回 403，非项目成员返回 404。域名冲突返回 409。模块预检或就绪失败返回 422，容量不足返回 429。已有版本记录的发布响应通过 `Location` 提供查询地址，包括激活时发生冲突和发布尚未完成的 409 响应。版本尚未登记时省略该响应头。失败版本保留原因与 Operation。确定失败时先确认容器删除，再释放容量。执行结果不明时保留容量与执行记录。

```text
GET /api/v1/projects/{projectID}/worker-deployments/{deploymentID}
```

查询返回不可变版本、最近执行与操作结果。Go 客户端使用 `pkg/client.PublishWorker` 和 `GetWorkerDeployment` 调用相同接口。MCP 的发布工具由 #47 接入。

## 验收

运行 `make dev`，使用项目所有者或 developer 会话发送上述发布请求。然后通过 Gateway 访问所声明的域名，核对 Worker 正文。读取 `Location`，比较上传模块的 SHA-256 与 `artifact_digest`，并用 Docker inspect 核对容器标识、归属标签和资源限制。用另一项目会话查询同一版本，确认返回 404。

自动验证命令与覆盖范围见 [测试说明](testing.md)。PR 审查者亲自执行发布、公开访问与元数据核对，并记录环境、实际操作和结果后批准。
