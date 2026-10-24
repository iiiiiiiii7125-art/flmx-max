# R12 修复批 X 报告（R12X-002 自建渲染侧归一）

- **基线**：`main` @ `cefb566`（= `integrate-r11` 尖端）；工作分支 `fix-r12/bX`（自 `cefb566` 切出，`main` 与 `integrate-r12` 未动，集成由协调者做）。
- **依据**：`R12-FIX-PLAN.md` §1 批 X、 `R12-X-verdicts.md` §0.2；未改任务书口径，未增删条目。
- **结论**：关闭断言成立——三处原文消费点归一落地，备份恢复变体四面产物逐字小写规范值，垃圾 mode 回退 `auto` 不毒化；先红后绿回归通过。

## X-0 先行探针（施工前必做，定后果等级）

**探针对象**：备份逐字恢复变体对象本身，未经 `Normalize`（N-35 合规）——以 `r11b1XHTTPInbound(mode)`（`r11_b1_xhttp_mode_test.go` 同款构造：VLESS / 443 / `xhttp` / `/xh` / `security none`）直接进 `Render(TypeXray, …)`；施工前渲染逐字性已实测确认：`mode` 渲染后仍为 `AUTO` / `Packet-Up` 原值（若被愈合则探针对象失效，本探针无此问题）。

**二进制**：`Xray 25.9.5 (118131f, go1.25.1 linux/amd64)`（在盘 `~/.r9bin/xray-25.9.5-bin`）。

**方法诚实注记**：原始 `Render` 产物含 `listen: ""`（探针构造未设 Listen），该产物在 25.9.5 下无论 mode 为何均 panic（`InboundDetourConfig.Build` index out of range，规范 `auto` 对照组亦同），属与 mode 无关的探针构造 artifact。故三份配置统一置 `listen=127.0.0.1`、端口统一改为 `14443`，其余字节与渲染产物一致；**mode 为唯一变量**。

| 配置 | Xray 25.9.5 `run -test` | 结果 |
|---|---|---|
| `mode: "AUTO"`（备份恢复变体） | exit **23** | `Failed to build XHTTP config. > infra/conf: unsupported mode: AUTO` |
| `mode: "Packet-Up"`（备份恢复变体） | exit **23** | `Failed to build XHTTP config. > infra/conf: unsupported mode: Packet-Up` |
| `mode: "auto"`（规范对照组） | exit **0** | `Configuration OK.` |

**mihomo 面**：注明不适用——探针对象是 Xray 服务端入站 JSON，非 mihomo 输入格式；且本机无 mihomo 二进制（`.r9bin/mihomo-home` 为空目录，仅存配置样本）。mihomo 客户端面（Clash `xhttp-opts.mode`）由 X-2 产物逐字证据覆盖。

**后果等级判定**：exit≠0 已证实 → 按 `R12-X-verdicts.md` §0.2 写死的升级条件，**终局候选版审计可据此将本格重定为 P2（不溯改 R12 ledger）**。本判定不改变本批施工本身（照常施工，见下）。

## X-1 施工

新增共享愈合函数 `canonicalXHTTPMode`（`render_xray.go`，与三处同包，定义落在批内文件，不另扩文件）：`TrimSpace` 后小写，与 `normalizeTransport` 同合法集 `{auto, packet-up, stream-up, stream-one}`（EqualFold 意义下）；愈合后仍域外（含空值）回退默认 `auto`——安全方向，与 `chain.go:487` 现有空值回退同向。只愈合渲染产物，绝不回写原对象。

| 落点 | 改动 |
|---|---|
| `go-backend/internal/kernel/render_xray.go:125`（Xray 入站 `xhttpSettings.mode`） | `t.Mode` → `canonicalXHTTPMode(t.Mode)` |
| `go-backend/internal/kernel/chain.go:487`（Xray 链式出站 `xhttpSettings.mode`） | `firstNonEmptyString(t.Mode, defaultXHTTPMode)` → `canonicalXHTTPMode(t.Mode)`（空值语义不变：空仍得 `auto`） |
| `go-backend/internal/kernel/share.go:192`（ShareURI `mode` 参数） | `t.Mode` → `canonicalXHTTPMode(t.Mode)` |
| `go-backend/internal/kernel/share.go:306-307`（Clash `xhttp-opts.mode`） | 非空守卫不变（空值仍省略该键，沿既有口径），值 → `canonicalXHTTPMode(…)` |

