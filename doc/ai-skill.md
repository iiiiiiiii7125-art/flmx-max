# AI Skill 使用指南

让大模型直接操作 FLMX 面板的技能包。支持 OpenCode、OpenClaw、Claude Code 等工具。

## 安装

### 方式 1: npm (推荐)

```bash
npm install -g @flmx/skill-api
```

postinstall 脚本会自动链接到 `~/.agents/skills/flmx-api/`。

### 方式 2: 手动链接

```bash
# 从 FLMX 源码
cd /path/to/flmx
mkdir -p ~/.agents/skills
ln -sf $(pwd)/skills/flmx-api ~/.agents/skills/

# 或从 GitHub
git clone https://github.com/iiiiiiiii7125-art/flmx-max.git
cd flmx-max
ln -sf $(pwd)/skills/flmx-api ~/.agents/skills/
```

## 配置

设置环境变量：

```bash
export FLMX_BASE_URL="https://your-panel.example.com"
export FLMX_USERNAME="admin"
export FLMX_PASSWORD="your-password"
```

或使用凭证文件：

```bash
mkdir -p ~/.flmx
cat > ~/.flmx/.env << 'EOF'
export FLMX_BASE_URL="https://panel.example.com"
export FLMX_USERNAME="admin"
export FLMX_PASSWORD="your-password"
EOF
chmod 600 ~/.flmx/.env
source ~/.flmx/.env
```

---

## 工具接入方法

### OpenCode

OpenCode 是命令行 AI 编程助手，支持通过 skills 扩展能力。

**安装 skill:**
```bash
npm install -g @flmx/skill-api
```

**使用:**
```bash
export FLMX_BASE_URL="https://panel.example.com"
export FLMX_USERNAME="admin"
export FLMX_PASSWORD="your-password"

opencode
```

**示例对话:**
```
你: 查看我的转发列表
你: 创建一个转发到 192.168.1.100:80 使用隧道 1
你: 检查节点状态
你: 查看流量使用情况
```

---

### OpenClaw

OpenClaw 同样支持 skills 机制。

**安装 skill:**
```bash
npm install -g @flmx/skill-api

# 或手动链接
mkdir -p ~/.openclaw/skills
ln -sf /path/to/flmx/skills/flmx-api ~/.openclaw/skills/flmx-api
```

**使用:**
```bash
openclaw

>>> 查看所有节点状态
>>> 给用户 alice 分配 50GB 流量
>>> 导出系统备份
```

---

### Claude Code

Claude Code 是 Anthropic 官方的命令行工具，支持通过 CLAUDE.md 扩展。

#### 方式 1: 项目级 CLAUDE.md

在项目根目录创建 `CLAUDE.md`：

```markdown
# FLMX API Skill

你可以通过 REST API 操作 FLMX 面板。

## 环境变量
- FLMX_BASE_URL: 面板地址
- FLMX_USERNAME: 用户名
- FLMX_PASSWORD: 密码

## 认证规则
- Authorization 头使用原始 JWT token，不加 "Bearer " 前缀
- 所有 API 使用 POST 方法

## 常用 API

### 登录获取 token
POST /api/v1/user/login
{"username": "...", "password": "..."}

### 查看转发列表
POST /api/v1/forward/list
Authorization: <token>
{}

### 创建转发
POST /api/v1/forward/create
{"name": "xxx", "tunnelId": 1, "remoteAddr": "1.2.3.4:80"}

### 查看节点
POST /api/v1/node/list
{}
```

**使用:**
```bash
cd /path/to/your/project
claude
```

#### 方式 2: 全局 CLAUDE.md

```bash
mkdir -p ~/.claude
cat > ~/.claude/CLAUDE.md << 'EOF'
# FLMX Panel Operations

使用 FLMX REST API 操作流量转发面板。

环境变量: FLMX_BASE_URL, FLMX_USERNAME, FLMX_PASSWORD
调用方式: curl -X POST "$FLMX_BASE_URL/api/v1/..." -H "Authorization: $TOKEN"
注意: Authorization 不要加 Bearer 前缀
EOF
```

#### 方式 3: 复制 SKILL.md

```bash
cat ~/.agents/skills/flmx-api/SKILL.md >> ~/.claude/CLAUDE.md
```

**示例对话:**
```
>>> 帮我查看 FLMX 面板上有哪些节点
>>> 创建一个名为 test 的转发，目标地址 10.0.0.1:80
>>> 查看我的流量使用情况
```

---

## API 覆盖

| 模块 | 操作 |
|------|------|
| 认证 | 登录、Token 管理 |
| 用户 | 增删改查、流量重置、密码 |
| 节点 | 增删改查、安装、升级、状态 |
| 隧道 | 增删改查、用户分配 |
| 转发 | 增删改查、暂停/恢复、诊断 |
| 分组 | 用户/隧道分组、权限 |
| 限速 | 增删改查 |
| 联邦 | 节点共享、远程节点 |
| 内核 | 能力矩阵、安装、版本、启停、日志 |
| 入站 | 增删改查、导入、分享、分流、中转线路 |
| 订阅 | 增删改查、订阅源、按订阅分配、公开拉取、流量统计（`/api/v1/subscription/traffic`、`/api/v1/user/subscription/traffic`） |
| 监控 | 节点/隧道指标、服务监控、监控权限 |
| Passkey | 登录、注册、列表、删除 |
| 系统 | 版本、存储、检查更新、面板自升级、公告、通知测试 |
| 备份 | 导出/导入 |

## 安全提示

- ⚠️ 环境变量在进程列表中可见
- 使用 `~/.flmx/.env` 文件并设置 `chmod 600`
- 添加 `export HISTIGNORE="*FLMX_PASSWORD*"` 防止密码进入历史记录
- Token 仅在会话内存中缓存，不写入磁盘

## 发布

技能包随 FLMX 产品版本一起发布，不单独打 tag：推送**纯数字 tag**（与产品版本相同，如 `1.3.7`）会同时触发产品构建与技能包发布流水线，技能包版本号由流水线按 tag 同步（SKILL.md 的 `version` 与 npm 包版本在发布时对齐到该 tag）。

```bash
# 随产品版本发布（示例）
git tag 1.3.7
git push --tags
```

注意：不要再使用 `skill-v<版本>` 形式的 tag——现行流水线的触发器只认纯数字 tag，`skill-v*` 不会触发任何发布。仓库内 `skills/flmx-api/SKILL.md`、`skills/flmx-api/package.json` 与本文示例的版本号字面量应保持一致（`scripts/check-doc-consistency.sh` 会校验）。

也可以在 GitHub Actions 页面手动触发 publish-skill workflow。

需要在 GitHub 仓库设置 `NPM_TOKEN` secret。
