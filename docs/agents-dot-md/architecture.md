# 系统架构
> CLI、连接配置、数据库会话和交互输入的边界；修改结构与依赖前阅读。

## 调用方向

`main.go → NewCommand → Store / Session / shell → database.Driver`

CLI 实现在 `internal/dbh`，按文件职责拆分。CLI 参数解析与输入来源选择在 `command.go`；连接配置持久化在 `store.go`；SQL 会话和结果输出在 `database.go`；元数据输出编排在 `metadata.go`；交互终端与补全在 `shell.go`；共享 SQL 分句器在 `sql.go`。

`internal/database.Driver` 定义会话创建、输入词法规则、切库及数据库/表/列/索引读取接口。MySQL、PostgreSQL、SQLite、MongoDB、ClickHouse 分别实现在 `internal/database/mysql`、`internal/database/postgres`、`internal/database/sqlite`、`internal/database/mongo`、`internal/database/clickhouse`；驱动注册、专属元数据查询、关系解析和能力差异留在各自包内。`internal/database/registry` 统一选择实现，供配置校验与会话初始化使用。

## 关键决策

- `Session` 持有 `database.Connection`；关系型实现仍使用同一条 `*sql.Conn`，保证事务、临时表和会话设置持续有效，MongoDB 实现持有官方 Go 驱动客户端和当前数据库。查询与元数据读取使用同一会话。
- 数据库实现接收现有连接和已设置超时的 context。元数据查询以命令文本与参数分别返回，由 `Session` 使用共同的表格/CSV/JSON 输出逻辑执行。`database.Rows` 同时表达 SQL 行和完整 MongoDB 文档，MongoDB 游标按需读取后续批次；JSON 保留嵌套结构和 BSON 扩展 JSON 类型，表格/CSV 保留完整文档文本。
- `\use` 和客户端 `USE` 由驱动接口处理。MySQL 在原会话切换数据库；PostgreSQL/ClickHouse 先连接成功，再关闭旧会话；MongoDB 复用客户端选择数据库。切换成功刷新结构候选，失败不替换当前连接，保存的 DSN 不变。
- SQL 分句先识别引号、注释和 PostgreSQL dollar quote，再处理分号。分句器通过驱动接口获得词法规则，不在 CLI 内分散判断驱动名称。批量执行与交互执行共用分句器；更改它时同时验证两种入口。
- 连接可保存有序代理列表。`internal/database/proxy` 在打开会话时建立 SOCKS5/SSH 链，`Session` 持有该链并在关闭连接后释放；驱动通过 `Open`/`SwitchDatabase` 接收 `database.Dial`（nil 表示直连）并注入各自驱动的拨号钩子。SSH 通道经 `net.Pipe` 包装以支持驱动依赖的 deadline。
- 连接配置使用文件锁和原子替换；SQL 历史使用每个连接独立的 JSONL 文件，保留完整多行语句。
- 补全候选来自缓存的 SQL/JSON 命令关键字与数据库结构。MongoDB 字段来自集合样本文档。键入字符时只匹配候选；结构读取放在连接初始化、切库成功后和 `\refresh`，避免每次按键访问数据库。
- 行编辑器负责候选菜单、光标和历史导航；业务层负责解析、执行与保存完整 SQL。输入片段和终端命令不混入持久化 SQL 历史。

新增驱动时一起更新连接校验、驱动注册、结构查询、分句差异和集成测试。新增用户命令时同步简写和 README。
