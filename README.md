# tcount

本机 AI agent 的 token 用量。Go 写的单文件二进制，默认进 TUI（像 `htop` / `k9s` 那种），管道或 `--json` / `--plain` 时当普通命令用。

```
totals = input + cache read + cache write + output
calls  = 上游 API 请求数（不是对话轮数）
```

缓存按实际处理量算，不是账单。

## 去重

Claude Code 在 `--resume` 或会话分叉时会把历史消息**复制**进新的 jsonl，同一条 API 响应因此被写入多次。tcount 按 `message.id` + `requestId` 全局去重，只计一次。

本机实测：7,352 条 assistant 记录里只有 2,436 条是真的，不去重会让 Claude 的用量虚高约 **3 倍**。`--json` 输出里的 `deduped` 字段会说明丢掉了多少。

Codex 用的是 `last_token_usage`（增量），Grok 的 `turn_completed` 也是每轮增量，两者都不需要去重。子 agent（sidechain）有独立 `message.id`，正常计入。

## 安装

```bash
cd ~/tool-lib/token_count
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
| `/` | 搜索 agent、模型、项目 |
| `d` | 详情面板 |
| `c` | 显示/隐藏小猫 |
| `j` `k` / `g` `G` | 移动 / 首尾 |
| `r` | 重扫 |
| `?` | 帮助 |
| `q` | 退出 |

## 观感

标题、卡片数字、进度条、趋势线、热力图都用 [go-colorful](https://github.com/lucasb-eyer/go-colorful) 在 Luv 空间做感知均匀的渐变，冷色到暖色。

进度条的渐变**采样整列宽度而不是填充部分**，所以短条只走到冷色、满条才到暖色 —— 长度和色温说的是同一件事。如果按填充长度采样，每根条都会以同样的暖色收尾，颜色就没有信息量了。

左边那只猫按「今天用量 ÷ 历史单日峰值」换表情，右上角会写出档位：

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
| Claude Code | `~/.claude/projects/**/*.jsonl` | `CLAUDE_CONFIG_DIR` |
| Codex | `~/.codex/sessions/**/*.jsonl` | `CODEX_HOME` |
| Grok | `~/.grok/sessions/**/updates.jsonl` | `GROK_HOME` |
| Gemini CLI | `~/.gemini/tmp/**/chats/*.json` | `GEMINI_DIR` |
| OpenCode | `~/.local/share/opencode/storage/message/**/*.json` | `OPENCODE_DATA` / `XDG_DATA_HOME` |

解析结果缓存在 `~/.cache/token-count/`（按文件 mtime + 大小失效）。强制重扫：`--no-cache` 或 TUI 里按 `r`。

## 开发

```bash
make build
make test
```
