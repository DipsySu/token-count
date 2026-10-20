# tcount

本机 AI agent 的 token 用量。Go 写的单文件二进制，默认进 TUI（像 `htop` / `k9s` 那种），管道或 `--json` / `--plain` 时当普通命令用。

```
totals = input + cache read + cache write + output
calls  = 上游 API 请求数（不是对话轮数）
```

缓存按实际处理量算，不是账单。

## 去重

agent 会把同一次调用写好几遍，tcount 全局去重，每次上游调用只计一次。结果是确定的：同一批日志，每次运行、无论扫描顺序，算出的数都一样。

- **Claude Code** 在 `--resume` 或会话分叉时会把历史消息**复制**进新的 jsonl；流式响应还会先写几份只带部分 output 的快照。tcount 按 `message.id` + `requestId` 去重，保留最大（即完整）的那一份。重复的往往比真的还多，不去重 Claude 的用量可能虚高数倍。子 agent（sidechain）有独立 `message.id`，正常计入。
- **Codex** 的 `token_count` 事件会在限流刷新、上下文压缩、进程恢复时原样再发一遍，累计值没变的那条跳过，每次交换的花费取 `last_token_usage`。新版 Codex 的日志带 `token_usage_record` 行：每行就是一次上游调用，按 `response_id` 去重；同一文件里第一条 record 之后的 `token_count` 不再计入，只有它之前的部分（老版本写的）按事件算。子 agent 的线程单独计入；拷到别处的日志（换机器、多个目录）只算一次。
- **Grok** 的 `turn_completed` 按会话 + 轮次（+ 模型）去重，**Gemini** 按会话文件和消息序号，**OpenCode** 按消息 id。

已知取舍（与 ccusage 一致）：

- Codex 的交换按「时间戳 + 模型 + 各项 token」去重，所以两个真不同的会话若在同一毫秒、同一模型、各项 token 完全相同，会被合并成一条。测试 `TestCodexIdenticalExchangesInDifferentSessionsMerge` 钉住了这一行为。
- Codex 子 agent 的日志开头会重放父会话历史。tcount 把开头相邻间隔不超过 1 秒的那一串 `token_count` 当作重放丢掉；如果子 agent 自己的前两次调用真在 1 秒内，会被少算。目前只用夹具验证过，等有真实子 agent 日志再复核。

`--json` 输出里的 `deduped` 说明丢掉了多少，统计的是整次加载，不限于当前窗口。

## 安装

```bash
git clone https://github.com/DipsySu/token-count.git && cd token-count
make install
```

装到 `~/.local/bin/tcount`，并链一个 `token-count`。

### 别名随你定

真正的二进制只有一个 `tcount`，其他名字都是指向它的符号链接。想叫什么自己加：

```bash
make install ALIASES="token-count tokenSummary tsum"
make install ALIASES=                # 只装 tcount，不要别名
make aliases                         # 看看现在装了哪些名字
make uninstall                       # 连别名一起清掉
```

**程序会按你调用它的名字自称** —— 帮助文本、报错信息、TUI 标题栏、`--plain` 表头全都跟着变，所以别名不是二等公民：

```
$ tokenSummary help
tokenSummary — local AI agent token usage

USAGE
  tokenSummary [span] [view] [options]
  tokenSummary week projects   ==   tokenSummary projects week
```

用符号链接而不是 shell alias，是因为 shell alias 在脚本、非交互 shell、被别的工具调用时都不生效。

`make uninstall` 会扫 `BINDIR` 里所有指向 `tcount` 的符号链接一并删掉，所以就算你忘了当初装了哪些别名也能清干净，且不会碰到别的软链。

驼峰名（`tokenSummary`）在 macOS / Windows 这种大小写不敏感的文件系统上，和 `tokensummary` 是同一个文件；如果你跨机器用，建议顺手把小写那个也加进 `ALIASES`。

## 用法

跨度和视图是两个独立的轴，命令行里也一样，顺序随便：

```bash
tcount                     # TUI
tcount day                 # 打开就停在按天
tcount projects            # 哪个项目烧的最多（不指定跨度＝全部历史）
tcount week projects       # 上 8 周的项目排行
tcount projects week       # 同上
tcount month models        # 近 12 个月的模型排行
tcount day hours           # 近 14 天的时段热力图
tcount agents              # 看看扫到了哪些目录
tcount --plain             # 不要 TUI，直接打表
tcount --json              # 给脚本用（heat 视图会带 7×24 网格）
tcount week -a claude,grok
tcount month --by-model --since 2026-01-01
```

`--plain` 打印**全部**行，不受终端高度限制；表头写明跨度、视图、窗口首末日期、UTC 偏移和过滤条件（`-a` 选的 agent、`--by-model`），卡片那行标着 `overview`。热力图（`tcount day hours`）用同样的表头。

