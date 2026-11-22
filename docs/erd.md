# 关系模型（ERD）

`dbh erd`（简写 `er`）在项目根目录的 `.dbh/` 中维护 [DBML](https://docs.dbdiagram.io/dbml) 关系模型，查询表、关系和关联路径，并在终端预览。模型文件可以直接粘贴到 dbdiagram.io 查看。

## 模型文件

每个模型由两个文件组成，默认模型名为 `schema`：

| 文件 | 内容 | 维护方式 |
|---|---|---|
| `.dbh/schema.dbml` | 数据库实际结构：表、字段、索引、枚举和数据库中的外键 | `dbh erd pull NAME --save` 生成，可随时覆盖 |
| `.dbh/schema.refs.dbml` | 从代码推断、数据库中没有约束的关系 | 手工或 agent 维护，`pull` 不会改动 |

`<model>.dbml` 中的关系是 **declared**，`.refs.dbml` 中的关系是 **inferred**。`.refs.dbml` 只能写 `Ref` 和注释，证据写在注释里：

```dbml
// app/models/order.go:42 (GORM foreignKey)
Ref: orders.product_id > products.id
```

条件关联或多态关联（例如 `a = 1` 时 `b` 关联表 `c`，`a = 2` 时 `b` 关联表 `d`）拆成多条 `Ref`，并用 `// when: <条件>` 注释（紧邻 `Ref` 上方连续注释块内，或 `Ref` 行尾）标注条件，保持与标准 DBML 和 dbdiagram.io 兼容：

```dbml
// app/models/event.go:12
// when: a = 1
Ref: events.b > c.id

// app/models/event.go:15
Ref: events.b > d.id // when: a = 2
```

多个库时用 `--model` 区分，例如 `--model reporting` 对应 `.dbh/reporting.dbml` 和 `.dbh/reporting.refs.dbml`。模型名只用字母、数字、`_`、`-`、`.`。

查询命令（`status`、`check`、`tables`、`relations`、`path`、`show`）默认从工程根目录（Git 仓库根目录，不在仓库内则为当前目录）递归扫描**所有** `.dbh/` 目录，并合并其中的模型进行查询；未指定 `--model` 时加载全部模型，显式传入 `--model` 时只加载同名模型，传入 `--dir PATH` 时仅限定单个 `.dbh/` 目录。不同 `.dbh/` 中出现同名表时按表名合并字段与关系，跨 `.dbh/` 的推断关系同样可解析。`pull --save` 仍只写入当前或向上最近的单个 `.dbh/`（也可配合 `--dir` 指定目标目录）。详细扫描与合并流程见 [ERD 多 `.dbh/` 扫描与合并流程](erd-scan.md)。模型与连接配置互不影响：`dbh remove` 不会删除 `.dbh/`。

## 生成结构

```bash
dbh erd pull pg --save                    # 写入 .dbh/schema.dbml
dbh erd pull mysql --db app --model app --save
dbh erd pull pg --schema public,billing --exclude '*_bak' -o billing.dbml
dbh erd pull local                        # 输出到 stdout
dbh erd diff pg                           # 对比本地模型与线上数据库结构
```

- 支持 SQLite、PostgreSQL 和 MySQL（8.0.16 及以上，或 MariaDB 10.2.1 及以上）。MongoDB、Redis、ClickHouse 不能 `pull`，可手工整理模型后查询。
- 读取范围：MySQL 读当前数据库（`--db` 切换）；PostgreSQL 读 `--schema`（默认 `public`，支持逗号分隔多 schema 如 `--schema public,billing`）；SQLite 读 `main`。`--include` / `--exclude` 可按 glob 模式筛选表（可重复传入），被排除表关联的外键也会同步过滤。
- `--save` 覆盖 `.dbh/<model>.dbml`；还没有 `.dbh/` 时在 Git 仓库根目录创建，不在仓库内则在当前目录创建。`-o FILE` 写入指定文件，与 `--save` 互斥。覆盖已有模型文件时会打印一行 `Changes: ...` 变更摘要。
- `--save` 后会检查 `.refs.dbml`（多 `.dbh/` 工程自动结合全局表集合校验，避免跨目录推断误报）：已成为数据库外键的推断关系给出 warning，引用不存在的表或字段的推断关系给出 error，均输出到 stderr，不影响退出码。
- `diff NAME` 对比本地 `.dbh/<model>.dbml` 与实时数据库结构，输出新增/删除/修改的表、字段与外键（支持 `--format table|csv|json|tsv`）。
- `--timeout` 限制连接和读取结构的总时间，默认 `60s`。

## 查询

以下命令只读 `.dbh/` 中的文件，不连接数据库，都支持 `--model`：

```bash
dbh erd check
dbh erd status
dbh erd tables --format tsv
dbh erd tables --match 'order*' --with-model --format tsv
dbh erd tables --isolated --format tsv
dbh erd relations --table orders --source inferred --format tsv
dbh erd path reviews users
dbh erd path orders users --all --format tsv
dbh erd path reviews users --declared-only
dbh erd show orders --depth 2 --declared-only
dbh erd show --format mermaid > erd.mmd
dbh erd show --format dot | dot -Tsvg > erd.svg
dbh erd show --format html -o erd.html
dbh erd show --format html --open
```

- `check` 校验两个文件，错误和警告以 `文件:行:列: 信息` 输出到 stderr；有错误时退出码非零。
- `status` 列出 `.dbh/` 中的模型（含无主文件的独立 `.refs.dbml`）及表数、关系数和状态（`ok`、`warning`、`invalid`）。
- `tables` 列出表、字段数、涉及的关系数和表注释；支持 `--match`（按表名/注释子串或 glob 过滤）、`--isolated`（仅无关系的孤立表）和 `--with-model`（输出所属模型列）。
- `relations` 列出关系：`name`、`from_table`、`from_columns`、`relation`、`to_table`、`to_columns`、`condition`、`on_delete`、`on_update`、`source`。`relation` 为 `>`（多对一）、`-`（一对一）或 `<>`（多对多）；DBML 中写成 `<` 的关系会调换两端，统一显示为 `>`，`from_table` 是持有外键的一侧。多列关系的字段以 `, ` 连接。`--table` 只看两端任一为该表的关系，`--source declared|inferred` 只看一类。
- `path FROM TO` 输出两表之间跳数最少的关联路径，每行一跳（含 `condition`），可直接用于拼写 JOIN。跳数相同时优先走 declared 关系；`--declared-only` 只走数据库外键；`--all` 列出全部最短路径（含平行外键与不同条件关系）；找不到路径时退出码非零。
- `show [TABLE...]` 预览或导出表、字段和关系，支持 `-o FILE` 写入文件、`--declared-only` / `--source declared|inferred` 过滤关系。给出表名时只显示这些表及 `--depth` 层（默认 1）内相邻的表。`--format text`（默认）在终端显示字段、默认值、注释、表级索引和关系，`──▶` 为 declared、`┄┄▶` 为 inferred、`◀──` 为指向本表的关系；`--format dot` 输出 Graphviz，`--format mermaid` 输出 Mermaid `erDiagram`，inferred 关系均为虚线；`--format html` 导出完全离线自包含的交互式 HTML ER 图（支持自动分层布局、拖拽表卡片、缩放平移、按表/字段/注释搜索、按 declared/inferred 过滤、列注释与索引悬停提示、条件与复合关系标签展示、浅/深色切换以及带主题底色的 SVG 导出），加 `--open` 可直接用默认浏览器打开（未指定 `-o` 时自动写入临时 `.html` 文件）。

`tables`、`relations`、`path`、`status`、`diff` 支持 `--format table|csv|json|tsv`。表名可写 `schema.table`，也可只写表名；只写表名且多个 schema 中都有时报错并列出候选。

查询时如果 `.refs.dbml` 中有引用不存在的表或字段的关系，会在 stderr 警告并跳过这些关系，其余结果照常输出；`<model>.dbml` 或 `.refs.dbml` 有语法错误时查询直接失败。

## 已知限制

- 不支持生成迁移 SQL。
