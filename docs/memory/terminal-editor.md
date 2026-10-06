# 终端编辑器经验
> reeflective/readline 的实时候选与中断行为，需要用实际键盘序列验证。

- 2026-10-06：默认 `complete` 命令在 Tab 后调用 `SkipDisplay`，会使后续候选菜单隐藏；dbh 将 Tab 绑定到 `menu-complete`，PTY 验证了选择 `SELECT` 后继续输入 `na` 仍显示列名候选。相关配置见 `internal/dbh/shell.go`。
- 2026-10-06：候选被选中时直接调用 `History.Accept(..., ErrInterrupt)` 会留下虚拟完成缓冲区，下一轮输入带上旧前缀；中断处理先调用内置 `abort` 清理完成或搜索状态，再接受中断。PTY 使用 `se` → Tab → Ctrl-C → `SELECT 1;` 验证了新输入不携带 `se`。
- 2026-10-06：只按原始 ESC 字节绑定 ↑/↓ 或 Alt-Enter，会与默认的 meta 编码绑定归一化到相同按键，默认动作仍可能覆盖新绑定。通过 `inputrc.Unescape` 的 meta 形式覆盖箭头键，并同时覆盖 Alt-Enter 的原始与 `inputrc.Enmeta('\r')` 形式；PTY 验证了上移后退格修改上一行，以及 Alt-Enter 保持整块 SQL 待执行。
