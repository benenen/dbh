# ERD 关系模型 spec

> 状态：已实现（见 [关系模型文档](../erd.md)）。项目根目录 `.dbh/` 中维护 DBML 关系模型：数据库结构由 `dbh erd pull` 生成，代码中推断的关系单独存放；`dbh erd` 查询表、关系和关联路径；`dbh-erd` 技能负责生成和维护模型。

## 背景与目标

数据库里的外键往往不完整：很多关系只存在于 ORM 模型、迁移脚本或业务 SQL 中。目标是：

1. Agent 通过 `dbh-erd` 技能，结合数据库实际结构和项目代码，在项目根目录的 `.dbh/` 中生成 DBML 关系模型。
2. 用户和 Agent 用 `dbh erd` 查询表、关系和关联路径，并在终端预览。

模型格式采用 [DBML](https://docs.dbdiagram.io/dbml)，可直接粘贴到 dbdiagram.io 查看。解析、Dot 生成和数据库结构读取复用 [`github.com/mevdschee/dbml-tools`](https://pkg.go.dev/github.com/mevdschee/dbml-tools)（MIT）。

## 范围

本期包含：

- `dbh erd` 命令组：从数据库生成 DBML、校验、查询表与关系、查询关联路径、终端预览。
- 区分数据库已声明的外键（declared）与从代码推断的关系（inferred）。
- `skills/dbh-erd/SKILL.md`：指导 Agent 生成和维护模型。

本期不包含：

- 用命令增删改模型中的表、字段、关系（直接编辑 DBML 文件）。
- 模型与数据库对比、生成迁移 SQL（后续可基于 `generator.Migrate` 实现 `erd diff`）。
- `pull` 的表名过滤；PostgreSQL 多 schema 读取。
- MongoDB、Redis、ClickHouse 的结构读取；它们可以使用手工整理的模型查询，但不能 `pull`。

## 模型文件

每个模型由两个文件组成，按逻辑模型名命名，与连接名无关：

| 文件 | 内容 | 维护者 |
|---|---|---|
| `.dbh/<model>.dbml` | 数据库实际结构：表、字段、索引、枚举、数据库中的外键 | `erd pull --save` 生成，可随时覆盖重新生成 |
| `.dbh/<model>.refs.dbml` | 从代码推断的关系，只含 `Ref` 和注释 | Agent 或人维护，`pull` 从不改动 |

- 默认模型名为 `schema`，即 `.dbh/schema.dbml` 与 `.dbh/schema.refs.dbml`；多个库时用 `--model M` 区分，如 `.dbh/reporting.dbml`。查询类命令未指定 `--model` 时加载全部模型，`pull` 未指定 `--model` 时默认使用 `schema`。
- 模型名只允许字母、数字、`_`、`-`、`.`，不以 `.` 开头，不以 `.refs` 结尾，路径不能逃逸 `.dbh/`。
- `.refs.dbml` 可选；不存在时模型只有 declared 关系。
- 定位 `.dbh/`：
  - 查询类命令（`status`、`check`、`tables`、`relations`、`path`、`show`）默认从工程根目录（最近的 `.git` 所在目录，不在 Git 仓库内则为当前目录）并发递归扫描所有 `.dbh/`（跳过其他隐藏目录、常见构建/依赖目录、`.gitignore` 忽略的目录以及深度超过 8 层的子树），两阶段合并表与关系，从而支持跨 `.dbh/` 的推断关联与 `path` 寻路；传入 `--dir PATH` 时限定单个 `.dbh/`。详细流程见 [ERD 多 `.dbh/` 扫描与合并流程](../erd-scan.md)。
  - 写命令 `pull --save` 仍从当前目录向上查找最近的单个 `.dbh/`（到达 Git 根目录未找到则在根目录创建，不在仓库内则在当前目录创建），传入 `--dir PATH` 时写入指定目录。
- 查询时每次重新读取并解析文件，编辑后立即生效。
- 模型与连接配置互不影响：`dbh remove` 不动 `.dbh/`，`.dbh/` 也不记录连接名。

## 关系来源

- **declared**：出现在 `<model>.dbml` 中的关系，包括独立 `Ref` 和行内 ref。
- **inferred**：出现在 `<model>.refs.dbml` 中的关系。证据写在 Ref 上方的注释中，名称可选：

  ```dbml
  // app/models/order.go:42 (GORM foreignKey)
  Ref: orders.user_id > users.id
  ```

- 来源只由所在文件判断，不解析名称或注释。
- 去重：两条关系两端的表与字段集合相同、方向相同或相反时视为同一关系。inferred 与 declared 重复时按 declared 处理；`check` 和 `pull --save` 警告该 inferred 关系可以删除。
- 失效：inferred 关系引用不存在的表或字段时，`check` 报错；查询类命令在 stderr 警告并跳过该关系，其余结果照常输出。
- `.refs.dbml` 只允许 `Ref` 和注释，出现 `Table`、`Enum`、`TableGroup`、`Project`、`Note` 等其他声明时所有命令报错。
- `<model>.dbml` 解析失败时所有命令报错。

## 命令

命令组 `dbh erd`，简写 `er`，完整名与简写行为相同。除 `pull` 外都只读 `.dbh/` 中的文件，不连接数据库。`--dir PATH` 限定单个 `.dbh/` 目录，`--model M` 选择模型（查询命令默认加载全部模型，`pull` 默认 `schema`）。

| 命令 | 作用 |
|---|---|
| `erd pull NAME [--db DB] [--schema S] [--dir PATH] [--model M] [--save \| -o FILE]` | 用连接 `NAME` 读取数据库结构，输出 DBML（默认 stdout）；`--save` 写入 `.dbh/<model>.dbml` |
| `erd check [--dir PATH] [--model M]` | 校验模型文件（多 `.dbh/` 时按合并后的表集合校验跨目录推断关系）；错误与警告以 `FILE:行:列: 信息` 输出到 stderr，有错误时退出码非零 |
| `erd status [--dir PATH] [--model M] [--format F]` | 列出工程内所有 `.dbh/` 中的模型；多 `.dbh/` 时结合全局表集合校验各 `.refs.dbml` |
| `erd tables [--dir PATH] [--model M] [--format F]` | 列出模型中的表 |
| `erd relations [--dir PATH] [--model M] [--table T] [--source S] [--format F]` | 列出关系；`--table` 只看与该表相关的关系（两端任一匹配），`--source declared\|inferred` 只看一类 |
| `erd show [TABLE...] [--dir PATH] [--model M] [--depth N] [--format text\|dot\|mermaid]` | 终端预览；给出 TABLE 时只显示这些表及 N 层内相邻的表（默认 1） |
| `erd path FROM TO [--dir PATH] [--model M] [--declared-only] [--format F]` | 两表之间的最短关联路径，供写 JOIN 参考 |

### `pull`

- 复用现有会话打开连接（含代理链和 `--timeout`），通过 dbml-tools `introspect.Introspect` 读取表、字段、主键、唯一约束、索引、外键、枚举，再用 `introspect.GenerateDBML` 生成 DBML，保留原生类型。
- 驱动映射：`sqlite` → SQLite，`postgres` → PostgreSQL，`mysql` → MariaDB 方言。MySQL 最低 8.0.16、MariaDB 最低 10.2.1（dbml-tools 读取 `information_schema.check_constraints`）；低于该版本时报错说明最低版本。`--timeout` 限制连接与读取的总时间，默认 `60s`。其他驱动报错 `erd pull is not supported for <driver>`。
- 读取范围：MySQL 读当前数据库（`--db` 先切换）；PostgreSQL 读单个 `--schema`，默认 `public`；SQLite 读 `main`。
- `--save` 覆盖 `.dbh/<model>.dbml`；找不到 `.dbh/` 时在 Git 仓库根目录创建，不在仓库内则在当前目录创建。写入使用临时文件加原子替换。`-o` 与 `--save` 互斥。
- `--save` 后若 `.refs.dbml` 存在，按 `check` 规则检查，把重复与失效的 inferred 关系作为警告输出到 stderr，不影响退出码。
- `Introspect` 需要 `*sql.DB`，而会话持有连接池上限为 1 的单条 `*sql.Conn`，直接交出会等待到超时。在 `internal/database` 增加可选接口，让 SQL 连接把持有的连接归还连接池后交出 `*sql.DB`，由同一物理连接完成读取；`pull` 是一次性会话，结束后整体关闭。不另建第二套连接或 DSN 解析。

### `path`

- 把关系视为无向边，在表之间做 BFS，返回跳数最少的路径；跳数相同时 declared 优先，再按表名排序，保证结果稳定。
- `--declared-only` 只走 declared 关系。
- 每行一跳：`step`、`from_table`、`from_columns`、`to_table`、`to_columns`、`source`。找不到路径时报错，退出码非零。

### 输出格式

- `tables`、`relations`、`path`、`status` 支持 `--format table|csv|json|tsv`，与现有结构命令一致，默认 `table`。
- `status` 列：`model`、`file`、`refs_file`（不存在时为空）、`tables`、`declared`、`inferred`、`state`（`ok` / `warning` / `invalid`）。
- `tables` 列：`table`、`columns`（字段数）、`relations`（涉及的关系数）、`note`。
- `relations` 列：`name`、`from_table`、`from_columns`、`relation`（`>`、`-`、`<>`；`<` 调换两端后按 `>` 输出）、`to_table`、`to_columns`、`on_delete`、`on_update`、`source`。多列外键的字段以 `, ` 连接。
- `show --format text` 为终端预览，inferred 关系用虚线箭头区分：

  ```
  orders
    id          integer   pk
    user_id     integer   not null  ──▶ users.id      on delete cascade
    product_id  integer             ┄┄▶ products.id   inferred
  users
    id          integer   pk
    ◀── orders.user_id
  ```

  `--format dot` 输出 Graphviz，inferred 边为虚线；`--format mermaid` 输出 `erDiagram`，inferred 关系用非标识关系线（`..`）。预览不着色。
- 表名匹配：模型中带 schema 的表可写 `schema.table` 或仅写表名；仅写表名且匹配多个 schema 时报错并列出候选。

### 错误与退出码

- 没有模型：`no ERD model "schema" (looked for .dbh/schema.dbml); run dbh erd pull NAME --save`，退出码非零。
- 错误信息不包含 DSN 与密码，沿用现有 `redact`。

## `dbh-erd` 技能

位于 `skills/dbh-erd/SKILL.md`，在用户要求“生成/更新数据库关系模型、从代码补充表关系、查询表之间的关系”时触发。流程：

1. 用 `dbh list` 和用户目标确定连接与数据库，用 `dbh check NAME` 确认可连；用 `dbh erd status` 查看已有模型，多个库时与用户确认模型名。
2. 生成或更新结构：`dbh erd pull NAME --save [--model M]`，直接覆盖生成文件，按输出的警告清理 `.refs.dbml`。MongoDB、Redis、ClickHouse 无法 `pull`：根据 `dbh tables` / `dbh describe` 手工整理 `<model>.dbml`，首行注释 `// generated by: manual (<driver>)`，只写表和字段，所有关系写入 `.refs.dbml`。
3. 在项目代码中查找数据库未声明的关系，来源按可信度排序：ORM 模型的关联定义（如 GORM `foreignKey`、Django `ForeignKey`、SQLAlchemy `relationship`、TypeORM `@ManyToOne`）、迁移脚本、业务 SQL 的 `JOIN ... ON`、`xxx_id` 命名约定。
4. 把关系追加到 `.refs.dbml`，每条上方注释写明证据位置。仅凭命名推断的关系先向用户列出，确认后才写入。
5. `dbh erd check` 通过后，用 `dbh erd relations --source inferred` 和 `dbh erd show` 核对，汇报新增关系数量与证据。
6. 不修改数据库结构；模型文件的提交按用户授权进行。

## 实现要点

- 独立包 `internal/erd`：`model.go`（`.dbh/` 定位、模型加载、来源判断、去重、邻接表与路径）、`render.go`（text/dot/mermaid 预览）、`rows.go`（查询结果行）、`pull.go`（读取结构与原子写入）。`internal/dbh/erd.go` 只负责命令注册、参数与输出格式，在 `command.go` 注册。
- 两个文件分别解析与解释，错误位置天然属于各自文件；dbml-tools 解释器不校验 Ref 两端，由 `internal/erd` 按 `<model>.dbml` 的表和字段校验。
- `internal/database` 增加可选的 `*sql.DB` 借出接口，仅 `sqlConnection` 实现。
- 新增依赖 `github.com/mevdschee/dbml-tools` v0.9.11；只导入 `lexer`、`parser`、`interpreter`、`introspect`，不导入其驱动注册，`lib/pq` 不会编入 dbh。Dot 输出由 dbh 自行生成，以便区分 inferred 边。上游会丢弃名为 `note`/`records` 的字段、重复 PostgreSQL 多列外键、生成无法解析的带类型转换默认值；dbh 通过 `replace` 使用 fork `github.com/BenLocal/dbml-tools` 的 `v0.9.11-dbh.1`（分支 `dbh-patches`）。
- `show` 的邻居与 `path` 共用同一份邻接表。

## 文档与索引

- 新增 `docs/erd.md` 使用文档；README 命令列表加入 `erd`；`dbh-cli` 技能补充 `dbh erd` 查询入口。
- 新增技能后运行 `bash docs/agents-dot-md/reindex.sh`；`docs/agents-dot-md/architecture.md` 补充 erd 模块、`.dbh/` 约定和新依赖。

## 验证

- 单元测试：`.dbh/` 向上查找与 Git 根目录边界、模型名校验、`--save` 创建目录与覆盖、两文件合并后的错误行号映射、`.refs.dbml` 非 Ref 声明报错、来源判断、去重警告、失效关系（check 报错、查询警告并跳过）、`relations`/`tables`/`path`/`status` 在四种格式下的输出、`show` 三种格式、多列外键、带 schema 的表名匹配、`path` 的稳定排序与 `--declared-only`。
- SQLite e2e：临时库建表加外键，`pull --save` → 写 `.refs.dbml` → `check` / `relations` / `path` / `show`；完整名与 `er` 简写一致。
- PostgreSQL / MySQL：设置 `DBH_TEST_POSTGRES_DSN` / `DBH_TEST_MYSQL_DSN` 时运行 `pull` 集成测试，未设置时明确跳过，不计为通过。
- 本机有 Graphviz 时，用 `dot -Tsvg` 检查 `show --format dot` 可渲染。
