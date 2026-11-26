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

## SQL Server（`mssql`）

SQL Server 使用纯 Go 驱动 `go-mssqldb`，支持 `[schema].[table]` 方括号标识符与 `--proxy` 代理链。`\use` 重新连接到目标数据库；`\describe` 显示列类型、可空性、默认值与主键序号，`\indexes` 显示索引类型、唯一性与列清单。

## Oracle（`oracle`）

Oracle 使用纯 Go 驱动 `go-ora/v2`，无需 Instant Client / CGO，支持 `--proxy` 代理链。`\database` 列出 `ALL_USERS`；`\use` 在当前会话执行 `ALTER SESSION SET CURRENT_SCHEMA` 切换 Schema；`\tables`、`\describe` 与 `\indexes` 读取当前 Schema 下的表/视图、列注释与索引。

## Trino / Presto（`trino`）

Trino 使用官方 `trino-go-client`，支持 `http://`、`https://` 与 `trino://` DSN 及 `--proxy` 代理链。`\use` 支持 `schema` 或 `catalog.schema`（重新连接）；`\describe` 执行 `DESCRIBE` 查看列信息；Trino 无索引概念，`\indexes` 提示不支持。

## 达梦（`dm`）

达梦使用纯 Go 驱动 `gitee.com/chunanyong/dm`（DSN 形如 `dm://SYSDBA:password@localhost:5236`），暂不支持 `--proxy` 代理链。`\database` 列出 Schema，`\use` 在当前会话执行 `SET SCHEMA` 切换 Schema；`\tables`、`\describe` 与 `\indexes` 读取当前 Schema 下的表/视图、列注释与索引。

## Elasticsearch（`elasticsearch`）

Elasticsearch 使用 HTTP REST + JSON 命令（`GET|POST|PUT|DELETE|HEAD|PATCH <path> [JSON body]`），支持 Basic Auth、`?api_key=...`、`?insecure=true` 与 `--proxy` 代理链。相对路径会自动拼接当前默认索引（可用 `--db` 或 `\use INDEX` 切换）。`\tables` / `\database` 列出索引，`\describe INDEX` 展开 `_mapping` 字段与类型；搜索响应（`hits.hits`）和 `_sql` 响应可直接以表格/CSV/JSON/TSV 输出。

## Milvus（`milvus`）

Milvus 使用 RESTful API v2 + JSON 命令（`GET|POST|PUT|DELETE|PATCH <path> [JSON body]`），支持 `user:password`、`?token=...` Bearer 认证、`?insecure=true` 与 `--proxy` 代理链。不以 `/` 开头的相对路径会自动补全 `/v2/vectordb/` 前缀，并在请求体缺少 `dbName` 时注入当前数据库（默认 `default`，可用 `--db` 或 `\use DB` 切换）。`\database` 列出数据库，`\tables` 列出 Collection，`\describe COLLECTION` 展开字段定义，`\indexes COLLECTION` 列出向量/标量索引。

## Qdrant（`qdrant`）

Qdrant 使用 HTTP REST + JSON 命令（`GET|POST|PUT|DELETE|PATCH <path> [JSON body]`），支持 `?api_key=...`、`?insecure=true` 与 `--proxy` 代理链。相对路径会自动拼接当前默认 Collection（可用 `--db` 或 `\use COLLECTION` 切换）。`\tables` / `\database` 列出 Collection，`\describe COLLECTION` 与 `\indexes COLLECTION` 展示向量维度、距离度量与 payload 索引。

## Neo4j（`neo4j`）

Neo4j 通过 HTTP Transactional Cypher API（`/db/{database}/tx/commit`）执行 Cypher 语句，支持 Basic Auth、`?insecure=true` 与 `--proxy` 代理链。`\database` 列出数据库，`\use DB` 切换目标数据库，`\tables` 列出节点标签，`\describe LABEL` 采样节点属性键，`\indexes` 列出索引。

## InfluxDB（`influxdb`）

InfluxDB v2 使用 HTTP REST 执行 Flux 查询（默认）或 `WRITE <line-protocol>` 写入，支持 `?org=...&token=...&insecure=true` 与 `--proxy` 代理链。`\database` 列出 Bucket，`\use BUCKET` 切换默认 Bucket，`\tables` 与 `\describe` 列出 Measurement 及 Field。

## TDengine（`tdengine`）

TDengine 通过 RESTful `/rest/sql` 执行 SQL，支持 Basic Auth、`?insecure=true` 与 `--proxy` 代理链。`\database` 列出数据库，`\use DB` 切换默认数据库，`\tables` 列出超级表与普通表，`\describe` 查看表结构。

## Cassandra / ScyllaDB（`cassandra`）

Cassandra 使用纯 Go `gocql` 驱动执行 CQL，支持 `?consistency=...&timeout=...` 与 `--proxy` 代理链。`\database` 列出 Keyspace，`\use KEYSPACE` 切换 Keyspace，`\tables`、`\describe` 与 `\indexes` 读取 `system_schema`。

## etcd（`etcd`）

etcd 通过 v3 HTTP/JSON Gateway 执行键值命令（`GET`、`PUT`、`DEL`、`PREFIX`、`KEYS`、`STATUS`、`COMPACT`、`POST <path> [json]`），每行一条命令，支持 `user:pass` 认证、`?tls=true` 与 `--proxy` 代理链。`\tables` 列出最多 200 个 key，`\describe KEY` 查询指定 key。

## bbolt（`bbolt`）

bbolt 使用纯 Go `go.etcd.io/bbolt` 打开本地嵌入式 KV 文件（`bbolt:///path/to/file.db?readonly=true`），不支持 `--proxy`。支持 `BUCKETS`、`KEYS <bucket> [prefix]`、`GET <bucket> <key>`、`PUT <bucket> <key> <value>`、`DEL <bucket> <key>`、`CREATE BUCKET`、`DELETE BUCKET` 与 `STATS`，每行一条命令。`\tables` 列出所有顶层 Bucket，`\describe BUCKET` 列出该 Bucket 下的键。

## OpenObserve（`openobserve`）

OpenObserve 通过 HTTP REST API 执行 SQL 查询（`POST /api/{org}/_search`）、`SHOW STREAMS`、`SHOW SCHEMA <stream>` 与 `INGEST <stream> <json>` 写入，支持 Basic Auth、`?token=...&stream_type=logs|metrics|traces&tls=true` 与 `--proxy` 代理链。`\database` 列出组织，`\use ORG` 切换默认组织，`\tables` 列出流（Stream），`\describe STREAM` 查看流字段 Schema。
