# 结构查询与连接诊断

结构查询无需进入交互终端，复用同一数据库连接与驱动元数据逻辑：

```bash
dbh tables pg --format tsv
dbh describe local '"order items"' --format tsv
dbh indexes mysql users --db app --format json
dbh databases pg --format tsv
dbh check pg --timeout 10s
```

`tables`、`describe`、`indexes`、`databases` 支持 `--db`、`--format table|csv|json|tsv` 和 `--timeout`。表或数据库列表使用 `name` 列；`describe` 依次输出字段与索引，CSV/JSON/TSV 不插入文本标题。

`check NAME` 验证保存的配置、逐跳代理连接、数据库连接与认证，以及当前数据库访问。SSH 在握手后报告，SOCKS5 在实际建立到下一跳或数据库的通道后报告；失败区分代理跳、网络、认证和数据库阶段，退出码非零。诊断只显示错误分类与耗时，不显示 DSN、密码或服务器原始错误。`--timeout` 限制整次诊断。SQLite 连接诊断会遵循驱动行为，在文件不存在时创建数据库文件。
