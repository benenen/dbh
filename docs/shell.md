# 交互终端

```bash
dbh connect local
```

```text
local/main> CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT);
OK
local/main> INSERT INTO users (name) VALUES ('Alice');
OK
local/main> \refresh
local/main> SELECT *
...> FROM users;
┌────┬───────┐
│ id │ name  │
├────┼───────┤
│ 1  │ Alice │
└────┴───────┘
(1 rows)
local/main> \q
```

- 提示符显示 `连接名/当前数据库>`（SQLite 为 `main`），切库成功后立即更新，失败时保留原目标；使用简洁彩色提示符，保留普通终端滚动记录；设置 `NO_COLOR=1` 可关闭提示符颜色。
- SQL 以 `;` 结束；未完成时按 Enter 继续换行，所有行仍可编辑。Alt-Enter 可直接插入换行。字符串、注释、PostgreSQL dollar quote 内的分号不会切分语句。
- 在支持括号粘贴的终端中，多行粘贴保留换行、缩进和中文，整块留在输入区，按 Enter 后才执行；LF、CRLF 和 CR 换行均支持。粘贴中有多条 SQL 时，确认后按顺序执行。
- 输入过程中自动显示 SQL 关键字、表名、列名和终端命令的候选，无需先按 Tab；候选按不区分大小写的包含匹配过滤，前缀匹配优先，例如 `\describe pl` 可匹配 `platform`、`apple` 和 `sample`。`\use ` 之后只提示数据库名，`\describe `、`\indexes ` 之后只提示表名，敲完空格即列出全部名称。`\format ` 提示 `table`、`csv`、`json`、`tsv`；SQL 字符串和注释内不显示候选，退出字符串或注释后恢复。候选超过半屏时只显示一部分并提示剩余行数，移动选择时菜单随之滚动。Tab 或 ↓（候选显示时）进入候选菜单，方向键在菜单中移动，Enter 只确认候选、不执行，再按 Enter 才提交；Shift-Tab 选择上一项。结构来自当前数据库，不调用大模型。修改表结构后执行 `\refresh` 更新候选项。`FROM`、`JOIN` 及同一 `FROM` 列表的逗号后只提示表；`FROM users u` 或 `FROM users AS u` 后输入 `u.` 可提示该表字段，也支持光标在前面的 `SELECT u.`。这些候选来自缓存，按键时不查询数据库；暂不推导 CTE、子查询结果和表达式的字段。
- ↑/↓ 在多行输入中移动，到顶部/底部后回顾历史（在最后一行且有候选显示时，↓ 进入候选菜单）；Ctrl-R 搜索历史。匹配的历史 SQL 会作为灰色文字预览，在输入末尾按 → 接受。Ctrl-C 清除整块输入，在空输入区按 Ctrl-D 退出。
- `\database` 在 MySQL/PostgreSQL/MongoDB/ClickHouse/Redis 中列出数据库，每行一个名称，与 `\tables` 一样不受 `\format` 影响。MySQL 使用 `SHOW DATABASES`，PostgreSQL 查询 `pg_database`，MongoDB 列出服务器数据库；列表范围由数据库权限决定。SQLite 不支持此命令。
- `\use DATABASE` 或 `USE DATABASE;` 切换当前数据库，成功后刷新表/集合和字段提示，不修改保存的连接配置。MySQL 保留当前会话；PostgreSQL 重新建立连接，临时表、会话设置和未提交事务不保留，连接失败时仍保留旧连接；MongoDB 复用客户端并选择数据库，新库在写入数据后出现。SQLite 不支持切库。
- `\tables` 列出表和视图；`\describe TABLE` 显示每个字段的名称、类型、是否可空、默认值、主键顺序和注释，并附上索引列表；`\indexes TABLE` 单独查看索引。支持 `schema.table` 和带引号的名称（如 `"a.b"`、`"order items"`），可直接使用 `\tables` 列出的名称；SQLite 表名不区分大小写。结果遵循当前 `\format`。
- 索引显示名称、唯一性、主键标记及索引字段/定义。MySQL 联合索引按字段顺序逐行显示，包含索引类型、前缀长度和排序方向。SQLite 没有原生字段注释，注释显示为 `NULL`；`INTEGER PRIMARY KEY` 使用 rowid 时没有独立索引，因此不会出现在索引列表中。CSV/JSON 下字段与索引结果顺序输出，不插入文本标题。
- `\history` 查看历史，`\clear` 清除待执行 SQL，`\format table|csv|json|tsv` 切换格式，`\help` 查看帮助。

## 历史

历史按连接名存储，保留多行 SQL，默认保留最近 1000 条；重复 SQL 移至最新位置。全局 `--history-limit N` 调整保留条数，`0` 为不限，适用于启动读取、记录和当前交互会话。写入历史使用文件锁和原子替换。

```bash
dbh history local --search users    # 不区分大小写的文本搜索
dbh history local --clear           # 清空该连接的历史
dbh connect local --history-limit 200
```

`--no-history` 禁用本次历史读取与记录。交互执行后默认在 stderr 显示耗时与行数统计。

## 限制

当前分句器面向常规 SQL 和 PostgreSQL dollar quote 块；SQLite trigger、MySQL 存储过程等含内部未引用分号的复合语句以及 `DELIMITER` 客户端指令暂不支持。

MongoDB、Redis 的交互差异见 [各数据库说明](drivers.md)。
