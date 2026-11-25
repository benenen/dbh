# dbh

用 Go 编写的数据库 CLI，支持 SQLite、PostgreSQL、MySQL、SQL Server、Oracle、Trino/Presto、达梦（DM）、MongoDB、ClickHouse、Redis、Elasticsearch 和 Milvus。管理命名连接，执行 SQL、MongoDB 原生 JSON 命令、Elasticsearch/Milvus HTTP REST 命令或 redis-cli 风格的 Redis 命令，或进入支持实时补全与查询历史的交互终端。

## 安装

从 [GitHub Releases](https://github.com/benenen/dbh/releases) 下载预编译包，或从源码构建（需要 Go 1.26+）：

```bash
make install   # 安装到 GOBIN，未设置时为 GOPATH/bin
```

Shell 补全和其他安装方式见 [安装](docs/install.md)。

## 快速开始

```bash
# 保存连接（--prompt-dsn 隐藏输入 DSN，避免密码进入 shell 历史）
dbh new local --driver sqlite --dsn ./demo.db
dbh new pg --driver postgres --prompt-dsn

# 执行语句后退出
dbh exec local 'SELECT * FROM users;'
dbh exec pg --db app 'SELECT * FROM users WHERE id = $1' --param 42 --format tsv

# 查看结构、诊断连接
dbh tables pg
dbh check pg

# 生成/更新关系模型到 .dbh/，查询表关系
dbh erd init pg
dbh erd resync pg
dbh erd path orders users

# 进入交互终端
dbh connect local
```

完整命令见 `dbh --help`。

## 文档

- [安装](docs/install.md)：源码构建、预编译包、Shell 补全
- [连接管理](docs/connections.md)：新建、编辑、删除连接，DSN 写法、命令简写与代理
- [执行语句与输出格式](docs/exec.md)：输入方式、表格/CSV/JSON/TSV 输出、行数限制、统计与参数绑定
- [各数据库说明](docs/drivers.md)：MongoDB、Redis、ClickHouse 的命令与差异
- [结构查询与连接诊断](docs/schema.md)：`tables`、`describe`、`indexes`、`databases`、`check`
- [关系模型](docs/erd.md)：`erd` 生成 DBML 模型，查询表关系、JOIN 路径并预览
- [交互终端](docs/shell.md)：实时补全、多行编辑、`\` 命令与历史
- [配置与历史](docs/configuration.md)：配置目录、文件权限
- [开发与验证](docs/development.md)：构建、测试与集成测试环境变量
- [GitHub Actions 与发布](docs/release.md)：CI 与版本发布流程

## Agent 技能

仓库提供 [dbh-cli](skills/dbh-cli/SKILL.md) 技能，指导 agent 选择连接、执行查询、导出结果和查看数据库结构，覆盖所有驱动。使用时让 agent 读取该技能，例如“使用 dbh-cli 查询连接 docker-pgsql 的 dbh_test 数据库”。

[dbh-erd](skills/dbh-erd/SKILL.md) 技能指导 agent 用 `dbh erd` 从数据库拉取结构、从项目代码补充数据库未声明的关系，并查询表关系，例如“使用 dbh-erd 为这个项目生成关系模型”。
