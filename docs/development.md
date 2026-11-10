# 开发与验证

```bash
make test
make e2e   # 使用 Go 构建真实 CLI 并运行端到端测试
make vet
make lint  # 需要已安装 golangci-lint
make fmt
make clean
```

`make` 或 `make help` 查看可用命令；`make clean` 仅删除本地构建的 `bin/dbh`。

e2e 测试由 Go `testing` 实现，通过 `os/exec` 启动真实 `dbh` 进程，覆盖连接管理与简写、环境变量配置、SQL 参数/文件/管道输入、输出格式、事务、错误退出和历史。Linux/macOS 上还通过 `creack/pty` 验证实时补全、Tab/方向键选择候选与 Enter 确认、多行粘贴与提交前编辑、换行兼容、彩色提示符、Ctrl-C 清空、历史回填与搜索、多行 SQL 和 Ctrl-D 退出。每个测试使用自动清理的临时配置，默认仅使用临时 SQLite 文件，不需要外部数据库。`make e2e` 等同于 `go test -tags=e2e -count=1 -v ./tests`，测试会在临时目录构建当前源码，避免复用过期二进制；普通 `go test ./...` 不运行这套 e2e。

SQLite 测试使用真实数据库，覆盖 CLI 生命周期、SQL 查询和写入、事务回滚、返回结果、结构读取、补全、历史和文件权限。PostgreSQL/MySQL 使用各自的 Go 驱动，集成验证需要可访问的数据库。

可设置 `DBH_TEST_POSTGRES_DSN` / `DBH_TEST_MYSQL_DSN` 后运行 `go test ./...` 启用集成测试，或运行 `make e2e` 启用真实 CLI 的 PostgreSQL/MySQL 测试；设置 `DBH_TEST_MONGO_DSN` 启用 MongoDB e2e。未设置的外部数据库测试会明确跳过。e2e 覆盖 `new --dsn-env` 创建连接、完整命令与简写、建表/插入/查询、事务回滚、中文与 NULL、表格/CSV/JSON 输出、文件/管道输入、删除连接后数据保留，以及 Linux/macOS 上的实时提示、表结构、索引、数据库列表与切换。设置 `DBH_TEST_MYSQL_ALT_DATABASE` / `DBH_TEST_POSTGRES_ALT_DATABASE` 可进一步验证真实跨库切换，需同一账号能访问的另一个独立测试库。MongoDB 验证文档读写、嵌套字段、64 位整数、多批游标、集合与索引以及原生 JSON 交互。测试会创建并清理随机名称的测试表或集合，请使用测试数据库，DSN 和密码通过环境变量传入。

设置 `DBH_TEST_REDIS_DSN`（如 `redis://127.0.0.1:6379/0`，使用独立测试实例）启用 Redis e2e，覆盖批量命令、引号解析、输出格式、错误退出、切库与交互终端；测试只创建并删除带随机前缀的 key。

设置 `DBH_TEST_CLICKHOUSE_DSN` 启用 ClickHouse Go e2e（独立测试库）；可额外设置 `DBH_TEST_CLICKHOUSE_ALT_DATABASE` 验证跨库切换。测试会创建并清理带随机名称的表。

驱动及终端库：[pgx](https://github.com/jackc/pgx)、[go-sql-driver/mysql](https://github.com/go-sql-driver/mysql)、[modernc SQLite](https://github.com/modernc-org/sqlite)、[MongoDB Go Driver](https://github.com/mongodb/mongo-go-driver)、[ClickHouse Go Driver](https://github.com/ClickHouse/clickhouse-go)、[go-redis](https://github.com/redis/go-redis)、[readline](https://github.com/reeflective/readline)。
