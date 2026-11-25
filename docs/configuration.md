# 配置与历史

默认使用系统用户配置目录中的 `dbh/`（Linux 通常是 `~/.config/dbh`），也可以设置 `DBH_CONFIG_DIR` 或使用全局 `--config-dir PATH`。

```text
connections.json          命名连接配置（AES-256-GCM 加密存储）
.store.key                本地随机加密密钥（可通过 DBH_STORE_KEY 环境变量覆盖）
snippets.json             命名查询片段
history/<name>.jsonl      每个连接的 SQL 历史
```

目录权限为 `0700`，密钥、配置、片段和历史文件为 `0600`（Unix）。`connections.json` 使用 AES-256-GCM 加密保存（兼容自动迁移旧版明文配置），动态凭据可使用 `--dsn-cmd` 在连接时按需获取。配置修改使用文件锁与原子替换。删除连接默认一并清理历史（`--keep-history` 可保留）。
