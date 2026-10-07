# 技术栈与验证
> Go CLI 的技术选型、数据访问约定和验证命令；写代码前阅读。

## 技术选型

语言要求和固定依赖版本以 `go.mod` 为准。命令框架为 Cobra，终端编辑器为 reeflective/readline。关系型数据库沿用 `database/sql`：SQLite 使用 modernc 驱动，PostgreSQL 使用 pgx，MySQL 使用 go-sql-driver/mysql，ClickHouse 使用官方 clickhouse-go/v2 驱动；MongoDB 使用官方 Go Driver v2 执行原生 JSON 命令。各实现通过 `internal/database` 的驱动与会话接口共用 CLI、输出和配置体系。

readline 暂通过 `go.mod` 的 `replace` 使用 `third_party/readline`，修复终端光标回报阻塞键盘读取的问题；来源与补丁范围见 [补丁说明](../../third_party/readline/DBH_PATCH.md)。更新依赖时必须保留此修复或确认上游已修复，并运行 PTY 回归测试。

连接参数采用驱动原生 DSN，通过现有 `Store` 管理命名连接。补全和历史不依赖大模型或外部服务。

## 验证命令

```bash
gofmt -w main.go internal/dbh
go test ./...
go vet ./...
go build -o bin/dbh .
make e2e
```

安装了 `golangci-lint` 时运行 `golangci-lint run`；并发相关改动补跑 `go test -race ./...`。

SQLite 测试无需外部数据库。设置 `DBH_TEST_POSTGRES_DSN` 或 `DBH_TEST_MYSQL_DSN` 后启用对应集成测试；只连接独立测试库，测试会创建与清理表。Go e2e 还支持 `DBH_TEST_MONGO_DSN`、`DBH_TEST_CLICKHOUSE_DSN`，只在独立测试库创建并清理集合/表；设置 `DBH_TEST_MYSQL_ALT_DATABASE` / `DBH_TEST_POSTGRES_ALT_DATABASE` 验证跨库切换；ClickHouse 对应 `DBH_TEST_CLICKHOUSE_ALT_DATABASE`。

Go e2e 位于 `tests/`，使用 `e2e` build tag，通过 `os/exec` 启动临时构建的真实 CLI；Linux/macOS 的终端用例使用 `creack/pty`。运行 `make e2e` 或 `go test -tags=e2e -count=1 -v ./tests`，无需 Python；外部数据库沿用上述 DSN 环境变量，未配置时明确跳过。

终端行为使用真实 TTY 或 PTY 验证：输入字符即显示候选、Tab 选择、Ctrl-C 清空、Ctrl-R 搜索、历史回填、多行 SQL。静态候选匹配测试不能替代终端验证。验证后更新 `bin/dbh`，产物不入库。
