# 系统架构
> CLI、连接配置、数据库会话和交互输入的边界；修改结构与依赖前阅读。

## 调用方向

`main.go → NewCommand → Store / Session / shell → database.Driver`

CLI 实现在 `internal/dbh`，按文件职责拆分。CLI 参数解析与输入来源选择在 `command.go`；连接配置持久化在 `store.go`；SQL 会话和结果输出在 `database.go`；元数据输出编排在 `metadata.go`，非交互式结构命令在 `metadata_command.go`，连接诊断在 `check.go`；交互终端与补全在 `shell.go`；共享 SQL 分句器在 `sql.go`；`erd` 命令在 `erd.go`。

`internal/erd` 管理项目 `.dbh/` 中的 DBML 关系模型：全项目多 `.dbh/` 扫描与模型合并（`FindAllDirs` / `LoadAll`，含同名表冲突与跨项目表警告）、用本地 `internal/dbml` 解析 `<model>.dbml`（declared）与 `<model>.refs.dbml`（inferred，只含 `Ref` 及 `// when:`、`// evidence:` 注释指令）、校验关系两端、去重、表/字段/关系查询、邻接与最短路径、`diff` 对比、`text`/`dot`/`mermaid`/离线交互 `html`（含字段行内注释）渲染，以及通过 `internal/dbml/introspect` 读取 SQLite/PostgreSQL/MySQL/SQL Server/Oracle/达梦/Trino 结构（`init` 首次初始化、`resync` 更新已有模型、`pull` 输出或保存、`diff` 对比线上库）。查询类命令只读文件，不打开数据库会话；`internal/dbml/introspect` 不导入任何数据库驱动注册。

`internal/database.Driver` 定义会话创建、输入词法规则、切库及数据库/表/列/索引读取接口。MySQL、PostgreSQL、SQLite、SQL Server、Oracle、Trino、达梦、MongoDB、ClickHouse、Redis、Elasticsearch、Milvus、Qdrant、Neo4j、InfluxDB、TDengine、Cassandra 分别实现在 `internal/database/` 下各自包中；驱动注册、专属元数据查询、关系解析和能力差异留在各自包内。`internal/database/registry` 统一选择实现，供配置校验与会话初始化使用。

## 关键决策

- `Session` 持有 `database.Connection`；关系型实现仍使用同一条 `*sql.Conn`，保证事务、临时表和会话设置持续有效，MongoDB 实现持有官方 Go 驱动客户端和当前数据库；Redis 实现持有 go-redis 的单条固定连接，`SELECT`/`MULTI` 状态持续有效，并关闭自动重试以免重复写入。查询与元数据读取使用同一会话。
- 数据库实现接收现有连接和已设置超时的 context。元数据查询以命令文本与参数分别返回，由 `Session` 使用共同的表格/CSV/JSON 输出逻辑执行。`database.Rows` 同时表达 SQL 行和完整 MongoDB 文档，MongoDB 游标按需读取后续批次；JSON 保留嵌套结构和 BSON 扩展 JSON 类型，表格/CSV 保留完整文档文本。
- `\use` 和客户端 `USE` 由驱动接口处理。MySQL 在原会话切换数据库；PostgreSQL/ClickHouse 先连接成功，再关闭旧会话；MongoDB 复用客户端选择数据库。切换成功刷新结构候选，失败不替换当前连接，保存的 DSN 不变。
- SQL 分句先识别引号、注释和 PostgreSQL dollar quote，再处理分号；`Syntax.LineCommands`（Redis）改为按引号外的换行分句，历史不追加分号。分句器通过驱动接口获得词法规则，不在 CLI 内分散判断驱动名称。批量执行与交互执行共用分句器；更改它时同时验证两种入口。
- 连接可保存有序代理列表。`internal/database/proxy` 在打开会话时建立 SOCKS5/SSH 链，`Session` 持有该链并在关闭连接后释放；驱动通过 `Open`/`SwitchDatabase` 接收 `database.Dial`（nil 表示直连）并注入各自驱动的拨号钩子。SSH 通道经 `net.Pipe` 包装以支持驱动依赖的 deadline。
- `erd init` / `resync` / `pull` / `diff` 共用 `*sql.DB` 读取链路：关系型连接实现 `database.SQLPool`，把会话持有的唯一连接归还池后交出 `*sql.DB`，读取仍走同一物理连接和代理链；之后该连接只能关闭。
- 连接配置使用文件锁和原子替换；SQL 历史使用每个连接独立的 JSONL 文件，保留完整多行语句，以文件锁和原子替换保护并发更新；保留上限与去重逻辑在 `history.go`。
- 补全候选来自缓存的 SQL/JSON 命令关键字与数据库结构。MongoDB 字段来自集合样本文档。MySQL/PostgreSQL/ClickHouse 实现 `database.TableColumns`，用一条查询缓存表与字段的对应关系，供 `FROM`/`JOIN` 和别名字段补全使用，避免大库在高延迟链路上逐表往返；其他驱动逐表读取。键入字符时只匹配候选；结构读取放在连接初始化、切库成功后和 `\refresh`，避免每次按键访问数据库。
- 行编辑器负责候选菜单、光标和历史导航；业务层负责解析、执行与保存完整 SQL。输入片段和终端命令不混入持久化 SQL 历史。

新增驱动时一起更新连接校验、驱动注册、结构查询、分句差异、端到端（含适用数据库的 ERD）集成测试，以及 `.github/workflows/ci.yml` 的 `e2e-databases` Docker 矩阵（有可用公共镜像时）。新增用户命令时同步简写、README 和 `docs/` 下的使用文档。