`--json` 的字段：

| 字段 | 含义 |
| --- | --- |
| `since` / `until` | 实际窗口的本地首末日期（跨度推出来的或 `--since/--until` 给的）；无界的一侧省略 |
| `utc_offset` | 生成报告时的本地 UTC 偏移 |
| `rows` / `records` / `calls` | 都只覆盖这个窗口 |
| `heat` | 只有 hours 视图才有；`heat.total` 等于各行之和 |
| `snapshot` | 今天 / 本周 / 本月 / 今年的卡片，外加 `all`（整次加载的合计）；不受跨度限制，但受 `--since/--until` 限制 |
| `deduped` | 去重丢掉的记录和 token，统计整次加载 |
| `complete` | 总有；为 `false` 时有日志没能读全，所有数字都是下限 |
| `issues` | 没读全的文件或目录（`agent` / `path` / `error`）；全部读全时省略 |

有日志（文件或目录）读不全（权限、损坏、超长行、崩溃留下的坏行、目录列不出来）时，三种输出都会说：TUI 有一行 ⚠ 警告，`--plain` 和热力图在表后列出前 5 个（`agent  path: error`）并写明还有几个，`--json` 如上。

TUI 快捷键：

**两个轴，两套键**：`←` `→` 走时间跨度，`Tab` 走视图。两者自由组合，比如「上周的项目排行」。顶部 tab 栏分两组，每组恒有一个亮着，告诉你当前落在哪个组合上。

| 键 | 作用 |
| --- | --- |
| `1`–`5` `0` / `←` `→` | 跨度：today / day / week / month / year / all |
| `Tab` / `Shift+Tab` | 视图：time / project / model / hours |
| `t` `p` `M` `H` | 直接跳到某个视图 |
| `enter` | 展开当前行（按 agent 拆分） |
| `E` `C` | 全部展开 / 全部收起 |
| `a` / `A` | 循环 agent 过滤 / 全部 |
| `m` | 按模型拆开 |
| `s` | 循环排序（默认 / 总量 / 调用数 / 均次） |
| `/` | 搜索 agent、模型、项目，边打边筛 |
| `d` | 详情面板 |
| `c` | 显示/隐藏小猫 |
| `j` `k` / `g` `G` | 移动 / 首尾 |
| `r` | 重扫 |
| `?` | 帮助 |
| `q` | 退出 |
| `ctrl+c` | 退出，任何状态下都有效（搜索框、帮助、详情、扫描中） |

**搜索**：不分大小写，同时匹配原始值和表里显示的形式，所以 `~/work` 能搜到家目录下的项目，短模型名也行。中文输入法和粘贴的文本都能用（换行、Tab 变成空格），退格删一个字符。搜索框打开时字母都是文字，不会触发快捷键；`enter` 保留搜索回到表格，`esc` 清空。

**选中行跟着走**：排序、换 agent、按模型拆、搜索、重扫、全部展开/收起之后，光标仍停在原来那一行（行没了就停在它的上级）；换跨度或视图才回到顶部。

## 界面

- **范围行**（tab 栏下面那行）写明表格覆盖什么：首末日期和天数，如 `2026-10-06 → 2026-10-19 · 14 days`、`2026-10-19 · today`；起点无界时用范围内最早一条记录，如 `since 2026-04-30 → 2026-10-19`，没有记录则是 `all history → 2026-10-19`。后面是 UTC 偏移和生效的过滤：agent（带颜色；用 `-a` 只加载了部分 agent 时列出它们）、by model、排序、搜索词。放不下时先丢天数，再丢偏移、年份，然后是整段日期，过滤条件留到最后（搜索词最优先，太长就截断）。搜索时这一行就是输入框，旁边写着匹配了多少条。
- **OVERVIEW 卡片**上方有一行说明：卡片只受 agent 过滤（`a` 键、`-a`）和 `--since/--until` 影响，不受跨度和搜索影响，免得被当成搜索结果。卡片的涨跌是同口径比较：今天 vs 昨天同一时刻为止、本周 vs 上周同一星期几同一时刻、本月 vs 上月同一天同一时刻。
- 表格最后一行是 **RANGE TOTAL**（名称列太窄时写 TOTAL），即当前范围的合计。
- 有日志（文件或目录）没读全时，卡片下面有一行 ⚠ 警告，说总数是下限；它比去重提示优先。
- 终端矮的时候按固定顺序让位，让表格尽量有 5 行（不足 5 行就全部；最矮的 10 行终端里固定的行占去 6 行，表格只剩 4 行）：先是小猫和卡片，再是空行和分隔线，然后去重提示、详情面板，⚠ 警告最后。标题、tab 栏、范围行、表头、合计行、底栏始终都在。tab 栏太窄时先去掉未选中 tab 的留白，再只显示当前的跨度和视图（`‹ day › ←→   ‹ project › tab`），不会把标签截成半个。
- 终端至少要 30×10，再小会提示 `terminal too small`。
- TUI 开着过了午夜会自动切到新的一天。

