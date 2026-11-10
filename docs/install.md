# 安装

需要 Go 1.26 或更新版本；使用下列命令还需要 Make。

```bash
make build
# 或安装到 Go 的 bin 目录
make install
# 指定安装目录
GOBIN="$HOME/.local/bin" make install
```

`make build` 生成 `bin/dbh`；`make install` 安装到 `GOBIN`，未设置时使用 `GOPATH/bin`（通常为 `~/go/bin`）。将安装目录加入 `PATH` 后即可直接运行 `dbh`。不使用 Make 时，也可运行 `go build -o bin/dbh .` 或 `go install .`。

也可从 [GitHub Releases](https://github.com/benenen/dbh/releases) 下载预编译包。Linux、macOS 和 Windows 均提供 amd64、arm64 版本；Linux/macOS 使用 `.tar.gz`，Windows 使用 `.zip`。解压后将 `dbh`（Windows 为 `dbh.exe`）放入 `PATH`，运行 `dbh --version` 确认版本。每次发布包含 `checksums.txt`，用于核对下载文件的 SHA-256；本地普通构建显示 `dev`。

## Shell 补全

在 `~/.bashrc` 中加入下面几行，重新打开终端后即可用 Tab 补全子命令、已保存的连接名（`dbh c z<Tab>` → `dbh c zgy-mysql`）、`--driver`/`--format` 取值、`--dsn-env` 环境变量名，以及 `--file`/`-f` 和 SQLite `--dsn` 的本地文件路径。zsh 和 fish 分别使用 `dbh completion zsh`、`dbh completion fish`。

```bash
if command -v dbh >/dev/null 2>&1; then
    source <(dbh completion bash)
fi
```
