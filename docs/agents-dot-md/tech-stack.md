# 技术栈与验证
> Go CLI 的技术选型、数据访问约定和验证命令；写代码前阅读。

## 技术选型

语言要求和固定依赖版本以 `go.mod` 为准。命令框架为 Cobra，终端编辑器为 reeflective/readline，数据库访问统一使用 `database/sql`。SQLite 使用 modernc 驱动，PostgreSQL 使用 pgx，MySQL 使用 go-sql-driver/mysql。

连接参数采用驱动原生 DSN，通过现有 `Store` 管理命名连接。补全和历史不依赖大模型或外部服务。

## 验证命令

```bash
gofmt -w main.go internal/dbh
go test ./...
go vet ./...
go build -o bin/dbh .
```

安装了 `golangci-lint` 时运行 `golangci-lint run`；并发相关改动补跑 `go test -race ./...`。

SQLite 测试无需外部数据库。设置 `DBH_TEST_POSTGRES_DSN` 或 `DBH_TEST_MYSQL_DSN` 后启用对应集成测试；只连接独立测试库，测试会创建与清理表。

终端行为使用真实 TTY 或 PTY 验证：输入字符即显示候选、Tab 选择、Ctrl-C 清空、Ctrl-R 搜索、历史回填、多行 SQL。静态候选匹配测试不能替代终端验证。验证后更新 `bin/dbh`，产物不入库。