## 观感

标题、卡片数字、进度条、趋势线、热力图都用 [go-colorful](https://github.com/lucasb-eyer/go-colorful) 在 Luv 空间做感知均匀的渐变，冷色到暖色。

进度条的渐变**采样整列宽度而不是填充部分**，所以短条只走到冷色、满条才到暖色 —— 长度和色温说的是同一件事。如果按填充长度采样，每根条都会以同样的暖色收尾，颜色就没有信息量了。

左边那只猫按「今天用量 ÷ 历史单日峰值」换表情，tab 栏右边会写出档位：

```
   /\_/\        /\_/\        /\_/\        /\_/\
  ( -ω- ) z   ( •ω• )     ( OωO )     ( @ω@ )
  |  ░  |     |  ░  |~    |  ░  |~    |  ░  |~
  \_____/     \_____/     \_____/     \_____/
   napping     content      busy        wired
  0 / <15%      <50%        <85%        ≥85%
```

轮廓是闭合的：头的括号、身体的竖线、底部的斜杠都落在同一两列上，所以左右边缘连成一条线。睡觉时尾巴收起来、加个 `z`。

扫描时它会眨眼（`•ω•` → `-ω-` → `^ω^`）、甩尾巴；没数据时它蜷着睡，旁边写清楚是真没数据还是被过滤器挡了。

**用字是挑过的。** 只用 DejaVu Sans Mono、Ubuntu Mono、Ubuntu Sans Mono 三家都有字形的字符 —— `●`／`◕` 当眼睛更好看，但 Ubuntu Mono 里是豆腐块，比朴素字形难看得多。`ω`（日式猫嘴）三家都有，所以用它。

猫只在终端够宽（≥84 列）且高度放得下卡片时出现，且**从不影响表格的列宽** —— 关掉它只会让卡片重排，不会改动数据区。

关掉这些效果：

| 方式 | 效果 |
| --- | --- |
| `c` | TUI 里临时隐藏小猫 |
| `--no-cat` | 启动就不要小猫 |
| `NO_COLOR=1` | 关掉全部颜色（标准约定） |
| `TCOUNT_NO_FANCY=1` | 只关渐变和小猫，保留基础配色 |
| `CLICOLOR_FORCE=1` | 反过来：管道里也强制上色，给 `\| less -R` 和截图用 |

`TERM=dumb`、非彩色终端、或颜色档不足 256 色时自动降级。浅色背景有独立的一套调色。

## 视图

- **时间**（默认）：一天/周/月/年一行主行，`enter` 展开成各 agent。
- **项目**：按工作目录排行。Claude 读 jsonl 里的 `cwd`，Codex 读 `session_meta.cwd`，Grok 从会话目录名 URL 解码。
- **模型**：按模型排行，含均次消耗。
- **热力图**：星期 × 小时的用量分布。和别的视图一样**遵守当前跨度** —— `today hours` 只有一行，`all hours` 才是完整分布。

## 数据从哪来

| Agent | 路径 | 覆盖环境变量 |
| --- | --- | --- |
| Claude Code | `~/.claude/projects/**/*.jsonl` 和 `$XDG_CONFIG_HOME/claude/projects/**/*.jsonl`（默认 `~/.config/claude`） | `CLAUDE_CONFIG_DIR` |
| Codex | `~/.codex/sessions/**/*.jsonl` | `CODEX_HOME` |
| Grok | `~/.grok/sessions/**/updates.jsonl` | `GROK_HOME` |
| Gemini CLI | `~/.gemini/**/chats/*.json` | `GEMINI_DIR` |
| OpenCode | `~/.local/share/opencode/storage/message/**/*.json` | `OPENCODE_DATA` / `XDG_DATA_HOME` |

解析结果缓存在 `~/.cache/token-count/index-v7.gob`（`XDG_CACHE_HOME` 可改位置），按文件 mtime + 大小失效；没读全的文件不进缓存，下次重读。日志中间有解析不了的行（比如崩溃留下的一串 NUL）算作没读全；正在写的日志最后一行写到一半不算。TUI 里按 `r` 重新扫描（没变的文件仍走缓存）；`--no-cache` 既不读也不写缓存。缓存格式升级后，旧版本留下的 `index-v*.gob` 不会自动清理，可以手动删掉。

## 开发

```bash
make build
make test
```

## 许可

[0BSD](LICENSE)：随便用、改、分发、商用，不需要保留署名。
