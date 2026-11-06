# 开发与部署

版本来源为 `web/package.json`。开发宿主为 Windows amd64、Node 22.17.0、pnpm 12.16.1。Go 依赖要求 1.26，Go 会按 `go.mod` 获取项目工具链。

## 开发

先执行 `pnpm --dir web install --frozen-lockfile`，再执行 `pnpm --dir web build`。前端输出到 `internal/webui/dist`，构建服务端前必须生成该目录；产物不提交。

执行 `go run ./cmd/tunnelquay-server -listen 127.0.0.1:7000` 查看管理端。开发前端可执行 `pnpm --dir web dev`，API 代理到本机 7000 端口。

执行 `go build -o bin/ ./cmd/tunnelquay-server ./cmd/tunnelquay-client` 构建两个程序，输出到 `bin`。客户端依赖不包含 SQLite 或前端。

## 前台运行

服务端执行 `tunnelquay-server -listen :7000 -data-dir data/server`。首次启动在终端显示临时管理密码和接入凭据，首次登录必须改密。管理密码以 bcrypt 哈希保存。服务端同时监听 TCP、UDP 7000；开放业务映射端口由用户自行配置防火墙。

客户端执行 `tunnelquay-client -address 203.0.113.10 -port 7000 -credential <接入凭据>`，默认 QUIC。增加 `-transport tcp` 显式选择 TCP。客户端身份默认保存在用户配置目录下 `tunnelquay/device-id`，可用 `-data-dir` 指定。多实例使用不同身份目录。进程收到中断或 SIGTERM 后清理退出。

Web 反向代理转发 HTTP 到 7000，保留 Host，并在 HTTPS 入口设置 `X-Forwarded-Proto: https`。客户端直连公网 IP 或域名和接入端口，不经过 HTTP 反向代理。在设置中单独保存接入地址，生成命令不使用浏览器域名推断。

备份时先停止服务端，再复制完整数据目录（包含 SQLite 和 `identity.json`）；身份文件丢失会使现有凭据失效。恢复时使用同一目录。客户端升级和重启保留其身份目录。

## 行为验证

`go test ./...` 使用临时数据库与本地 TCP/UDP 服务，覆盖两种接入、TCP 双向传输与半关闭、三个 UDP 来源及 65507 字节数据报、配置切换与失败保留、重启恢复、停用、认证拒绝、改密和历史清理。`go vet ./...` 检查通过。当前运行验证平台为 Windows amd64；其他系统和架构尚未完成原生运行验证。

浏览器检查先构建两个程序到 `bin/`（Windows 使用 `.exe`），执行 `pnpm --dir web exec playwright install chromium`，再执行 `pnpm --dir web exec playwright test`。测试自行启动临时服务端、QUIC 客户端和 TCP 目标，检查管理操作与真实转发。截图与失败跟踪位于 `web/test-results`，不提交。测试结束删除隔离数据。

## 客户端升级

在设备页打开“客户端升级”，选择设备。展开“发行包”可查询官方版本并下载，或导入对应的 `release-manifest.json`、`release-manifest.sig` 与客户端归档。缓存通过签名验证后才成为升级候选。选择版本后发起任务。

升级成功后客户端退出码为 0。Linux 外部守护应采用 `Restart=always`，避免 `Restart=on-failure` 跳过正常退出后的重启。前台运行需手动再次执行原接入命令。Windows 可使用用户已有守护工具，macOS 可使用用户配置的 launchd；项目不安装系统服务。新版启动失败需人工替换程序，保留身份目录。

正式发行公钥通过构建变量 `github.com/Sanjeever/tunnelquay/internal/update.PublicKey` 注入，值为 Base64 编码的 32 字节 Ed25519 公钥。当前未配置正式签名材料，普通开发构建会拒绝远程升级验证。`go test ./...` 使用隔离临时密钥构建两个测试版本，完成实际替换与重启验证，不修改正式信任配置。Windows 运行中的旧文件可能保留为隐藏的 `.tunnelquay-client.exe.old`，确认新版可用且旧进程退出后可人工清理。

Windows 测试还会在临时目录设置并恢复写入拒绝 ACL，验证无写权限时原进程继续运行。另以截断和损坏的缓存包验证下载中断与 SHA-256 失败路径。测试密钥与受限目录均限于测试临时文件。

## 版本与发行

`node scripts/version.mjs print` 读取唯一版本来源。`node scripts/version.mjs check` 校验版本及中文日志，附加标签参数如 `v0.1.0` 时校验一致性。升级版本前先编写对应 `docs/releases/<version>.md`，然后执行 `node scripts/version.mjs bump patch`，也可指定 `minor`、`major` 或完整 SemVer。缺少日志时不修改版本文件。

完成前端构建后执行 `go run ./scripts/release -test-key -output bin/validation-0.1.0`，交叉构建六个平台架构的两个组件，并输出十二个归档、清单、签名、校验文件与测试标记。输出目录必须为空。执行 `node scripts/publish.mjs --check-test bin/validation-0.1.0` 校验测试附件。测试私钥只在构建进程内存中存在，测试公钥和标记随本地附件输出；这些附件不得作为正式发行包。

正式发行使用受保护的 GitHub Environment `release`，配置以下材料后，才可由获得授权的 `vX.Y.Z` 标签触发：

| 配置 | 含义 |
| --- | --- |
| Environment Secret `TUNNELQUAY_SIGNING_KEY` | Base64 编码的 64 字节 Ed25519 私钥，采用 Go Ed25519 私钥格式（32 字节种子加 32 字节公钥） |
| Environment Variable `TUNNELQUAY_PUBLIC_KEY` | 对应的 Base64 编码 32 字节公钥，构建时注入服务端和客户端 |

密钥由维护者离线生成并保存，当前任务没有生成或配置正式密钥。私钥缺失、格式无效或与公钥不匹配时构建失败。发布任务先核对标签、日志、主分支归属和同版本尚未公开，再从源码构建前端、运行测试、生成全部归档并签名。上传完整草稿并核对附件后才公开；已公开版本不覆盖。预发布版本标记为 Prerelease，不进入默认稳定升级候选。

CI 配置原生 Windows amd64、Linux amd64 和 macOS Intel 验证，以及 Linux 竞争检测和六个平台架构交叉构建。工作流文件写入仓库不代表这些远端验证已经运行。当前测试环境没有可用的 CGO 编译器，WSL 虚拟机服务无法启动；本机竞争检测和 Linux 原生验证尚未执行。macOS 代码签名、Gatekeeper 行为及各 arm64 原生运行也尚待对应环境验证。
