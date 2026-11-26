# VPSGoGo 规格说明（v0.1 草案）

> 目标：用户买完 VPS，只需在 App 里粘贴 **IP + root 密码**，剩下的事（加固、监控、续费提醒、装服务）由 VPSGoGo 完成。

状态：草案 v0.2。开源客户端，AI 采用 BYOK（用户自带 Anthropic API Key）。

---

## 1. 产品定义

| 项目 | 决定 |
|---|---|
| 形态 | 本地桌面 App（[egoist/mygo](https://github.com/egoist/mygo)，Web 前端 + Go 后端，非 cgo），通过 SSH 管理 VPS |
| VPS 端 | 轻量 `gogo-agent`（Go 单二进制，systemd 服务），负责采集指标与**电脑关机时**的告警/续费推送 |
| 装服务 | AI Agent Skill：内置 `SKILL.md` 技能包，由 Claude 在用户审批下通过 SSH 执行 |
| 开源与 AI 计费 | 客户端与 agent 全部开源（MIT，与 mygo 一致；M0 时确认）；AI 为 BYOK，项目方不运营任何服务端、不经手用户数据或费用 |
| 目标用户 | 会买 VPS、但不想学 Linux 运维的个人用户 |
| 首发平台 | 桌面：macOS（先），Windows / Linux 随后；VPS：Debian 12/13、Ubuntu 22.04/24.04，amd64 / arm64 |

### 1.1 核心用户旅程（衡量标准）

1. 打开 App → 「添加服务器」→ 填 IP、root 密码（或私钥）、到期日、价格。
2. App 确认主机指纹 → 自动加固 → 安装 agent → 显示仪表盘。**目标：≤ 3 分钟，用户零命令行。**
3. 进入「技能」→ 选「Uptime Kuma」→ 填域名 → 审批计划 → 完成后给出可访问的 HTTPS 链接。**目标：≤ 10 分钟。**
4. 到期前 7/3/1 天，即使电脑关机，也能在 Telegram / 邮件 / Webhook 收到续费提醒。

### 1.2 不做（v1 范围外）

- 调用云厂商 API 自动购买、续费、扣款；读取厂商账单（到期日由用户手填）。
- 多用户 / 团队 / 权限；Web 版面板；手机 App。
- 项目方托管的 AI 代理、账号体系、订阅计费（AI 只走 BYOK）。
- Windows Server、CentOS/RHEL 系、Alpine 等非 Debian 系 VPS。
- Kubernetes、集群编排；agent 对外开放任何监听端口。
- 用户自写技能的在线市场（v1 只支持本地目录导入）。

---

## 2. 架构

```
┌──────────────── 桌面 App (mygo) ────────────────┐
│  前端 (Vite + React, 像素风)                     │
│     │ mygo 生成的 TS client (Bind / Event / Channel)
│  Go 服务层 (apps/desktop/services) —— 仅做传输适配 │
│     │                                           │
│  领域核心 packages/core（纯 Go，可 go test）      │
│   fleet · provisioning · monitoring · renewal    │
│   skills · notify · audit · sshx · vault         │
│     │ SSH (host key 已固定, ed25519)              │
└─────┼───────────────────────────────────────────┘
      ▼
┌──────────── VPS ────────────┐      出站 HTTPS
│ gogo-agent (systemd)        │ ───────────────► Telegram / SMTP / Webhook / ntfy
│  采集 → 本地环形缓冲          │ ───────────────► 心跳 URL (Healthchecks 兼容, 可选)
│  阈值告警 / 续费提醒          │
│  不监听任何端口              │
└─────────────────────────────┘
      ▲
Claude API（技能执行时由桌面 App 调用；VPS 不接触 API Key）
```

### 2.1 仓库结构（monorepo：单 Go module + Bun）

```
vpsgogo/
  go.mod              # 单一 Go module（见下）
  scripts/verify.sh   # verify.sh M<n>：跑该里程碑全部自动判定
  apps/
    desktop/            # mygo 项目 (bunx mygo-cli init 生成后改造)
      main.go           # mygo.Bind 各服务、App.Run
      services/         # 绑定层：参数校验 + 调 core，无业务逻辑
      mygo.config.ts
      src/              # 前端，FSD：app/ pages/ widgets/ features/ entities/ shared/
      resources/
    agent/              # gogo-agent，cmd + internal
                        # scripts/build-agent.sh 交叉编译 linux/amd64+arm64 → apps/desktop/embed/agent/，再 go:embed
  packages/
    core/               # 领域核心（DDD 限界上下文，见 §3）
    skills/             # 内置技能包（SKILL.md + 资源），go:embed 进桌面 App
  test/
    e2e/                # lima VM / docker sshd 目标机
  docs/
    SPEC.md  CONTEXT.md  adr/
```

Go：仓库根一个 module（`github.com/bubbleptr/vpsgogo`），所以 `go build ./...`、`go test ./...` 在根目录直接可用，无需 go.work。M0 须验证 `mygo dev/build` 能在子目录 `apps/desktop` 中使用父级 go.mod；不行再改为 go.work + 每个 module 单独命令，并更新 `scripts/verify.sh`。

### 2.2 关键技术选择

| 关注点 | 选择 | 理由 |
|---|---|---|
| 桌面框架 | mygo web 模板（Vite + TS），前端用 React + TanStack Router/Query | 像素风需要自定义字体与 CSS，web 窗口最灵活 |
| 本地数据 | mygo `sqlite` 插件（无 cgo）——Go 侧 API 未读文档，M0 验证 | 服务器、续费、审计日志 |
| 机密 | `mygo.Secrets`（系统钥匙串）——API 未读文档，M0 验证 | SSH 私钥、Claude API Key、通知渠道 token |
| SSH | `golang.org/x/crypto/ssh` | 纯 Go；自管 known_hosts |
| LLM | Anthropic Go SDK，Messages API + Tool Runner，在**桌面端本地**跑循环；模型 `claude-opus-5-5`，开启 `fallbacks: "default"` | 工具要用本机钥匙串中的 SSH 私钥执行，循环必须在本地 |
| agent 分发 | linux/amd64 + linux/arm64 二进制 `go:embed` 进桌面 App，经 SSH 上传并校验 sha256 | 不从公网下载，供应链最短 |
| 自动更新 | mygo `updater` 插件，Ed25519 签名（`mygo keygen`） | 技能包随 App 更新一起签名发布 |

---

## 3. 领域模型（packages/core 限界上下文）

| 上下文 | 职责 | 核心概念 |
|---|---|---|
| `fleet` | 服务器档案 | `Server{ID, Name, Host, Port, HostKeyFP, OS, Arch, Provider, Tags}` |
| `vault` | 机密存取抽象（生产用 mygo.Secrets，测试用内存实现） | `SecretRef` |
| `sshx` | SSH 会话、host key 固定、命令执行与流式输出 | `Session`, `ExecResult{Stdout, Stderr, Code}` |
| `provisioning` | 首次接入与加固流水线（幂等步骤） | `Step`, `Pipeline`, `StepResult` |
| `monitoring` | 指标查询、实时流、阈值规则 | `Sample{CPU, Mem, Disk, NetRx, NetTx, Load, Uptime}`, `AlertRule` |
| `renewal` | 到期日、周期、价格、提醒计划 | `Subscription{ServerID, ExpiresAt, Cycle, Price, Currency, RenewURL}`, `ReminderSchedule` |
| `notify` | 通知渠道与发送 | `Channel{Telegram/SMTP/Webhook/Ntfy}`, `Message` |
| `skills` | 技能加载、计划、审批、执行、审计 | `Skill`, `Run`, `ProposedCommand`, `Approval` |
| `audit` | 只追加的操作日志（谁、何时、在哪台机器、执行了什么、结果） | `Entry` |

规则归属：业务规则只在 `packages/core`；`apps/desktop/services` 只做 DTO 转换；`apps/agent` 复用 `monitoring`/`renewal`/`notify` 的纯逻辑部分。词汇表写进 `docs/CONTEXT.md`。

---

## 4. 功能规格

### 4.1 添加服务器与自动加固（provisioning）

输入：Host、Port(默认 22)、root 密码 **或** 私钥、可选：名称、厂商、到期日、价格。

流水线（每步幂等、可重试、结果写入审计日志）：

1. **连接 & 指纹确认**：首次连接展示 SHA256 指纹，用户确认后固定；之后指纹变化 → 拒绝连接并显著告警。
2. **探测**：`/etc/os-release`、架构、内存、磁盘；不支持的系统直接终止并说明原因。
3. **生成密钥**：本地生成 ed25519 密钥对，私钥入钥匙串。
4. **创建运维用户** `gogo`：写入公钥，`sudo` 免密（仅该用户，且仅密钥登录）。
5. **SSH 加固**（写 `/etc/ssh/sshd_config.d/10-vpsgogo.conf`）：`PasswordAuthentication no`、`PermitRootLogin no`、`KbdInteractiveAuthentication no`。**先用新密钥以 `gogo` 登录验证成功，再 reload sshd**；失败则回滚该文件。
6. **基础安全**：`unattended-upgrades`、防火墙（ufw：仅放行 SSH 端口；技能按需开放端口）、`fail2ban`（sshd jail）。
7. **安装 agent**：上传二进制 → 校验 sha256 → 创建系统用户 `gogo-agent`（无 shell、无 sudo）→ systemd unit（`NoNewPrivileges`, `ProtectSystem=strict`, `ProtectHome`, `PrivateTmp`）→ 写入配置 → 启动 → 健康检查。
8. root 密码在内存中用完即弃，**永不落盘、不进日志**。

可选开关（高级）：改 SSH 端口、保留 root 密钥登录。

### 4.2 监控（monitoring + gogo-agent）

- agent 用 systemd `StateDirectory=gogo-agent`（`/var/lib/gogo-agent`，0750，组 `gogo-agent`）与 `RuntimeDirectory=gogo-agent`（unix socket，0660）。`gogo` 用户加入 `gogo-agent` 组，以只读方式读取数据、连接 socket；`gogo-agent query/stream` 子命令就是这个客户端。
- agent 每 10s 采集：CPU%、内存、交换、各挂载点磁盘、网络收发速率、load、uptime、TCP 连接数；保存最近 7 天（降采样：10s×24h，1min×7d）于 `/var/lib/gogo-agent/`。
- 桌面端通过 SSH 执行 `gogo-agent query --since <t> --json` 拉历史；`gogo-agent stream` 走 SSH 通道实时推送 → mygo `Channel` → 前端图表。
- 告警规则（默认值可改）：CPU > 90% 持续 5 分钟、内存 > 90%、磁盘 > 85%、被监视的 systemd unit 非 active、被监视的 HTTP(S) 地址探测失败（技能安装后自动把 `verify` 地址登记为探测目标）。agent 不访问 Docker socket；容器明细只由桌面端经 SSH（`gogo` + sudo）查询。告警有冷却期（默认 1h），恢复时发「已恢复」。
- 离线检测：VPS 宕机无法自报。v1 方案：① 桌面 App 打开时轮询，失败 3 次即提醒；② agent 定时 ping 用户配置的心跳 URL（Healthchecks.io 兼容），由外部服务负责「失联」告警。
- 仪表盘：所有服务器卡片（在线状态、CPU/内存/磁盘条、到期天数）；详情页：实时曲线 + 24h/7d 历史 + 运行中的服务列表。

### 4.3 续费提醒（renewal）

- 每台服务器一条 `Subscription`：到期日、周期（月/季/年/一次性）、价格+币种、续费链接、提前提醒天数（默认 7/3/1/0）。
- 桌面端：系统通知 + 仪表盘红色倒计时；续费日历视图；月度/年度总花费统计。
- agent 端：桌面端每次同步时把订阅信息写入 agent 配置；agent 每天检查一次，按计划通过通知渠道发送——**电脑关机也能收到**。
- 用户点「已续费」→ 到期日按周期顺延，并同步到 agent。

### 4.4 通知渠道（notify）

v1 支持：Telegram Bot、SMTP 邮件、通用 Webhook（JSON POST）、ntfy。每个渠道有「发送测试消息」按钮。渠道凭据在本机存钥匙串；为了关机时也能告警，**必须复制一份到 VPS**：`/etc/gogo-agent/notify.json`（`root:gogo-agent`，0640）。残余风险：VPS 被攻破会泄露这些 token——UI 建议为 VPSGoGo 单独建一个 Bot / Webhook，并支持一键在所有 VPS 上轮换。

### 4.5 AI 技能（skills）

#### 技能包格式

```
packages/skills/uptime-kuma/
  SKILL.md        # frontmatter + 给 Claude 的执行说明
  files/          # 可选：compose 模板、配置模板
```

```yaml
---
name: uptime-kuma
title: Uptime Kuma 监控面板
description: 自托管的网站可用性监控，带 HTTPS 访问
version: 1
requires: [docker, caddy]           # 依赖其他技能，自动先装
inputs:
  - { key: domain, label: 域名, type: domain, required: true }
ports: [443]                        # 需要在防火墙放行的端口（只能是 Caddy 的 80/443 或非 HTTP 服务自身端口）
os: [debian>=12, ubuntu>=22.04]
verify: "curl -fsS https://{{domain}} -o /dev/null"   # 完成判定
---
```

约束：容器端口一律绑定 `127.0.0.1`，由 Caddy 对外（Docker 发布的端口会绕过 ufw）。

无域名时：默认使用 `<ip-dashed>.sslip.io` 作为域名，Caddy 仍能签发 HTTPS 证书。

v1 内置技能：`docker`、`caddy`（反代 + 自动 HTTPS）、`uptime-kuma`、`vaultwarden`、`wg-easy`（WireGuard）、`static-site`（上传本地目录并托管）。用户可从本地目录导入自定义技能（带醒目「未经审核」标记）。

#### 执行模型

1. 用户选技能、填 inputs → 预检（系统版本、内存、端口占用、DNS 是否解析到该 VPS）。
2. Claude 读取 SKILL.md + 服务器事实，输出**结构化执行计划**（步骤列表 + 每步命令）。
3. 用户在 UI 审批计划（可逐条查看命令）。
4. Tool Runner 循环执行；Claude 可用工具：
   - `exec(command, reason)`：经 SSH 以 `gogo` 执行，流式输出到 UI。
   - `write_file(path, content, mode)`、`read_file(path)`。
   - `open_port(port, proto)`：走受控的 ufw 操作。
   - `ask_user(question)`：需要用户输入时暂停。
   - `finish(summary, urls)`：结束并跑 `verify`。
5. **审批策略**（真正的安全边界）：只有与已批准计划中某条命令**逐字一致**的命令才自动执行；`write_file` 的路径+内容也属于计划的一部分，按同样规则比对。其余一律暂停等待用户点「允许」，守卫（§5.3）再额外拦截。
6. 每条命令、输出摘要、审批人、结果写入审计日志；运行结束展示总结、访问地址、占用端口、回滚提示。

模型设置：`claude-opus-5-5`，自适应思考，`effort: medium`（可在设置改），流式，开启 server-side fallback（`fallbacks: "default"`）。API Key 由用户在设置页填入，存钥匙串。估算并显示每次运行的 token 花费。

---

## 5. 安全模型

### 5.1 威胁与对策

| 威胁 | 对策 |
|---|---|
| 中间人冒充 VPS | 首次指纹确认 + 固定；变化即拒连 |
| 本地凭据泄露 | SSH 私钥、API Key 只存系统钥匙串；渠道 token 本机存钥匙串，另有一份在 VPS（§4.4 残余风险）；SQLite 不含机密；日志脱敏 |
| 数据发往 Claude API | 技能运行时，命令输出、读取的文件内容会发送给 Anthropic。首次使用技能前明示并需同意；发送前脱敏：已知机密值、私钥块、`*_KEY=`/`*_TOKEN=`/`password=` 形式的值替换为 `[REDACTED]`（有测试） |
| root 密码被保存 | 只在内存中使用一次，结构体用完清零，禁止写日志（有测试保证） |
| 弱 SSH 配置 | 关闭密码登录与 root 登录；fail2ban；自动安全更新 |
| agent 被利用 | 不监听端口；独立无特权用户；systemd 沙箱；只出站 HTTPS |
| agent 二进制被替换 | 随 App 签名发布，上传后校验 sha256 |
| LLM 执行危险命令 / 提示注入 | 见 §5.3；工具输出（网页、文件内容、命令输出）一律视为数据 |
| 技能包被篡改 | 内置技能随 App 签名更新；第三方技能标记「未审核」且每条命令都需审批 |
| 更新包被篡改 | mygo updater Ed25519 签名校验 |

### 5.2 权限边界

- 桌面端 → VPS：只用 `gogo` 用户 + ed25519 密钥。
- `gogo-agent` 进程：无 sudo、无登录 shell、不在 docker 组；只读 `/proc`、`/sys`、`statfs`、`systemctl is-active`，出站 HTTPS。
- VPS 永远拿不到 Claude API Key。

### 5.3 技能命令守卫（`skills/guard`）

定位：**纵深防御，不是安全边界**。基于字符串的 shell 拦截可被 `bash -c`、先写脚本再执行等方式绕过；真正的边界是 §4.5 的逐字审批。守卫同时作用于 `exec` 命令和 `write_file` 的路径/内容。

- **硬拒绝**（不可审批放行）：`rm -rf /`、`mkfs`、`dd of=/dev/sd*`、修改 `sshd_config` 中 `PasswordAuthentication yes`、删除 `gogo` 用户或其 authorized_keys、`chmod -R 777 /` 等。
- **需单独审批**：任何计划外命令；`curl|sh` / `wget|bash`（展示目标 URL）；改防火墙；`systemctl stop/disable` 非本技能服务；写 `/etc` 下非本技能文件。
- 守卫是纯函数 `Classify(cmd) → Allow | NeedApproval | Deny`，有表驱动测试覆盖。

---

## 6. 安装与分发

- macOS：DMG（签名 + 公证）；Windows：NSIS 安装器；Linux：deb / rpm / PKGBUILD / 带 `install.sh` 的归档（均由 `mygo build` 产出）。
- 首次启动向导：语言（中/英）→ 通知渠道（可跳过）→ Claude API Key（可跳过，跳过则技能页提示）→ 添加第一台服务器。
- 自动更新：GitHub Releases 源，增量更新。

---

## 7. 质量与测试

| 层 | 方法 | 命令 |
|---|---|---|
| 领域核心 | 单元测试，表驱动；`vault`/`sshx` 有内存/假实现 | `go test ./packages/... ./apps/agent/...` |
| SSH 集成 | docker 起 Debian 12 / Ubuntu 24.04 sshd 容器（colima） | `go test -tags integration ./test/e2e/...` |
| 完整加固 + systemd | lima VM（Debian 12），真实 systemd；`test/e2e/lima/fresh-vps.yaml` 用 cloud-init 开启 root 密码登录、不放任何密钥，模拟刚买到的 VPS；每次测试前 `limactl` 从快照恢复 | `go test -tags vm ./test/e2e/...` |
| 技能执行 | 录制的 Claude 响应 fixture 回放（CI 不调真 API）；守卫表驱动测试 | `go test ./packages/core/skills/...` |
| 前端 | Vite dev server，把生成的 `src/mygo.ts` 别名到 mock 实现；Vitest + Playwright 截图 | `bun run test`、`bun run test:e2e` |
| 构建 | 能产出 macOS 包 | `cd apps/desktop && bun run build` |

前端 mock 绑定是推断方案（mygo 文档未提供官方 mock），M0 需验证可行。

---

## 8. 里程碑（供 `/goal` 逐个执行）

规则：
- 每个里程碑：先写失败测试 → 实现 → `scripts/verify.sh M<n>` 退出码 0 → 一个 PR。
- `verify.sh M<n>` 只包含**自动判定**（下表），并把每条命令及其输出打印到终端——`/goal` 的评估器不读文件、不跑命令，只看对话里出现的输出。
- **人工判定**不进 `/goal` 条件；里程碑在自动判定通过后标记为「待人工验收」，由用户确认。
- 前一里程碑的 `verify.sh` 必须仍然通过（回归）。

| # | 里程碑 | 自动判定（`scripts/verify.sh M<n>`） | 人工判定 |
|---|---|---|---|
| M0 | 脚手架：LICENSE、README（含 BYOK 说明）、go.mod、mygo web 模板（apps/desktop）、React + TanStack、FSD 目录、mock 绑定、`scripts/build-agent.sh`、`verify.sh`、CI；验证 §2.2 中标注「M0 验证」的 API | `go build ./...`；`go vet ./...`；`cd apps/desktop && bun run test`；`bun run build:web`（mock 绑定下纯浏览器构建）；`mygo doctor` 退出 0 | 无 |
| M1 | `sshx` + `vault` + `fleet`：连接、指纹固定、执行命令、SQLite 存储 | `go test ./packages/...`；`go test -tags integration ./test/e2e/...`：对 sshd 容器 `uname -a` 成功；篡改指纹后连接失败 | 无 |
| M2 | `provisioning` 流水线（不含 agent） | `go test -tags vm ./test/e2e/...`：对 fresh-vps VM 跑完后，密码登录被拒、root 登录被拒、`gogo` 密钥登录成功；再跑一次幂等；注入错误 sshd 配置时自动回滚且仍可登录；日志与 SQLite 中搜不到 root 密码 | 无 |
| M3 | `gogo-agent`：采集、存储、query/stream、告警、通知 | agent 单测；vm 测试：`systemctl is-active gogo-agent`=active、`ss -lntup` 中无 gogo-agent、`gogo` 执行 `gogo-agent query --json` 有最近样本；告警送达本地 webhook 捕获服务 | 无 |
| M4 | `renewal`：订阅、提醒计划、同步到 agent | 单测：当天、已过期、闰年、跨月、「已续费」顺延；vm 测试：到期=明天时 agent 的提醒送达 webhook | 无 |
| M5a | 桌面 UI 功能（占位 token）：仪表盘、添加服务器向导、详情页实时图表、续费日历、设置 | Vitest 组件测试；Playwright 在 mock 绑定下走完「添加服务器」向导与各页面，无控制台错误 | 无 |
| M5b | 视觉落地（§9）：token、`ServerBadge`、`PixelStream`、像素图表、团子/场景 SVG | Playwright 截图生成成功；token 对比度测试（正文 ≥ 4.5:1）；reduced-motion 下无动画 | 用户对照参考图验收截图；真机 macOS 添加 fresh-vps VM 全流程 |
| M6 | `skills`：加载、守卫、计划/审批（逐字比对）、Tool Runner、脱敏、审计 | 守卫表驱动测试；审批比对测试；脱敏测试；fixture 回放走完 `docker`+`caddy`（CI 不调真 API） | 设 `ANTHROPIC_API_KEY` 后在 VM 上真实安装 `uptime-kuma`，`verify` 通过 |
| M7 | 其余内置技能 + 自定义技能导入 | 每个技能 fixture 回放测试；导入技能标记「未审核」且每条命令都需审批 | 抽查 1 个技能真机安装 |
| M8 | 打包、签名、自动更新、首次启动向导 | `mygo build -platform darwin/universal -skip-notarize` 产出 DMG；更新包签名校验测试（坏签名被拒） | 签名 + 公证后的 DMG 在干净 Mac 上安装运行 |

### 8.1 推荐的 `/goal` 条件写法

每次只跑一个里程碑，例如：

```
/goal docs/SPEC.md 的里程碑 M2 完成：在 feat/m2-provisioning 分支上，`scripts/verify.sh M2` 与 `scripts/verify.sh M1` 都在本会话中运行且退出码为 0（输出已显示），且已开出 PR；不修改 docs/SPEC.md 的验收标准；或 40 轮后停止
```

### 8.2 前置条件（M0 之前由用户完成）

- [x] Go ≥ 1.27.3（已升级到 1.27.4）。
- [x] Git 仓库：github.com/bubbleptr/vpsgogo（每个里程碑以 PR 结束）。
- [x] colima 运行中（Docker 29.5.2）；lima 已安装；Bun 1.3.12。
- [ ] 以 auto 模式运行 `/goal`，否则每次工具调用都会停下等授权。

---

## 9. UI 视觉（扁平插画 + 像素粒子）

参考图：[docs/design/reference.webp](design/reference.webp)。风格不是全屏 8-bit，而是**圆润扁平插画 + 像素方块作为「数据/流动」的视觉语言**。只借鉴风格；吉祥物、场景、图标全部原创，不使用参考图中的任何第三方品牌 logo。

### 9.1 视觉语言 → 产品含义

| 参考图元素 | 在 VPSGoGo 中的含义 |
|---|---|
| 圆形徽章里的小团子吉祥物（两只椭圆白眼，各自一种颜色） | **一台 VPS**。每台服务器分配一个团子颜色 + 圆环色 |
| 徽章内的场景（金门桥、棕榈海滩、雪山、宝塔、铁塔、城市楼群） | **机房地区**：按 IP 地理位置自动选场景（美西、东南亚、日本、欧洲、香港…），用户可改 |
| 白天 / 夜晚场景 | 实时状态：白天=在线；夜晚+团子睡着=离线；团子抱时钟=临近到期；团子冒汗/变红=告警 |
| 徽章下方白底彩边胶囊里的小图标 | **已安装的服务/技能**（Docker、Caddy、Uptime Kuma…，用自绘单色图标） |
| 徽章之间流动、渐隐的彩色像素方块 | 服务器之间/服务器→服务的**数据流**；技能安装进度以像素流从团子流向服务卡片 |
| 黑色粗描边、略微倾斜的白色卡片 + 像素马赛克图 | **服务卡片 / 详情面板**；监控曲线用像素方块绘制（阶梯式，无平滑） |
| 黑底白字胶囊标签 | 卡片标题、状态标签（如「续费还剩 3 天」） |
| 卡片上的色块条 | 调色板本身，也用作服务器颜色选择器 |
| 米白底 + 浅点阵网格 + 细横线 | 全局画布背景；仪表盘可自由拖拽排列徽章（类白板） |

### 9.2 设计 token（`apps/desktop/src/shared/ui/tokens.css`，组件禁止硬编码颜色）

取色为从参考图估计的初值，M5b 用 OKLCH 校准对比度：

| token | 值（初值） | 用途 |
|---|---|---|
| `--canvas` | `#F7F5F0` | 背景 |
| `--canvas-dot` | `#E4E0D8` | 点阵网格 |
| `--ink` | `#161616` | 粗描边、黑胶囊、正文 |
| `--paper` | `#FFFFFF` | 卡片、胶囊底 |
| `--coral` | `#E8664A` | 危险 / 告警 |
| `--amber` | `#F2B63D` | 警告 / 临近到期 |
| `--teal` | `#1FA7B8` | 主色 / 链接 |
| `--green` | `#2FA36B` | 在线 / 成功 |
| `--cream` | `#ECE8DF` | 次级填充 |
| 团子色 ×10 | 天蓝 `#4A9BF0`、湖蓝 `#22A6C3`、森绿 `#2E9E6A`、草绿 `#9BC63B`、粉 `#EE6AA7`、紫 `#7B5CE0`、宝蓝 `#2D55D6`、橙 `#F28A3C`、黄 `#F0B93A`、珊瑚 `#E25A44` | 每台服务器一个；圆环用同色 |

暗色模式：画布换深墨蓝（参考图夜景色 `#1E2440` 一档），描边改为 `--paper`，团子色保持不变。

### 9.3 组件与规则

- **字体**：界面文字用圆润无衬线（中文 HarmonyOS Sans / 系统 PingFang SC，英文 Inter 或同类，随 App 分发须为可再分发许可）；标签加粗。像素字体（Fusion Pixel，OFL）只用于数字读数与像素图表刻度。
- **描边与阴影**：卡片 3px `--ink` 描边、圆角 16px；可倾斜 ±3°；无模糊阴影，仅地面椭圆浅影。
- **徽章 `ServerBadge`**：圆形 120–160px，6px 同色圆环；下方服务胶囊溢出时显示 `+N`。
- **像素粒子 `PixelStream`**：8px 方块，颜色取两端服务器/服务色，沿路径渐隐；Canvas 绘制，最多 60fps，离屏暂停；`prefers-reduced-motion` 时改为静态虚线方块。
- **图表**：像素柱/阶梯线，格子 4–8px。
- **动效**：团子眨眼、呼吸（≤ 2% 缩放）；状态切换用 `steps()`；均遵守 reduced motion。
- **插画资产**：团子与地区场景为 SVG，团子身体颜色由 CSS 变量注入；M5b 由 Claude 生成 SVG 初稿：1 个团子 × 8 个状态 + 8 个地区场景（昼/夜），用户验收后定稿。

---

## 10. 已定事项

- App 名：**VPSGoGo**；Go module：`github.com/bubbleptr/vpsgogo`。
- 插画：M5b 由 Claude 生成 SVG 初稿（团子 × 8 状态、8 个地区场景昼/夜），用户验收后定稿。
