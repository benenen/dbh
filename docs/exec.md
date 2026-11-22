# 执行语句与输出格式

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
dbh exec local 'SELECT * FROM users' --format tsv   # 紧凑格式，适合 AI agent 读取
```

默认显示带边框和行分隔线的表格，单元格中的换行独立显示，长内容按终端宽度折行，中文和组合字符不会被拆开。列数过多时按记录显示字段/值表格，避免终端自动换行破坏行边界。CSV 包含列标题，NULL 输出为 `NULL`；JSON 每行一个对象（NDJSON），字段按结果列顺序排列，重名列依次加 `_2`、`_3` 后缀，避免 JOIN 结果丢失字段。TSV 为 agent 等程序读取设计：首行列名，每条记录一行，制表符分隔，值中的制表符、换行、回车和反斜杠转义为 `\t`、`\n`、`\r`、`\\`，NULL 输出为 `\N`、空字符串留空；没有边框、行分隔线和行数统计，体积约为表格输出的 1/7、JSON 的一半。不返回数据的 SQL 语句在表格模式下显示 `OK`，CSV/JSON/TSV 模式不输出状态文本。支持 NULL 和 `INSERT ... RETURNING`。每个 SQL 语句独立执行，批量模式遇错停止，以非零状态退出；需要原子性时显式使用 `BEGIN` / `COMMIT`。

表格默认每 500 行渲染一批，读完一批即输出；每批重复表头，列宽按该批内容计算，末尾显示总输出行数。`--table-batch-size N` 调整批大小。`--max-rows N` 限制每条语句的结果行数（默认 `0` 不限）；确有剩余行时，stderr 显示截断提示，适用于所有格式。行数上限只限制读取和显示，不限制数据库实际执行的写操作。

脚本添加 `--stats` 后，stderr 显示每条语句的耗时、查询输出行数，以及 SQLite/PostgreSQL/MySQL 普通 `INSERT`、`UPDATE`、`DELETE` 等写语句的影响行数；包含返回结果的写语句显示返回行数。影响行数取自原次执行，遵循驱动语义。ClickHouse 异步写入及 MongoDB/Redis 命令不提供统一的影响行数，查看命令响应；耗时包含结果读取和输出。默认脚本结果不附带统计。

`--timeout 30s` 控制连接和每条语句的超时，可修改。一个连接会话使用同一条底层连接，事务、临时表和会话设置可持续使用。

## exec 命令

`exec NAME [QUERY]`（简写 `x`）支持位置参数语句、`--sql/-e`、`--file/-f` 或标准输入，执行完退出；支持 `--format`、`-o/--output`（全部语句成功后原子写入文件）、`--timeout`、`--no-history`、`--param`、`--stats`、`--max-rows` 和 `--table-batch-size`。`--db` 可省略（`connect` 同样支持 `--db`），默认使用连接 DSN 中的数据库；指定时先切换服务器数据库，不修改已保存配置。MySQL、PostgreSQL、MongoDB、ClickHouse 和 Redis（数据库编号）支持切换，SQLite 不支持 `--db`。MongoDB 的 QUERY 使用原生 JSON 命令。

## 参数绑定

`exec` 的 `--param` 按顺序绑定 JSON 标量，可重复使用。支持字符串、数值、布尔值和 `null`；不接受数组或对象。保留数据库原生占位符，MySQL/SQLite 常用 `?`，PostgreSQL 用 `$1`、`$2`，ClickHouse 按驱动使用 `?`。绑定只允许一条 SQL，MongoDB/Redis 使用各自原生命令文档或参数。

```bash
dbh exec local 'SELECT * FROM users WHERE id = ?' --param 42 --format tsv
dbh exec pg 'SELECT * FROM users WHERE name = $1 AND active = $2' \
  --param '"Alice"' --param true --format tsv
dbh exec local 'INSERT INTO users(name) VALUES (?)' --param '"O’Connor"' --stats
```

绑定值不会拼入 SQL 或写入 dbh 历史；占位符 SQL 仍按历史开关记录。敏感绑定值也应避免写入系统 shell 历史或共享进程命令行。
