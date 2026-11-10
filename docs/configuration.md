# 配置与历史

默认使用系统用户配置目录中的 `dbh/`（Linux 通常是 `~/.config/dbh`），也可以设置 `DBH_CONFIG_DIR` 或使用全局 `--config-dir PATH`。

```text
connections.json          命名连接配置
history/<name>.jsonl       每个连接的 SQL 历史
```

目录权限为 `0700`，配置和历史文件为 `0600`（Unix）。配置包含明文 DSN，历史可能包含 SQL 中的敏感值。配置修改使用文件锁与原子替换。删除连接后保留历史，再次使用相同名称可复用历史。
