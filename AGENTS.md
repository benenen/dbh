# dbh 项目指令

> 本仓库的常驻规则与文档索引；`CLAUDE.md` 链接到本文件。细则按触发条件读取，索引由 agents-dot-md 技能生成。

## 项目约束

- 使用 Go 实现数据库 CLI，沿用现有 `database/sql` 会话与连接配置体系。
- 交互式 SQL 提示应在输入过程中显示候选；验证时实际输入字符，不以 Tab 补全测试代替实时提示验证。
- 命令完整名与简写保持相同行为；命令及使用方法以 `dbh --help` 和 `README.md` 为准。
- 沟通默认简体中文；代码注释、CLI 输出和提交信息沿用仓库英文风格，详见 [语言约定](docs/agents-dot-md/translation.md)。
- 提交和推送按用户已授权范围执行；数据库测试使用独立测试库，真实凭据不入库。

## 实现准则

1. 动手前说明关键假设，把需求变成可验证的结果。
2. 只实现当前需求，优先复用既有逻辑。
3. 改动范围围绕当前任务，清理本次产生的未使用代码和依赖。
4. 完成适当验证后报告结果；明确哪些数据库或终端行为尚未实测。

## 按任务读取

- 写代码前读 [技术栈与验证](docs/agents-dot-md/tech-stack.md)；修改后逐条检查 [代码清单](docs/agents-dot-md/code-checklist.md)。
- 改模块边界、连接会话或添加依赖前读 [架构](docs/agents-dot-md/architecture.md)。
- 改 CLI 参数、错误处理或输出格式时读 [编码约定](docs/agents-dot-md/coding-guidelines.md)。
- 配置测试数据库、构建产物或执行提交推送时读 [开发环境](docs/agents-dot-md/environment.md)。

## 技能与索引维护

- 仓库自带技能放在 `skills/<name>/SKILL.md`，全局技能不复制进仓库。
- 增删技能、文档模块、记忆主题，或修改技能摘要后，运行 `python3 <agents-dot-md 技能目录>/scripts/reindex.py .`。
- 保留下方索引标记，标记之间的内容只由脚本更新。
- 新模块和记忆文件使用 `# 标题` 与 `> 一句话摘要`，供脚本收录。

## 记忆记录

- 本仓库特有的排障经验按主题保存到 `docs/memory/`，每条以绝对日期开头，写明现象、原因、验证证据和做法。
- 只记录已验证且能节省后续排查的结论；代码结构、提交历史、模块已有内容和秘密不重复记录。
- 跨项目偏好和本机环境事实使用已配置的记忆服务；同一事实只保存一处。

<!-- MEM0_ACTIVE_MEMORY_START -->
## Mem0 主动记忆

每个新会话开始时，读取并遵守 `/home/shiben/.agents/mem0-policy.md`：新任务先召回；出现已确认的长期偏好、项目约束、重要决策或可复用经验时，主动提炼并用 Mem0 保存，无需等待用户提醒。使用 `memory_search` / `memory_add`，写入 `infer=false`；没有新事实时不保存。用户已授权这项记忆策略；用户明确要求不记录时遵从。
<!-- MEM0_ACTIVE_MEMORY_END -->

## 项目技能索引

<!-- SKILLS:START -->
- （仓库内暂无 SKILL.md）
<!-- SKILLS:END -->

## 模块文档索引

<!-- MODULES:START -->
- [系统架构](docs/agents-dot-md/architecture.md) — CLI、连接配置、数据库会话和交互输入的边界；修改结构与依赖前阅读。
- [代码清单](docs/agents-dot-md/code-checklist.md) — 写完代码逐条检查的 CLI、数据库与 Go 实现要求。
- [编码约定](docs/agents-dot-md/coding-guidelines.md) — 扩展 CLI 参数、错误处理和输出格式时遵循的项目契约。
- [开发环境与交付](docs/agents-dot-md/environment.md) — 本地配置、测试数据库、构建和 Git 操作的边界。
- [技术栈与验证](docs/agents-dot-md/tech-stack.md) — Go CLI 的技术选型、数据访问约定和验证命令；写代码前阅读。
- [语言约定](docs/agents-dot-md/translation.md) — 中文交流与文档；代码注释、CLI 输出和提交信息保持英文。
<!-- MODULES:END -->

## 记忆索引

<!-- MEMORY:START -->
- [终端编辑器经验](docs/memory/terminal-editor.md) — reeflective/readline 的实时候选与中断行为，需要用实际键盘序列验证。
<!-- MEMORY:END -->
