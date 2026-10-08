# dbh

用 Go 编写的数据库 CLI，支持 SQLite、PostgreSQL、MySQL、MongoDB 和 ClickHouse。管理命名连接，执行 SQL 或 MongoDB 原生 JSON 命令，或进入支持实时补全与查询历史的交互终端。

## 安装

需要 Go 1.26 或更新版本；使用下列命令还需要 Make。

```bash
make build
# 或安装到 Go 的 bin 目录
make install
# 指定安装目录
GOBIN="$HOME/.local/bin" make install
```

`make build` 生成 `bin/dbh`；`make install` 安装到 `GOBIN`，未设置时使用 `GOPATH/bin`（通常为 `~/go/bin`）。将安装目录加入 `PATH` 后即可直接运行 `dbh`。不使用 Make 时，也可运行 `go build -o bin/dbh .` 或 `go install .`。

## 连接管理

```bash
dbh new local --driver sqlite --dsn ./demo.db
dbh new pg --driver postgres --prompt-dsn
# 输入：postgres://user:password@localhost:5432/app?sslmode=require

dbh new mysql --driver mysql --prompt-dsn
# 输入：user:password@tcp(localhost:3306)/app?parseTime=true

dbh new mongo --driver mongo --prompt-dsn
# 输入：mongodb://user:password@localhost:27017/app?authSource=admin

dbh new ch --driver clickhouse --prompt-dsn
# 输入：clickhouse://user:password@localhost:9000/app
# 也支持 HTTP：http://user:password@localhost:8123/app

dbh ls
dbh edit local --dsn ./another.db
dbh remove local
```

`new` 保存配置，不要求数据库在线。`edit` 仅更新传入的字段。`remove` 只删除连接配置，不删除数据库。连接名允许字母、数字、点、下划线、连字符，以字母或数字开头。`ls` 不显示 DSN。

命令简写：`list → ls`、`new → n`、`remove → rm / r`、`edit → e`、`connect → c`。简写与完整命令的参数及行为相同。

自动化可用 `--dsn-env ENV_NAME` 从环境变量读取 DSN。`--dsn`、`--dsn-env`、`--prompt-dsn` 三选一。密码建议通过隐藏输入或环境变量传入，避免出现在 shell 历史或进程参数中。

## 执行 SQL

```bash
dbh connect local --sql 'SELECT * FROM users;'
# exec: NAME 是已保存连接，--db 是服务器数据库名
dbh exec docker-pgsql --db dbh_test 'SELECT * FROM dbh_demo;'
dbh exec docker-mysql --db dbh_test --sql 'SELECT * FROM dbh_demo;' --format json
dbh exec local 'SELECT * FROM users;'
dbh connect local -f queries.sql
printf 'SELECT 1; SELECT 2;' | dbh connect local
dbh connect local --sql 'SELECT * FROM users' --format csv
dbh connect local --sql 'SELECT * FROM users' --format json
```

默认显示带边框和行分隔线的表格，单元格中的换行独立显示，长内容按终端宽度折行，中文和组合字符不会被拆开。列数过多时按记录显示字段/值表格，避免终端自动换行破坏行边界。CSV 包含列标题；JSON 每行一个对象（NDJSON）。不返回数据的 SQL 语句在表格模式下显示 `OK`，CSV/JSON 模式不输出状态文本。支持 NULL 和 `INSERT ... RETURNING`。每个 SQL 语句独立执行，批量模式遇错停止，以非零状态退出；需要原子性时显式使用 `BEGIN` / `COMMIT`。

`--timeout 30s` 控制连接和每条语句的超时，可修改。一个连接会话使用同一条底层连接，事务、临时表和会话设置可持续使用。

MongoDB 使用原生 JSON 命令文档，不执行 SQL 或 mongosh JavaScript。例如：

```bash
dbh c mongo --sql '{"insert":"users","documents":[{"name":"Alice"}]}' --format json
dbh c mongo --sql '{"find":"users","filter":{"name":"Alice"},"batchSize":100}' --format json
dbh c mongo --sql '{"aggregate":"users","pipeline":[{"$match":{"name":"Alice"}}],"cursor":{}}' --format json
```

MongoDB 沿用 `--sql` / `--file` / stdin 输入入口；多条命令用 `;` 分隔。JSON 输出保留完整文档、嵌套对象和 BSON 扩展 JSON 类型，并读取游标后续批次；表格/CSV 使用 `document` 列保存完整 JSON 文档。写入命令输出服务器响应，写入错误使批处理停止并以非零状态退出。

## 交互终端

```bash
dbh connect local
```

