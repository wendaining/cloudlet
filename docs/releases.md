# 交付与发布

## CI

master 更新和 PR 变化时，CI 根据改动路径选择检查任务。路径规则位于 [.github/ci-paths.yml](../.github/ci-paths.yml)。

| 改动范围 | 执行检查 |
| --- | --- |
| docs 目录、Markdown、许可证与文档工具配置 | Markdown、文件链接与入口文档长度 |
| Go 源码、模块依赖与运行配置 | Go 格式、模块依赖、go vet、race 测试、构建与 govulncheck |
| 数据库、账号、管理接口与迁移 | 真实 PostgreSQL 上的迁移、账号、会话、项目成员角色、容量预留、项目容量查询、额度调整与重启验证 |
| 路由快照与 Gateway 动态路由 | 真实 PostgreSQL、HTTP 上游与 Gateway 的发布、冲突、恢复与并发验证 |
| Worker 运行配置、发布接口与镜像 | 真实 PostgreSQL、workerd 与 Gateway 的首发、权限、幂等、错误清理和 HTTP 验证 |
| Browser Run 执行服务与镜像 | 真实 Chromium 容器的认证、导航和退出验证 |
| 容器适配器与 Docker 执行 | 真实 Docker 中的创建、启动、观测、归属校验、HTTP 就绪探测、配额触发、停止与端口释放 |
| 前端与接口类型 | 前端静态检查、构建与生成一致性 |
| 开发入口、Compose 与实例配置 | 真实开发环境、热重载、停止、多实例与失败清理 |
| 页面、认证与页面测试 | 真实 Chromium 中的登录、会话与退出 |
| 自动化脚本与仓库配置 | JSON、YAML、Issue 表单、脚本语法与 ShellCheck |
| Actions 工作流与工作流工具依赖 | actionlint |
| 容器源码、依赖、Dockerfile 与容器配置 | 实际容器构建、启动、转发和退出验证 |
| 二进制打包脚本与发布工作流 | 四个平台的打包与校验和验证 |

Go 测试文件改动触发 Go 检查。CI 定义与路径规则改动触发全部相关检查。新增目录需要在路径规则中声明检查范围。版本发布前执行完整验证。

CI 汇总任务持续报告合并检查结果，确认选中的任务全部成功，其他任务按范围跳过。

PR 更新时检查最新内容。master 的每次推送独立执行对应范围的检查并保留结果。

README 最多 90 行，AGENTS.md 最多 70 行，CONTRIBUTING.md 最多 110 行。详细内容放入 docs。

容器检查在实际 Docker 中构建并运行服务，验证 readyz、CLI、Gateway 转发与未知域名响应。运行时使用普通用户、只读文件系统、资源限制和已移除的 Linux capabilities。

## 容器交付

Release 完成版本与提交验证、完整 CI 后调用 Delivery，将镜像发布到 GHCR。镜像同时使用版本标签与 sha- 加上完整提交标识，构建时写入 version 和 commit。

```sh
docker run --rm \
  --read-only --cap-drop=ALL --security-opt=no-new-privileges \
  --memory=128m --cpus=1 --pids-limit=128 \
  -p 127.0.0.1:8080:8080 -p 127.0.0.1:8081:8081 \
  ghcr.io/wendaining/cloudlet:v0.1.0
```

GHCR 包的访问权限由包设置管理。使用私有包的机器需要登录 GHCR。

容器提供 8080 管理 API 与 8081 Gateway。主机端口默认绑定回环地址。配置文件位于 /app/config.json，可以通过只读挂载替换。

镜像包含构建来源信息与 SBOM。交付流程负责构建和发布产物。部署机器使用提交对应的镜像启动服务，并检查 readyz、CLI 与路由结果。

## 二进制版本

维护者在 master 上通过验证的提交创建 v 开头、符合 SemVer 的版本标签并推送：

```sh
git tag v0.1.0
git push origin v0.1.0
```

Release 确认版本符合 SemVer、标签提交属于 master，执行完整 CI 并发布版本镜像，再为 Linux、macOS 的 amd64 与 arm64 构建服务端和 CLI。每个压缩包包含二进制文件、默认配置、文档和 LICENSE。

发布文件附带 SHA256SUMS。可以在本地验证发布构建：

```sh
sh scripts/release.sh dev
cd dist
shasum -a 256 -c SHA256SUMS
```

解压压缩包后执行 ./cloudlet-server -config configs/cloudlet.json，并在另一终端运行 ./cloudlet health。

## 发布权限

Release 调用的 Delivery 使用 GitHub 自动提供的仓库令牌写入 GHCR。Release 使用仓库令牌创建 GitHub Release。当前交付流程无需服务器 SSH 密钥。

运行失败时检查对应 Actions 日志，修复后按同一流程提交与验证。部署配置应使用经过验证的具体提交或版本。
