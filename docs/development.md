# 开发与部署

版本来源为 `web/package.json`。当前开发环境为 Windows amd64、Go 1.25.4、Node 22.17.0、pnpm 12.16.1。依赖与实际工具链约束见锁文件及 `go.mod`。

## 开发

先执行 `pnpm --dir web install --frozen-lockfile`，再执行 `pnpm --dir web build`。前端输出到 `internal/webui/dist`，构建服务端前必须生成该目录；产物不提交。

执行 `go run ./cmd/tunnelquay-server -listen 127.0.0.1:7000` 查看管理端。开发前端可执行 `pnpm --dir web dev`，API 代理到本机 7000 端口。

执行 `go build ./cmd/tunnelquay-server` 和 `go build ./cmd/tunnelquay-client` 构建两个程序。客户端依赖不包含 SQLite 或前端。其余运行参数与验收结果随对应功能更新。
