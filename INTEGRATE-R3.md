# INTEGRATE-R3 — 流量修复第三轮集成台账

基线：traffic-fix/integrate 尖端 700a9f9a（代码尖端 59fce6d7，V-D fixup＋C03 契约修正）。
代码尖端：c8e61df0（三路全部合入，见下）。门禁均在 c8e61df0 上跑通；本台账提交本身只加文档，不改代码。

## 合入顺序（实际）

| # | 分支 | 提交 | 内容 | 合并提交 | 冲突 |
|---|------|------|------|----------|------|
| 1 | traffic-fix3/vdnew | d39e9c32 | B-VD-NEW 截断窗口逃逸：fail-closed（门关时丢弃 SS 端点，不回退源站） | 6f5d8c67 | 无 |
| 2 | traffic-fix3/40900 | 5635885f | R2C-01 40900 硬编码：伴生 Clash API 口按节点密钥 SHA-256 派生 41000–41999 | 27c77b04 | 无 |
| 3 | traffic-fix3/lows | 00621eb0 | 13 低＋1 微捆绑（E-01-RES、E-05-NEW-1/2、E-02-PARTIAL、R2A-1/2/3/5、R2C-02/03、B-L4-NEW、D-OBS-01、D-N1、R2A-4） | c8e61df0 | 无 |

3 路全部为 merge commit 合入，3 条分支均已是 HEAD 祖先（merge-base --is-ancestor 全过）。
700a9f9a..c8e61df0 差集：43 文件 +1135/−278，与 3 路各自差集之和（5 文件 +144/−18 ＋ 10 文件 +483/−27 ＋ 31 文件 +508/−233 ＝ 46 文件 +1135/−278，减 3 个重叠文件：doc/guide/subscription.md、go-backend/internal/http/handler/subscription.go、go-gost/x/kernel/manager.go）逐项一致，无修复丢失。
冲突点：0。凡动到修复逻辑的冲突点：无（未发生冲突，故无）。

## 门禁（代码尖端 c8e61df0）

| 门禁 | 结果 | 日志 |
|------|------|------|
| check-doc-consistency.sh | PASS（EXIT=0；含 F3-C 新增 §26 技能包例文形态门、§27 内核视图字段对齐门，三项新检查均 OK） | ~/workspace/tmp/int-r3/doc-consistency.log |
| go vet（go-backend） | PASS（EXIT=0，零输出） | ~/workspace/tmp/int-r3/vet-backend.log |
| go vet（go-gost/x） | PASS（EXIT=0，零输出） | ~/workspace/tmp/int-r3/vet-x.log |
| K11 计数核对 | PASS（实测 tested=90 / untested=123，与 ci-build.yml 声称 90/123 一致；三路新增测试文件均落在已有 tested 包内，计数不动） | 独立 find 复算（本台账正文） |
| pnpm build | PASS（EXIT=0） | ~/workspace/tmp/int-r3/pnpm-build.log |
| handler 全包（`go test ./...`，含 tests/contract） | PASS（19 包 ok，0 FAIL，EXIT=0；handler 包 906.111s、repo 包 292.067s、contract 包 89.354s） | ~/workspace/tmp/int-r3/handler-full.log |
| go-gost/x 全包 | PASS（首跑即全绿，EXIT=0；90 包 ok＋123 包无测试文件＝213，与 K11 实数一致；internal/net/udp ok 9.090s，无抖动复跑） | ~/workspace/tmp/int-r3/x-full.log |

前置：vite-frontend/node_modules 在工作树内在场（typescript 在场，集成前已核实）；TMPDIR 均用磁盘目录（/tmp 为 512M tmpfs）。

## 移交 V-round 的待验项（本集成路不跑 live）

- D-OBS-01 live 未验（F3-C 单测绿，live 需伴生＋截断，当时 40900 被占）。
- R2A-3 / R2A-5 / R2A-4 live 未验（单测钉死，无自然触发）。
- F3-B 移交①：B 首轮取订阅曾落在截断窗口拿到源站口（与 B-VD-NEW 同族，合入后 F3-A 的 fail-closed 应覆盖——V-round 须验窗口内拉取为空、不是源站）。

## 未启动的后续阶段

真机总验与第三轮全量回头看是之后的独立阶段，本集成路不启动。
