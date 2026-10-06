# dbh

用 Go 编写的数据库 CLI，支持 SQLite、PostgreSQL 和 MySQL。管理命名连接，直接执行 SQL，或进入支持 Tab 补全与 SQL 历史的交互终端。

## 安装

需要 Go 1.26 或更新版本。

```bash
go build -o bin/dbh .
# 或安装到 Go 的 bin 目录
go install .
```

## 连接管理

```bash
dbh new local --driver sqlite --dsn ./demo.db
dbh new pg --driver postgres --prompt-dsn
# 输入：postgres://user:password@localhost:5432/app?sslmode=require

dbh new mysql --driver mysql --prompt-dsn
# 输入：user:password@tcp(localhost:3306)/app?parseTime=true

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
dbh connect local -f queries.sql
printf 'SELECT 1; SELECT 2;' | dbh connect local
dbh connect local --sql 'SELECT * FROM users' --format csv
dbh connect local --sql 'SELECT * FROM users' --format json
```

默认显示表格。CSV 包含列标题；JSON 每行一个对象（NDJSON）。不返回数据的语句在表格模式下显示 `OK`，CSV/JSON 模式不输出状态文本。支持 NULL 和 `INSERT ... RETURNING`。每个 SQL 语句独立执行，批量模式遇错停止，以非零状态退出；需要原子性时显式使用 `BEGIN` / `COMMIT`。

`--timeout 30s` 控制连接和每条语句的超时，可修改。一个连接会话使用同一条底层连接，事务、临时表和会话设置可持续使用。

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
id  name
1   Alice
(1 rows)
local> \q
```

- SQL 以 `;` 结束，支持多行输入；字符串、注释、PostgreSQL dollar quote 内的分号不会切分语句。
- Tab 补全 SQL 关键字、表名、列名和终端命令；结构来自当前数据库，不调用大模型。修改表结构后执行 `\refresh` 更新候选项。补全为候选匹配，暂不解析别名、作用域或带空格的引用标识符。
- ↑/↓ 回顾历史，Ctrl-R 搜索历史。Ctrl-C 清除当前输入，Ctrl-D 退出。
- `\tables` 列出表和视图；`\describe TABLE` 列出列名。
- `\history` 查看历史，`\clear` 清除待执行 SQL，`\format table|csv|json` 切换格式，`\help` 查看帮助。

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
go test ./...
go vet ./...
```

SQLite 测试使用真实数据库，覆盖 CLI 生命周期、SQL 查询和写入、事务回滚、返回结果、结构读取、补全、历史和文件权限。PostgreSQL/MySQL 使用各自的 Go 驱动，集成验证需要可访问的数据库。

可设置 `DBH_TEST_POSTGRES_DSN` / `DBH_TEST_MYSQL_DSN` 后运行 `go test ./...` 启用集成测试。测试会创建并清理独立测试表，请使用测试数据库。

驱动及终端库：[pgx](https://github.com/jackc/pgx)、[go-sql-driver/mysql](https://github.com/go-sql-driver/mysql)、[modernc SQLite](https://github.com/modernc-org/sqlite)、[readline](https://github.com/chzyer/readline)。
