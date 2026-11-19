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

多个库时用 `--model` 区分，例如 `--model reporting` 对应 `.dbh/reporting.dbml` 和 `.dbh/reporting.refs.dbml`。模型名只用字母、数字、`_`、`-`、`.`。

dbh 从当前目录逐级向上查找 `.dbh/`，到 Git 仓库根目录为止，因此在子目录中运行也会使用项目根目录的模型。模型与连接配置互不影响：`dbh remove` 不会删除 `.dbh/`。

## 生成结构

```bash
dbh erd pull pg --save                    # 写入 .dbh/schema.dbml
dbh erd pull mysql --db app --model app --save
dbh erd pull pg --schema billing -o billing.dbml
dbh erd pull local                        # 输出到 stdout
```

- 支持 SQLite、PostgreSQL 和 MySQL（8.0.16 及以上，或 MariaDB 10.2.1 及以上）。MongoDB、Redis、ClickHouse 不能 `pull`，可手工整理模型后查询。
- 读取范围：MySQL 读当前数据库（`--db` 切换）；PostgreSQL 读一个 schema，`--schema` 默认 `public`；SQLite 读 `main`。
- `--save` 覆盖 `.dbh/<model>.dbml`；还没有 `.dbh/` 时在 Git 仓库根目录创建，不在仓库内则在当前目录创建。`-o FILE` 写入指定文件，与 `--save` 互斥。
- `--save` 后会检查 `.refs.dbml`：已成为数据库外键的推断关系给出 warning，引用不存在的表或字段的推断关系给出 error，均输出到 stderr，不影响退出码。
- `--timeout` 限制连接和读取结构的总时间，默认 `60s`。

## 查询

以下命令只读 `.dbh/` 中的文件，不连接数据库，都支持 `--model`：

```bash
dbh erd check
dbh erd status
dbh erd tables --format tsv
dbh erd relations --table orders --source inferred --format tsv
dbh erd path reviews users
dbh erd path reviews users --declared-only
dbh erd show orders --depth 2
dbh erd show --format mermaid > erd.mmd
dbh erd show --format dot | dot -Tsvg > erd.svg
```

- `check` 校验两个文件，错误和警告以 `文件:行:列: 信息` 输出到 stderr；有错误时退出码非零。
- `status` 列出 `.dbh/` 中的模型及表数、关系数和状态（`ok`、`warning`、`invalid`）。
- `tables` 列出表、字段数、涉及的关系数和表注释。
- `relations` 列出关系：`name`、`from_table`、`from_columns`、`relation`、`to_table`、`to_columns`、`on_delete`、`on_update`、`source`。`relation` 为 `>`（多对一）、`-`（一对一）或 `<>`（多对多）；DBML 中写成 `<` 的关系会调换两端，统一显示为 `>`，`from_table` 是持有外键的一侧。多列关系的字段以 `, ` 连接。`--table` 只看两端任一为该表的关系，`--source declared|inferred` 只看一类。
- `path FROM TO` 输出两表之间跳数最少的关联路径，每行一跳，可直接用于拼写 JOIN。跳数相同时优先走 declared 关系；`--declared-only` 只走数据库外键；找不到路径时退出码非零。
- `show [TABLE...]` 预览表、字段和关系。给出表名时只显示这些表及 `--depth` 层（默认 1）内相邻的表。`--format text`（默认）在终端显示，`──▶` 为 declared、`┄┄▶` 为 inferred、`◀──` 为指向本表的关系；`--format dot` 输出 Graphviz，`--format mermaid` 输出 Mermaid `erDiagram`，inferred 关系均为虚线。

`tables`、`relations`、`path`、`status` 支持 `--format table|csv|json|tsv`。表名可写 `schema.table`，也可只写表名；只写表名且多个 schema 中都有时报错并列出候选。

查询时如果 `.refs.dbml` 中有引用不存在的表或字段的关系，会在 stderr 警告并跳过这些关系，其余结果照常输出；`<model>.dbml` 或 `.refs.dbml` 有语法错误时查询直接失败。

## 已知限制

- PostgreSQL 一次只读一个 schema；跨 schema 的外键会指向未读取的表。
- 不支持对比模型与数据库、生成迁移 SQL。
