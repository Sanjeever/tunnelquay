# TunnelQuay

TunnelQuay 是面向个人自托管的 TCP、UDP 内网穿透工具。通过服务端 Web 面板统一管理多台设备和端口映射，客户端使用服务端地址、端口和接入凭据连接。

首版 `0.1.0` 已实现两个独立程序与中文管理界面，完成 Windows amd64 本地运行、浏览器交互和六个平台架构交叉构建验证。尚未推送代码或发布公开发行包；正式发行密钥与其他平台原生运行仍待配置、验证。详细范围见 [验收记录](docs/verification.md)。

## 从源码开始

使用 Go 1.26、Node 22.17 或符合 Vite 要求的更新版本，以及 pnpm 12.16.1。先构建前端，再构建两个程序。

```sh
pnpm --dir web install --frozen-lockfile
pnpm --dir web build
go build -o bin/ ./cmd/tunnelquay-server ./cmd/tunnelquay-client
```

Linux、macOS 启动服务端：

```sh
./bin/tunnelquay-server -listen :7000 -data-dir data/server
```

Windows 使用 `.\bin\tunnelquay-server.exe`，参数相同。首次启动会显示临时管理密码与客户端接入凭据。浏览器打开 `http://服务器地址:7000`，登录后修改临时密码。

在设置中保存客户端直连接入地址，例如 `203.0.113.10:7000`。在设备页复制接入命令，或手动运行：

```sh
./bin/tunnelquay-client -address 203.0.113.10 -port 7000 -credential <接入凭据>
```

Windows 使用 `.\bin\tunnelquay-client.exe`。默认通过 QUIC 接入，增加 `-transport tcp` 可选择 TCP。客户端身份保存在用户配置目录，使用 `-data-dir` 可指定独立目录。设备上线后即可在面板创建隧道。

源码开发构建显示版本 `dev`；发行工具从 `web/package.json` 读取版本并注入两个程序。正式构建和升级签名配置见 [开发与部署](docs/development.md)。

## 使用方式

在公网服务器以前台方式启动服务端，在 Linux、macOS 或 Windows 设备上启动客户端。客户端自动登记设备并保存身份。之后通过 Web 选择设备，创建、修改、启停和删除隧道。

所有隧道配置保存在服务端。修改映射不需要登录客户端设备编辑配置文件，设备重新连接后同步最新配置。

| 能力 | 已实现行为 |
| --- | --- |
| 隧道协议 | TCP、UDP、TCP + UDP |
| 目标地址 | 客户端可访问的本机或局域网 IP、域名与端口 |
| 公网端口 | 手动指定，或从设置范围内自动分配，分配后保持固定 |
| 客户端接入 | QUIC 或 TCP，可显式选择 |
| 设备观测 | 在线状态、版本、往返延迟、隧道和错误 |
| 连接记录 | 来源地址、起止时间、流量、结果，保留 7 天 |
| 客户端升级 | Web 发起，验证签名和替换程序后退出，重新上线确认实际版本；需要配置正式发行公钥 |

服务端以同一端口号的 TCP、UDP 提供接入，业务映射使用额外的公网端口。管理界面通过 HTTP 访问，域名和 HTTPS 由用户自行配置反向代理。客户端直接连接服务端 IP 和端口，接入链路独立加密并验证服务端身份。

客户端与服务端都以前台方式运行，用户自行配置常驻。升级完成后由外部守护或人工重启客户端，升级失败由人工恢复。

## 运行行为

- TCP + UDP 组合隧道整体启动，两种协议全部准备成功后运行。
- 配置更新成功后切换；准备失败时，原配置继续运行，面板显示期望配置、实际配置和失败原因。
- 修改目标时，已有连接继续使用原目标，新连接采用新目标。
- 停用或删除隧道立即停止接入并断开关联连接。
- 设备离线后保留配置和端口分配，重新上线自动恢复。已断开的业务连接由应用自行重连。
- 目标状态依据实际转发结果呈现。设备在线、监听就绪与目标服务可达分别表达。

## 技术与目录

后端采用 Go 与 SQLite，客户端和服务端分别构建。管理端采用 React、TypeScript、Vite、React Router、shadcn/ui、Tailwind CSS，按 Refero 参考设计。前端构建产物嵌入服务端二进制。

主要目录如下。

```text
cmd/
  tunnelquay-server/    服务端入口
  tunnelquay-client/    客户端入口
internal/
  server/              设备管理、隧道调度、管理 API
  client/              接入、转发、自更新
  protocol/            消息、版本与能力协商
  transport/           TCP、QUIC 与数据通道
  store/               SQLite、迁移与历史清理
  webui/               管理端资源嵌入
  buildinfo/           程序版本与构建信息
web/                   React 管理端
docs/                  设计、开发与版本日志
scripts/               版本及必要构建工具
.github/workflows/     持续集成与发布
```

Go Module 为 `github.com/Sanjeever/tunnelquay`。前端依赖通过 pnpm 管理，SQLite 使用 `modernc.org/sqlite`。

## 开发与发布

需求与运行契约见 [设计文档](docs/design.md)，操作见 [开发与部署](docs/development.md)，验证范围与缺口见 [验收记录](docs/verification.md)。[AGENTS.md](AGENTS.md) 保存开发约定，[handoff.md](handoff.md) 保留首版实施需求及验收目标。

文档、界面、CLI 帮助和提交描述使用简体中文，暂不实现 i18n。实现过程中按完整改动做本地小步提交。

程序版本统一来源于 `web/package.json`。每次发布先编写 `docs/releases/<version>.md` 中文更新日志，再更新版本，由 `vX.Y.Z` 标签触发 GitHub Actions。客户端、服务端与 Web 使用同一发布版本，通信协议与数据库结构分别版本化。

发布目标覆盖 Linux、macOS、Windows 的 amd64、arm64，分别提供客户端和服务端归档、校验文件及签名发行清单。协议通过版本和能力协商支持后续演进，兼容的旧客户端可继续运行已有功能。

本地检查使用 `go test ./...`、`go vet ./...` 和 `pnpm --dir web build`。真实浏览器验收、升级替换和交叉构建命令见开发文档。未配置正式发行密钥时，可以构建运行程序；远程升级验证会明确拒绝未建立信任的发行包。

仓库地址：[Sanjeever/tunnelquay](https://github.com/Sanjeever/tunnelquay)。