**护栏遵守**：未改备份恢复逐字落库行为（`repository_inbound_backup.go` 未动；`TestR11B1BackupRestoreXHTTPModeGate` 仍 PASS，见下）；未改 `normalizeTransport` 本体（`model.go` 不在 diff 内，`git diff --name-only` 计 0）；sing-box 自建入站不消费 `Transport.Mode`（grep 确认），故三处即全集，无第四落点遗漏。

## X-2 证据

**回归测试**（新增 `go-backend/internal/kernel/r12_bx_xhttp_mode_test.go`，全程不调 `Normalize`，探针对象为备份恢复变体本身）：

- 先红：施工前 `TestR12BXSelfBuiltRenderHealsBackupVariants` FAIL（`变体 "Stream-Up" 的 Xray 入站 mode 须为 "stream-up"，got "Stream-Up"`）——逐字毒化复现。
- 后绿：施工后同测试 PASS。

**四面产物逐字**（变体格 `AUTO` / `Packet-Up` / `STREAM-ONE` / `Stream-Up`；以 `AUTO` / `Packet-Up` 实测输出为例）：

| 面 | `AUTO` 输入 → 产物 | `Packet-Up` 输入 → 产物 |
|---|---|---|
| Xray 入站 `xhttpSettings.mode` | `"auto"` | `"packet-up"` |
| Xray 链式出站 `xhttpSettings.mode` | `"auto"` | `"packet-up"` |
| ShareURI | `vless://11111111-2222-3333-4444-555555555555@203.0.113.10:443?encryption=none&mode=auto&path=%2Fxh&security=none&type=xhttp#sb` | 同形，`mode=packet-up` |
| Clash `xhttp-opts.mode` | `mode: "auto"` | `mode: "packet-up"` |

**垃圾 mode 存量行**：`bogus` / `packet_up` / `auto-up` 四面均回退 `auto`（Xray 入站 `auto`、链式 `auto`、ShareURI `mode=auto`、Clash `mode: "auto"`），不拒单、不毒化。

**空值格**：Xray 入站 / 链式 / ShareURI 补默认 `auto`；Clash 沿既有口径省略 `mode` 键（守卫未动）。

**渲染不改落库值**：测试断言渲染后原对象 `Transport.Mode` 仍为输入原值（`AUTO` 等），渲染侧愈合不回写。

**修后二进制复验**：修后渲染的 `AUTO` / `Packet-Up` / `bogus` 三份 Xray 配置（`listen=127.0.0.1`，同 X-0 方法）经 Xray 25.9.5 `run -test` 均为 **exit 0**（`Configuration OK.`），与 X-0 的 exit 23 形成前后对照。

**受影响包验证**（`export PATH=$HOME/go/bin:$PATH`，`TMPDIR=~/workspace/.tmp-big`）：

- `go build ./...`（go-backend）通过；`go vet ./internal/kernel/ ./internal/store/repo/` 通过（仅 GOPATH/GOROOT 同目录 warning，与本批无关）。
- `go test ./internal/kernel/ -count=1`：**PASS**（全包，含新增测试与既有 R11 XHTTP 测试）。
- `go test ./internal/store/repo/ -run TestR11B1BackupRestoreXHTTPModeGate`：**PASS**（R5X-024 逐字落库护栏未动）。

## 提交

- 分支：`fix-r12/bX`（自 `cefb566`）。
- 本批文件（显式 `git add`，未用 `git add -A`；同工作区内批 T 的未提交改动未碰、未入库）：
  - `go-backend/internal/kernel/render_xray.go`（愈合函数 + 入站点）
  - `go-backend/internal/kernel/chain.go`（链式点）
  - `go-backend/internal/kernel/share.go`（ShareURI + Clash 两点）
  - `go-backend/internal/kernel/r12_bx_xhttp_mode_test.go`（新增回归测试）
  - `audit-out-132/r12/R12-FIX-BX-report.md`（本报告）
- 提交号：代码提交 `215e67a85ade34b93b1458ffb06928679da8eede`（`fix-r12/bX` 首提交，4 文件）；本报告随后单独提交于同分支。

— R12-FIX-BX END · 探针 exit 23/23/0 → 终局可重定 P2（不溯改 ledger）；施工照常完成，断言关闭 · END
