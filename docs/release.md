# GitHub Actions 与发布

分支提交、PR 和手动运行会触发 [CI](https://github.com/benenen/dbh/actions/workflows/ci.yml)。CI 在 Ubuntu 上执行格式检查、`go vet`、单元与 race 测试、真实 CLI/PTY 测试、lint、构建和 GoReleaser 配置检查。Go 版本取自 `go.mod`；未配置的外部数据库测试会跳过。

推送 `v主版本.次版本.修订版本` 标签会触发 [Release](https://github.com/benenen/dbh/actions/workflows/release.yml)。例如，确认要发布当前提交后执行：

```bash
git tag -a v0.1.0 -m "Release v0.1.0"
git push origin v0.1.0
```

Release 会先对标签指向的源码运行完整 CI，通过后用 GoReleaser 生成六个平台组合的二进制包、许可证、README 和 SHA-256 校验文件，并创建 GitHub Release。`v0.1.0-rc.1` 这类标签会标为预发布。也可以在 Actions 的 Release 页面选择 **Run workflow**，填写已经推送的标签；手动运行使用该标签源码，不使用页面选择分支的源码。

发布使用仓库自带的 `GITHUB_TOKEN`，只有发布 job 具有 `contents: write` 权限，无需个人令牌。Actions 引用固定到 commit SHA，GoReleaser 和 lint 工具也固定版本。

安装 GoReleaser v2 后，可先在本地验证和试打包：

```bash
make release-check
make release-snapshot
```

试打包会重建 `dist/` 中的快照包与校验文件，不创建标签或 GitHub Release。正式包的 `dbh --version` 显示标签版本；快照包带有快照版本。打包配置见 [.goreleaser.yaml](../.goreleaser.yaml)。
