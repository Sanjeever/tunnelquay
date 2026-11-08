# 开发与部署

本文说明开发、运行、验证和发行操作。历史验证结果与环境限制统一见 [验收记录](verification.md)。所有命令在仓库根目录执行。

## 开发

Go 版本要求见 [go.mod](../go.mod)，前端依赖见 [web/package.json](../web/package.json) 与 [锁文件](../web/pnpm-lock.yaml)。Node 与 pnpm 版本沿用 [CI 配置](../.github/workflows/ci.yml)。程序发布版本以 `web/package.json` 为唯一来源，普通 `go build` 显示 `dev`；发行工具读取该版本并注入版本、提交和发行公钥。

源码构建命令见 [README 从源码开始](../README.md#从源码开始)。前端构建产物输出到 `internal/webui/dist`，构建服务端前必须先生成该目录；生成产物不提交。

执行 `go run ./cmd/tunnelquay-server -listen 127.0.0.1:7000` 查看管理端。开发前端可执行 `pnpm --dir web dev`，API 代理到本机 7000 端口。

## 前台运行

服务端与客户端启动命令见 [README 从源码开始](../README.md#从源码开始)，完整参数与默认值见 [服务端入口](../cmd/tunnelquay-server/main.go) 与 [客户端入口](../cmd/tunnelquay-client/main.go)（或执行 `-h`）。多客户端实例需通过 `-data-dir` 指定各自的身份目录。进程收到中断或 SIGTERM 后清理退出。开放接入和业务映射端口由用户自行配置防火墙。

Web 反向代理转发 HTTP 到服务端监听端口，保留 Host，并在 HTTPS 入口设置 `X-Forwarded-Proto: https`。客户端直连公网 IP 或域名和接入端口，不经过 HTTP 反向代理。未保存客户端接入地址时，默认由当前请求主机名与服务端监听端口推导；若经反向代理访问且浏览器域名不能直接用于客户端 TCP 接入，需在设置中保存客户端直连的 `主机:端口`。

备份时先停止服务端，再复制完整数据目录（包含 SQLite、`identity.json` 与发行包缓存）；身份文件丢失会使现有凭据失效。恢复时使用同一目录。客户端升级和重启保留完整身份目录（包含 `device-key.pem` 私钥）；私钥备份应放在受保护的位置。

## 身份与撤销

设备密钥认证、身份撤销、离线设备清理与登记凭据重置的契约见 [信任边界](design.md#信任边界)。

私钥丢失或损坏时，使用新的客户端数据目录登记新身份，并在设备页撤销原身份；损坏私钥不会被程序覆盖。Unix 私钥文件按 0600 创建，Windows 应使用用户私有目录并保护目录 ACL。

## 行为验证

先完成前端构建，再执行 `go test ./... -timeout=5m` 与 `go vet ./...`。非 `-short` 测试会额外运行代表性负载（`TestRepresentativeLoad`）与实际客户端替换、退出、重启升级验证（`TestActualClientReplacementExitAndRestart`）。系统在发送阶段以 `EMSGSIZE` 拒绝 65507 字节 UDP 报文时，最大数据报子用例明确跳过，8192 字节用例仍执行。全部验证覆盖范围与平台记录见 [验收记录](verification.md)。

竞争检测需支持 CGO 的本机工具链；CI 命令为 `go test -race ./internal/... -short -timeout=3m`，`-short` 会跳过代表性负载与实际升级。

浏览器检查先构建两个程序到 `bin/`（Windows 使用 `.exe`），再执行 `pnpm --dir web exec playwright test`。测试自行启动临时服务端、客户端和本地 TCP/UDP 目标，检查管理操作与真实转发。截图与失败跟踪位于 `web/test-results`，不提交。测试结束删除隔离数据。

慢链路回归默认跳过，也未接入 CI。在支持网络命名空间的 Linux 上，准备 `unshare`、`ip`、`tc`，完成前端构建后，以有权创建网络命名空间和配置 netem 的用户执行：

```sh
bash scripts/test-slow-link.sh transfer
bash scripts/test-slow-link.sh recovery
```

[脚本](../scripts/test-slow-link.sh) 在隔离网络命名空间中设置延迟、抖动、丢包和带宽限制。`transfer` 注入短时完全丢包，检查双向内容校验、传输期间管理操作与接入连续性；`recovery` 注入持续断网，检查旧连接中断、同身份恢复、尊重离线停用和重新启用后的新连接。数据量可由 `TUNNELQUAY_SLOW_BYTES` 调整，参数及默认值见脚本与 [测试](../internal/server/slow_test.go)。隔离回归不能替代公网复验。

## 客户端升级

在设备页打开“客户端升级”，选择设备。展开“发行包”可查询官方版本并下载，或导入对应的 `release-manifest.json`、`release-manifest.sig` 与客户端归档。缓存通过签名验证后才成为升级候选。选择版本后发起任务。

升级成功后客户端退出码为 0。Linux 外部守护应采用 `Restart=always`，避免 `Restart=on-failure` 跳过正常退出后的重启。前台运行需手动再次执行原接入命令。Windows 可使用用户已有守护工具，macOS 可使用用户配置的 launchd；项目不安装系统服务。新版启动失败需人工替换程序，保留身份目录。

发行工具将公钥注入构建变量 `github.com/Sanjeever/tunnelquay/internal/update.PublicKey`，配置来源见下节。普通开发构建没有该公钥，会拒绝远程升级验证。Windows 运行中的旧文件可能保留为隐藏的 `.tunnelquay-client.exe.old`，确认新版可用且旧进程退出后可人工清理。

缓存容量达到上限时，先停止服务端，再清理数据目录 `releases` 下不再使用的版本；保留进行中升级任务所需的包。容量与包大小限制见 [资源边界](design.md#资源边界)。

## 版本与发行

`node scripts/version.mjs print` 读取唯一版本来源。`node scripts/version.mjs check` 校验版本及中文日志，附加标签参数如 `v0.1.0` 时校验一致性。升级版本前先编写对应 `docs/releases/<version>.md`，然后执行 `node scripts/version.mjs bump patch`，也可指定 `minor`、`major` 或完整 SemVer。缺少日志时不修改版本文件。

完成前端构建后执行 `go run ./scripts/release -test-key -output bin/validation-release`，构建 [发行工具](../scripts/release/main.go) 中的平台矩阵，输出各平台客户端与服务端归档、清单、签名、校验文件与测试公钥标记。输出目录必须为空。执行 `node scripts/publish.mjs --check-test bin/validation-release` 校验测试附件。测试私钥只在构建进程内存中存在；这些附件不得作为正式发行包。

正式发行使用受保护的 GitHub Environment `release`，配置以下材料后，才可由获得授权的 `vX.Y.Z` 标签触发：

| 配置 | 含义 |
| --- | --- |
| Environment Secret `TUNNELQUAY_SIGNING_KEY` | Base64 编码的 64 字节 Ed25519 私钥，采用 Go Ed25519 私钥格式（32 字节种子加 32 字节公钥） |
| Environment Variable `TUNNELQUAY_PUBLIC_KEY` | 对应的 Base64 编码 32 字节公钥，构建时注入服务端和客户端 |

密钥由维护者离线生成并保存。私钥缺失、格式无效或与公钥不匹配时构建失败。[标签发布工作流](../.github/workflows/release.yml) 先核对标签、日志、主分支归属和同版本尚未公开，再从源码构建前端、运行测试、生成全部归档并签名。Windows 使用 ZIP，Linux/macOS 使用 TAR.GZ；附件包括 `checksums.txt`、`release-manifest.json` 和 `release-manifest.sig`。上传完整草稿并核对附件后才公开；已公开版本不覆盖。预发布版本标记为 Prerelease，不进入默认稳定升级候选。

原生测试与交叉构建矩阵以 [CI 配置](../.github/workflows/ci.yml) 为准；已执行范围与外部环境待办见 [验收记录](verification.md)。推送、标签、正式签名配置与公开 Release 按 [仓库授权约定](../AGENTS.md#git-与发布) 执行。
