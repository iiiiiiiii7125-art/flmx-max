# coverage-ledger.md — 覆盖账本

每读完一项更新：路径 / 深度（静态/动态/端到端）/ 结论。
结论只能是：通过 / 已修复 / 明确受限（附文档依据）/ 未验证（附替代验证方式）。

## S1 安装与部署（脚本静态 + 动态）

| 文件 | 静态 | 动态 | 结论 |
|---|---|---|---|
| install.sh | ✅ 全读+bash -n+shellcheck | ✅ S4 桩动态（T1/T2/T3/T4a/T4b/T8） | 动态：安装 happy path/注入无注入点/fail-closed 三项/H-01·D19/更新回滚/幂等/H-24 守卫均通过；SCR-007 已修复；未验证：Alpine 实机（见 S4 未验证项） |
| panel_install.sh | ✅ 全读+bash -n+shellcheck | ✅ S4 桩动态（T5/T6/T7/T8） | 动态：DOMAIN 注入证伪（单遍展开无执行，SCR-019 降级为字符集校验）；SCR-009 复现确认可达；get_public_ip GNU 正常；SCR-003/008/009/011/018/019 已修复；未验证：install_panel 全流程需真实 docker、SCR-011 busybox 本机无环境 |
| test-install-scripts-alpine.sh | ✅ 全读+bash -n+shellcheck | ⏸️ 未验证（本机 docker info 不可用） | 静态通过；动态替代：在有 docker 的机器/CI 按头注释 `docker run --rm alpine:3.22` 执行 |
| test-install-scripts-proxy.sh | ✅ 全读+bash -n+shellcheck | ⏸️ 未验证（桩环境未跑全） | 静态通过；24 用例设计完备，替代验证=CI |
| docker-compose-v4.yml | ✅ yaml 解析+逐行 | 待 | 静态通过（SCR-016 弱默认值备注） |
| docker-compose-v6.yml | ✅ yaml 解析+逐行 | 待 | 静态通过（仅差 enable_ipv6+子网） |
| go-backend/Dockerfile | ✅ 人工审查 | 待 | 静态通过（SCR-013 无 .dockerignore） |
| vite-frontend/Dockerfile | ✅ 人工审查 | 待 | 静态通过 |
| vite-frontend/nginx.conf | ✅ 人工审查（无 nginx -t） | ⏸️ 未验证（本机无 nginx -t） | SCR-002 已修复（client_max_body_size 50m）；语法变更为单行指令，风险低 |
| vite-frontend/vercel.json | ✅ | 待 | 静态通过（SPA rewrite） |
| vite-frontend/.env.development | ✅ | 待 | 静态通过（dev 指向 127.0.0.1:6365） |
| vite-frontend/.env.production | ✅ | —（静态配置） | SCR-005 已修复（改 fork 地址） |

## S2 构建与发布

| 文件 | 静态 | 动态 | 结论 |
|---|---|---|---|
| go-backend/Makefile | ✅ | 待 | 静态通过 |
| go-backend/patch.py | ✅ 全读 | 待 | P3 备注：str.replace 无匹配静默无操作；但无任何 CI/脚本/文档调用，属手动开发工具 |
| vite-frontend/package.json | ✅ | 待 | 静态通过（pnpm 10.28.1 pin、rolldown-vite） |
| vite-frontend/vite.config.ts | ✅ | 待 | 静态通过；注 AGENTS.md 称 minify:false/treeshake:false 但实际文件无此配置（文档漂移） |
| vite-frontend/liquid-glass.js | ✅（引用关系确认） | — | SCR-014 已修复（文件已删除） |
| vite-frontend/postcss.config.js | ✅ | 待 | 静态通过（tailwind v4 postcss 插件） |
| vite-frontend/tailwind.config.js | ✅（结构+色板） | 待 | 静态通过 |
| doc/package.json | ✅ | 待 | 静态通过；doc/package-lock.json 存在，deploy-docs 缓存键有效 |
| doc/.vitepress/config.mts | ✅ 全读 | 待 | 静态通过（base /flvx-max/、链接均指向 fork） |
| skills/flvx-api（含 postinstall 与 npm 发布链） | ✅ package.json 全读+SKILL.md 结构 | ✅ 逻辑桩验证 | SCR-004 已修复（三 case 桩验证通过）；SCR-015 已修复（版本对齐） |
| .github/workflows/ci-build.yml | ✅ yaml 解析+全读 | ✅ YAML 解析 | SCR-017 已修复（setup-go 1.25）；动态=下次 CI 运行见证 |
| .github/workflows/docker-build.yml | ✅ yaml 解析+全读 | 待 | 静态完成：发现 SCR-001（含死代码）；其余发版链路完整 |
| .github/workflows/deploy-docs.yml | ✅ | 待 | 静态通过 |
| .github/workflows/publish-skill.yml | ✅ | ✅ YAML 解析 | SCR-001 已修复（触发器/版本提取/写回三处）；动态=下次纯数字 tag 推送见证 |

## F1–F14 功能基准（代码审查 + 实际验证）

状态：R0 未开始；从 Round 1 按 R1 覆盖顺序推进（脚本清单 → F14 → F1-F3 → F4 → F5 → F6 → F7 → F8 → F9 → F10 → F11 → F12 → F13 → 前端页面）。

详细条目见任务书 §2（F1.1–F14.8 共约 150 条），每条结论填入 rounds/R<N>.md 的功能基准表。
