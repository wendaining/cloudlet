# 交付与发布

## CI

CI 检查 Go 格式与模块依赖、go vet、启用 race detector 的测试、二进制构建和 govulncheck。仓库检查包含 Markdown、相对文件链接、JSON 与 YAML、Issue 表单、Action 固定版本及 npm audit。

README 最多 90 行，AGENTS.md 最多 70 行，CONTRIBUTING.md 最多 110 行。详细内容放入 docs。

容器检查在实际 Docker 中构建并运行服务，验证 readyz、CLI、Gateway 转发与未知域名响应。运行时使用普通用户、只读文件系统、资源限制和已移除的 Linux capabilities。

## 容器交付

master 更新触发 Delivery。工作流完整执行 CI，通过后将镜像发布到 GHCR。镜像标签使用 sha- 加上完整提交标识，构建时同时写入 version 和 commit。

```sh
docker run --rm \
  --read-only --cap-drop=ALL --security-opt=no-new-privileges \
  --memory=128m --cpus=1 --pids-limit=128 \
  -p 127.0.0.1:8080:8080 -p 127.0.0.1:8081:8081 \
  ghcr.io/wendaining/cloudlet:sha-<完整提交标识>
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

Release 执行 CI，确认标签对应的提交属于 master，为 Linux、macOS 的 amd64 与 arm64 构建服务端和 CLI。每个压缩包包含二进制文件、默认配置、README 和 LICENSE。

发布文件附带 SHA256SUMS。可以在本地验证发布构建：

```sh
sh scripts/release.sh dev
cd dist
shasum -a 256 -c SHA256SUMS
```

解压压缩包后执行 ./cloudlet-server -config configs/cloudlet.json，并在另一终端运行 ./cloudlet health。

## 发布权限

Delivery 使用 GitHub 自动提供的仓库令牌写入 GHCR。Release 使用仓库令牌创建 GitHub Release。当前交付流程无需服务器 SSH 密钥。

运行失败时检查对应 Actions 日志，修复后按同一流程提交与验证。部署配置应使用经过验证的具体提交或版本。
