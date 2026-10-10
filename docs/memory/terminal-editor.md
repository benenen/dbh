# 终端编辑器经验
> reeflective/readline 的实时候选与中断行为，需要用实际键盘序列验证。

- 2026-10-06：默认 `complete` 命令在 Tab 后调用 `SkipDisplay`，会使后续候选菜单隐藏；dbh 将 Tab 绑定到 `menu-complete`，PTY 验证了选择 `SELECT` 后继续输入 `na` 仍显示列名候选。相关配置见 `internal/dbh/shell.go`。
- 2026-10-06：候选被选中时直接调用 `History.Accept(..., ErrInterrupt)` 会留下虚拟完成缓冲区，下一轮输入带上旧前缀；中断处理先调用内置 `abort` 清理完成或搜索状态，再接受中断。PTY 使用 `se` → Tab → Ctrl-C → `SELECT 1;` 验证了新输入不携带 `se`。
- 2026-10-06：只按原始 ESC 字节绑定 ↑/↓ 或 Alt-Enter，会与默认的 meta 编码绑定归一化到相同按键，默认动作仍可能覆盖新绑定。通过 `inputrc.Unescape` 的 meta 形式覆盖箭头键，并同时覆盖 Alt-Enter 的原始与 `inputrc.Enmeta('\r')` 形式；PTY 验证了上移后退格修改上一行，以及 Alt-Enter 保持整块 SQL 待执行。
- 2026-10-06：Ghostty 中连接后显示提示符但键盘、Ctrl-C 与退出均无响应，PTY 注入 `ESC[1;7R` 可稳定复现。readline v1.3.0 将光标回报无条件送入无缓冲通道，关闭光标探测后没有接收者，永久阻塞输入读取；原始终端模式下 Ctrl-C 也只能走该读取路径。本地依赖补丁缓存一条回报并使用非阻塞发送；PTY 验证了单独与重复回报、回报同批 SQL、Ctrl-C 和退出。单纯调整退出按键或打开光标探测无法稳妥解决这类问题。
- 2026-10-06：关闭光标探测时，readline 无法判断输入是否处于窗口底行；清理候选区域所用的 CUD 在底行原地停留，清屏擦掉输入后，CUU 将光标错误上移，与连接提示重叠。本地补丁在绝对行号未知时用换行代替 CUD，允许终端滚屏后正确上移。PTY 输出回放与独立终端模拟器均复现旧行为并验证修复；布局回归覆盖窗口顶部、最后两行、候选菜单、Ctrl-C、执行后提示符与多行 SQL。
- 2026-10-08：将 dbh 候选改为包含匹配后，`pl` 仍只显示前缀候选；readline `Engine.generate` 会对调用方返回的候选再次执行 `FilterPrefix`，丢弃 `apple` 等非前缀匹配。本地补丁增加 `Completions.NoFilter()` 跳过该过滤，并让 `InsertCommonPrefix` 在公共前缀不以输入开头时不改写输入；PTY 逐字输入 `p`、`l` 验证了 `apple`、`platform` 同时出现，`AMP` + Tab 替换为 `sample`。
- 2026-10-10：用 pyte 回放 PTY transcript 检查屏幕时，Python `open()` 默认的通用换行会把 `\r\n` 和单独的 `\r` 都变成 `\n`，回放出续行大缩进、重复绘制等假象；读取时必须传 `newline=''`。仓库自带的 `terminalScreen` 把每个 rune 当作一列，不能验证中文等宽字符布局，需要真实宽度时用 pyte 回放。
- 2026-10-10：menu-select 键表未绑定 Enter 时，按键回落到 emacs 的 `accept-line`，`acceptLineWith` 先 `completer.Reset()` 接受候选再提交整行，表现为"选中候选的同时换行或直接执行"。dbh 在 menu-select 中把 `\r` 绑定到只调用 `Reset()` 的命令。菜单内方向键本就按网格移动（`adjustCycleKeys` 把 ↓ 换算成下一行），单行菜单下看起来像线性移动。↓ 进入菜单只在候选可见且光标位于最后一行时生效：↑ 移到上一行后光标常落在单词上并显示候选，若不限定最后一行，↓ 就无法回到下一行。PTY 回归见 `TestTerminalMenuSelectionKeys`。