```text
local> CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT);
OK
local> INSERT INTO users (name) VALUES ('Alice');
OK
local> \refresh
local> SELECT *
...> FROM users;
┌────┬───────┐
│ id │ name  │
├────┼───────┤
│ 1  │ Alice │
└────┴───────┘
(1 rows)
local> \q
```

- 使用简洁彩色提示符，保留普通终端滚动记录；设置 `NO_COLOR=1` 可关闭提示符颜色。
- SQL 以 `;` 结束；未完成时按 Enter 继续换行，所有行仍可编辑。Alt-Enter 可直接插入换行。字符串、注释、PostgreSQL dollar quote 内的分号不会切分语句。
- 在支持括号粘贴的终端中，多行粘贴保留换行、缩进和中文，整块留在输入区，按 Enter 后才执行；LF、CRLF 和 CR 换行均支持。粘贴中有多条 SQL 时，确认后按顺序执行。
- 输入过程中自动显示 SQL 关键字、表名、列名和终端命令的候选，无需先按 Tab；候选按不区分大小写的包含匹配过滤，例如 `\describe pl` 可匹配 `platform`、`apple` 和 `sample`。Tab 选择候选，Shift-Tab 选择上一项。结构来自当前数据库，不调用大模型。修改表结构后执行 `\refresh` 更新候选项。补全为候选匹配，暂不解析别名、作用域或带空格的引用标识符。
- ↑/↓ 在多行输入中移动，到顶部/底部后回顾历史；Ctrl-R 搜索历史。匹配的历史 SQL 会作为灰色文字预览，在输入末尾按 → 接受。Ctrl-C 清除整块输入，在空输入区按 Ctrl-D 退出。
- `\database` 在 MySQL/PostgreSQL/MongoDB/ClickHouse 中列出数据库，每行一个名称，与 `\tables` 一样不受 `\format` 影响。MySQL 使用 `SHOW DATABASES`，PostgreSQL 查询 `pg_database`，MongoDB 列出服务器数据库；列表范围由数据库权限决定。SQLite 不支持此命令。
- `\use DATABASE` 或 `USE DATABASE;` 切换当前数据库，成功后刷新表/集合和字段提示，不修改保存的连接配置。MySQL 保留当前会话；PostgreSQL 重新建立连接，临时表、会话设置和未提交事务不保留，连接失败时仍保留旧连接；MongoDB 复用客户端并选择数据库，新库在写入数据后出现。SQLite 不支持切库。
- `\tables` 列出表和视图；`\describe TABLE` 显示每个字段的名称、类型、是否可空、默认值、主键顺序和注释，并附上索引列表；`\indexes TABLE` 单独查看索引。支持 `schema.table`，结果遵循当前 `\format`。
- 索引显示名称、唯一性、主键标记及索引字段/定义。MySQL 联合索引按字段顺序逐行显示，包含索引类型、前缀长度和排序方向。SQLite 没有原生字段注释，注释显示为 `NULL`；`INTEGER PRIMARY KEY` 使用 rowid 时没有独立索引，因此不会出现在索引列表中。CSV/JSON 下字段与索引结果顺序输出，不插入文本标题。
- `\history` 查看历史，`\clear` 清除待执行 SQL，`\format table|csv|json` 切换格式，`\help` 查看帮助。

MongoDB 交互输入使用 JSON 文档，完整文档按 Enter 即执行，也支持多行输入和 `;`。`\tables` 列出集合；字段提示采样集合的一条文档，`\describe COLLECTION` 显示样本文档与索引，`\indexes COLLECTION` 列出索引定义。

历史按连接名存储，保留多行 SQL。`dbh history NAME` 可在终端外查看。`--no-history` 禁用本次历史读取与记录。

当前分句器面向常规 SQL 和 PostgreSQL dollar quote 块；SQLite trigger、MySQL 存储过程等含内部未引用分号的复合语句以及 `DELIMITER` 客户端指令暂不支持。

## 配置与历史

默认使用系统用户配置目录中的 `dbh/`（Linux 通常是 `~/.config/dbh`），也可以设置 `DBH_CONFIG_DIR` 或使用全局 `--config-dir PATH`。

```text
connections.json          命名连接配置
history/<name>.jsonl       每个连接的 SQL 历史
```

目录权限为 `0700`，配置和历史文件为 `0600`（Unix）。配置包含明文 DSN，历史可能包含 SQL 中的敏感值。配置修改使用文件锁与原子替换。删除连接后保留历史，再次使用相同名称可复用历史。

## 开发与验证

