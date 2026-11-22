# 结构查询与连接诊断

结构查询无需进入交互终端，复用同一数据库连接与驱动元数据逻辑：

```bash
dbh tables pg --format tsv
dbh describe local '"order items"' --format tsv
dbh indexes mysql users --db app --format json
dbh databases pg --format tsv
dbh check pg --db app --timeout 10s
dbh check --all --format tsv
```

`tables`（简写 `tbl`）、`describe`（简写 `desc`）、`indexes`（简写 `idx`）、`databases`（简写 `dbs`）支持 `--format table|csv|json|tsv` 和 `--timeout`；除 `databases` 外均支持 `--db`。表或数据库列表使用 `name` 列；`describe` 依次输出字段与索引，CSV/JSON/TSV 不插入文本标题。

`check [NAME]` 验证保存的配置、逐跳代理连接、数据库连接与认证，以及当前数据库访问；支持 `--db` 验证指定服务器数据库、`--all` 批量巡检所有已保存连接，以及 `--format table|csv|json|tsv` 结构化输出。SSH 在握手后报告，SOCKS5 在实际建立到下一跳或数据库的通道后报告；失败区分代理跳、网络、认证和数据库阶段，退出码非零。诊断只显示错误分类与耗时，不显示 DSN、密码或服务器原始错误。`--timeout` 限制单次诊断。SQLite 连接诊断会遵循驱动行为，在文件不存在时创建数据库文件。
