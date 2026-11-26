# 连接管理

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

dbh new cache --driver redis --prompt-dsn
# 输入：redis://user:password@localhost:6379/0（TLS 使用 rediss://）

dbh new mssql --driver mssql --prompt-dsn
# 输入：sqlserver://user:password@localhost:1433?database=app

dbh new ora --driver oracle --prompt-dsn
# 输入：oracle://user:password@localhost:1521/ORCLPDB1

dbh new trino --driver trino --prompt-dsn
# 输入：http://user@localhost:8080?catalog=memory&schema=default

dbh new dm --driver dm --prompt-dsn
# 输入：dm://SYSDBA:password@localhost:5236

dbh new es --driver elasticsearch --prompt-dsn
# 输入：http://user:password@localhost:9200（可选 ?api_key=...&insecure=true）

dbh new milvus --driver milvus --prompt-dsn
# 输入：http://root:Milvus@localhost:19530/default（可选 ?token=...&insecure=true）

dbh new qdrant --driver qdrant --prompt-dsn
# 输入：http://localhost:6333/docs（可选 ?api_key=...&insecure=true）

dbh new neo4j --driver neo4j --prompt-dsn
# 输入：http://neo4j:password@localhost:7474/neo4j（可选 ?insecure=true）

dbh new influxdb --driver influxdb --prompt-dsn
# 输入：http://localhost:8086/metrics?org=acme&token=YOUR_TOKEN

dbh new tdengine --driver tdengine --prompt-dsn
# 输入：http://root:taosdata@localhost:6041/power

dbh new cassandra --driver cassandra --prompt-dsn
# 输入：cassandra://localhost:9042/app

dbh ls
dbh ls --driver postgres --format tsv
dbh edit local --dsn ./another.db
dbh remove local
dbh remove local --keep-history
```

`new` 保存配置，不要求数据库在线。`edit` 仅更新传入的字段。`remove` 删除连接配置以及对应的 SQL 历史文件（加 `--keep-history` 可保留历史），不删除数据库。连接名允许字母、数字、点、下划线、连字符，以字母或数字开头。`ls` 不显示 DSN，支持 `--driver` 按驱动过滤和 `--format table|csv|json|tsv`。

命令简写：`list → ls`、`new → n`、`remove → rm / r`、`edit → e`、`connect → c`、`exec → x`。简写与完整命令的参数及行为相同。

自动化可用 `--dsn-env ENV_NAME` 从环境变量读取 DSN。`--dsn`、`--dsn-env`、`--prompt-dsn` 三选一。密码建议通过隐藏输入或环境变量传入，避免出现在 shell 历史或进程参数中。

## 代理

每个连接可保存自己的代理列表。`--proxy` 可重复，按从本机出发的顺序逐跳连接，最后一跳连接 DSN 中的数据库地址：

```bash
# 经 SSH 跳板访问内网 PostgreSQL
dbh new pg --driver postgres --prompt-dsn --proxy ssh://ops@jump.example.com
# 先经 SOCKS5，再经 SSH 跳板
dbh new ch --driver clickhouse --prompt-dsn \
  --proxy socks5://127.0.0.1:1080 --proxy 'ssh://ops@10.0.0.5:2222?identity=~/.ssh/ops_key'
dbh edit pg --proxy socks5://user:pass@proxy:1080   # 替换整个列表
dbh edit pg --no-proxy                              # 改为直连
```

- `socks5://[user:pass@]host:port`：主机名交给代理解析（等同 `socks5h`）。
- `ssh://[user[:pass]@]host[:port]`：端口默认 22，用户默认当前用户。认证依次尝试 URL 中的密码、`identity=FILE`（可重复）、`SSH_AUTH_SOCK` 代理和 `~/.ssh/id_ed25519|id_ecdsa|id_rsa`（有口令的密钥请放进 ssh-agent）。主机密钥按 `~/.ssh/known_hosts` 严格校验，可用 `known_hosts=FILE` 指定，未知主机会拒绝连接。
- 使用代理时，DSN 中的主机名由最后一跳解析，可直接写内网主机名。PostgreSQL、MySQL、SQL Server、Oracle、Trino、达梦（`dm`，经本地桥接）、ClickHouse（TCP 与 HTTP）、MongoDB、Redis（含 `rediss://`、`redis+cluster://`、`redis+sentinel://`）、Elasticsearch、Milvus、Qdrant、Neo4j、InfluxDB、TDengine 和 Cassandra 支持代理，SQLite 和 Redis Unix socket 不支持；MySQL 需使用 `tcp(...)` 地址，PostgreSQL 不支持 Unix socket。`mongodb+srv://` 的 SRV 记录仍在本机解析。ClickHouse HTTP、Trino、Elasticsearch、Milvus、Qdrant、Neo4j、InfluxDB 和 TDengine 使用代理列表时不再读取 `HTTP_PROXY` 环境变量。
- 安全与分组：`new`/`edit` 支持 `--read-only`（默认拦截写操作）、`--env prod`（默认拦截 `DROP`/`TRUNCATE` 及无 `WHERE` 的 `UPDATE`/`DELETE`，可用 `--allow-write` 放行）、`--dsn-cmd`（连接时执行命令获取动态 DSN）以及 `--tag`（配合 `ls --tag` / `check --all --tag` 过滤）。
- 代理 URL 中的密码与 DSN 一样明文保存在配置文件；`ls` 的 `PROXIES` 列隐藏密码和参数。
