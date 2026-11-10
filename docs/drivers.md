# 各数据库说明

SQLite、PostgreSQL、MySQL 使用标准 SQL，见 [执行语句与输出格式](exec.md)。下面是使用方式不同的驱动。

## MongoDB

MongoDB 使用原生 JSON 命令文档，不执行 SQL 或 mongosh JavaScript。例如：

```bash
dbh c mongo --sql '{"insert":"users","documents":[{"name":"Alice"}]}' --format json
dbh c mongo --sql '{"find":"users","filter":{"name":"Alice"},"batchSize":100}' --format json
dbh c mongo --sql '{"aggregate":"users","pipeline":[{"$match":{"name":"Alice"}}],"cursor":{}}' --format json
```

MongoDB 沿用 `--sql` / `--file` / stdin 输入入口；多条命令用 `;` 分隔。JSON 输出保留完整文档、嵌套对象和 BSON 扩展 JSON 类型，并读取游标后续批次；表格/CSV 使用 `document` 列保存完整 JSON 文档。写入命令输出服务器响应，写入错误使批处理停止并以非零状态退出。

MongoDB 交互输入使用 JSON 文档，完整文档按 Enter 即执行，也支持多行输入和 `;`。`\tables` 列出集合；字段提示采样集合的一条文档，`\describe COLLECTION` 显示样本文档与索引，`\indexes COLLECTION` 列出索引定义。

## Redis

Redis 使用 redis-cli 风格命令，每行一条，不需要分号：

```bash
dbh exec cache 'SET greeting "hello dbh"'
dbh exec cache --db 1 'HGETALL user:1' --format json
printf 'RPUSH queue a b\nLRANGE queue 0 -1\n' | dbh c cache
```

参数按 redis-cli 规则解析：双引号支持 `\n`、`\t`、`\xHH` 等转义，单引号只转义 `\'`。字符串、整数和 nil 输出为单行 `value` 列（nil 显示为 `NULL`）；数组每个元素一行；哈希、`CONFIG GET` 等映射结果输出 `field`/`value` 两列；嵌套数组（如 `XRANGE`）在表格/CSV 中显示为 JSON 文本，JSON 输出保留嵌套结构。会话固定使用一条连接，`SELECT`、`MULTI`/`EXEC` 在同一会话中持续有效；连接中断不会自动重试命令，避免重复写入。仅支持单机部署，暂不支持 Cluster 和 Sentinel。

Redis 交互输入按 Enter 执行当前行，引号未闭合时继续换行。`\database` 列出有 key 的数据库编号和当前库，`\use N` 重新连接到第 N 号库；`\tables` 用 `SCAN` 列出当前库最多 1000 个 key；`\describe KEY` 显示类型、TTL（秒）、编码和长度，Redis 没有索引，`\indexes` 会提示不支持。候选包含常用命令名和扫描到的 key。

## ClickHouse

ClickHouse 使用官方 Go 驱动，支持原生 TCP（`clickhouse://`）和 HTTP/HTTPS DSN（HTTPS 需加 `?secure=true`）。`dbh exec ch --db app 'SELECT * FROM users;'` 可指定数据库。切库重新连接，原会话设置和临时表不保留，失败时保留当前连接。`\describe` 显示列类型、默认表达式、主键/排序键标记和注释；`\indexes` 显示主键表达式与数据跳过索引（ClickHouse 主键不是唯一约束）。ClickHouse 不支持这里用于关系型数据库的通用事务流程。