```bash
make test
make e2e   # 使用 Go 构建真实 CLI 并运行端到端测试
make vet
make lint  # 需要已安装 golangci-lint
make fmt
make clean
```

`make` 或 `make help` 查看可用命令；`make clean` 仅删除本地构建的 `bin/dbh`。

e2e 测试由 Go `testing` 实现，通过 `os/exec` 启动真实 `dbh` 进程，覆盖连接管理与简写、环境变量配置、SQL 参数/文件/管道输入、输出格式、事务、错误退出和历史。Linux/macOS 上还通过 `creack/pty` 验证实时补全、Tab 选择、多行粘贴与提交前编辑、换行兼容、彩色提示符、Ctrl-C 清空、历史回填与搜索、多行 SQL 和 Ctrl-D 退出。每个测试使用自动清理的临时配置，默认仅使用临时 SQLite 文件，不需要外部数据库。`make e2e` 等同于 `go test -tags=e2e -count=1 -v ./tests`，测试会在临时目录构建当前源码，避免复用过期二进制；普通 `go test ./...` 不运行这套 e2e。

SQLite 测试使用真实数据库，覆盖 CLI 生命周期、SQL 查询和写入、事务回滚、返回结果、结构读取、补全、历史和文件权限。PostgreSQL/MySQL 使用各自的 Go 驱动，集成验证需要可访问的数据库。

可设置 `DBH_TEST_POSTGRES_DSN` / `DBH_TEST_MYSQL_DSN` 后运行 `go test ./...` 启用集成测试，或运行 `make e2e` 启用真实 CLI 的 PostgreSQL/MySQL 测试；设置 `DBH_TEST_MONGO_DSN` 启用 MongoDB e2e。未设置的外部数据库测试会明确跳过。e2e 覆盖 `new --dsn-env` 创建连接、完整命令与简写、建表/插入/查询、事务回滚、中文与 NULL、表格/CSV/JSON 输出、文件/管道输入、删除连接后数据保留，以及 Linux/macOS 上的实时提示、表结构、索引、数据库列表与切换。设置 `DBH_TEST_MYSQL_ALT_DATABASE` / `DBH_TEST_POSTGRES_ALT_DATABASE` 可进一步验证真实跨库切换，需同一账号能访问的另一个独立测试库。MongoDB 验证文档读写、嵌套字段、64 位整数、多批游标、集合与索引以及原生 JSON 交互。测试会创建并清理随机名称的测试表或集合，请使用测试数据库，DSN 和密码通过环境变量传入。

驱动及终端库：[pgx](https://github.com/jackc/pgx)、[go-sql-driver/mysql](https://github.com/go-sql-driver/mysql)、[modernc SQLite](https://github.com/modernc-org/sqlite)、[MongoDB Go Driver](https://github.com/mongodb/mongo-go-driver)、[ClickHouse Go Driver](https://github.com/ClickHouse/clickhouse-go)、[readline](https://github.com/reeflective/readline)。

`exec NAME [QUERY]` 支持位置参数语句、`--sql/-e`、`--file/-f` 或标准输入，执行完退出；支持 `--format`、`--timeout` 和 `--no-history`。`--db` 可省略，默认使用连接 DSN 中的数据库；指定时先切换服务器数据库，不修改已保存配置。MySQL、PostgreSQL、MongoDB 和 ClickHouse 支持切换，SQLite 不支持 `--db`。MongoDB 的 QUERY 使用原生 JSON 命令。

ClickHouse 使用官方 Go 驱动，支持原生 TCP（`clickhouse://`）和 HTTP/HTTPS DSN（HTTPS 需加 `?secure=true`）。`dbh exec ch --db app 'SELECT * FROM users;'` 可指定数据库。切库重新连接，原会话设置和临时表不保留，失败时保留当前连接。`\describe` 显示列类型、默认表达式、主键/排序键标记和注释；`\indexes` 显示主键表达式与数据跳过索引（ClickHouse 主键不是唯一约束）。ClickHouse 不支持这里用于关系型数据库的通用事务流程。

设置 `DBH_TEST_CLICKHOUSE_DSN` 启用 ClickHouse Go e2e（独立测试库）；可额外设置 `DBH_TEST_CLICKHOUSE_ALT_DATABASE` 验证跨库切换。测试会创建并清理带随机名称的表。

## Agent 技能

仓库提供 [dbh-cli](skills/dbh-cli/SKILL.md) 技能，指导 agent 选择连接、执行查询、导出结果和查看数据库结构，覆盖五种驱动。使用时让 agent 读取该技能，例如“使用 dbh-cli 查询连接 docker-pgsql 的 dbh_test 数据库”。
