# coverage-ledger.md — 覆盖账本

每读完一项更新：路径 / 深度（静态/动态/端到端）/ 结论。
结论只能是：通过 / 已修复 / 明确受限（附文档依据）/ 未验证（附替代验证方式）。

## S1 安装与部署（脚本静态 + 动态）

| 文件 | 静态 | 动态 | 结论 |
|---|---|---|---|
| install.sh | ✅ 全读+bash -n+shellcheck | 待（S4） | 静态完成：发现 SCR-006/007；H-01/H-24 修复确认存在 |
| panel_install.sh | ✅ 全读+bash -n+shellcheck | 待（S4） | 静态完成：发现 SCR-003/008/009/010/011/012；更新/备份/回滚链路完整 |
| test-install-scripts-alpine.sh | ✅ 全读+bash -n+shellcheck | 待（S4） | 静态通过；POSIX clean，锚点手法成立 |
| test-install-scripts-proxy.sh | ✅ 全读+bash -n+shellcheck | 待（S4） | 静态通过；24 用例覆盖代理/回滚/幂等/JSON 转义（SCR-010 触发点） |
| docker-compose-v4.yml | ✅ yaml 解析+逐行 | 待 | 静态通过（SCR-016 弱默认值备注） |
| docker-compose-v6.yml | ✅ yaml 解析+逐行 | 待 | 静态通过（仅差 enable_ipv6+子网） |
| go-backend/Dockerfile | ✅ 人工审查 | 待 | 静态通过（SCR-013 无 .dockerignore） |
| vite-frontend/Dockerfile | ✅ 人工审查 | 待 | 静态通过 |
| vite-frontend/nginx.conf | ✅ 人工审查（无 nginx -t） | 待 | 静态完成：发现 SCR-002；WS/SSE/缓存策略排序正确 |
| vite-frontend/vercel.json | ✅ | 待 | 静态通过（SPA rewrite） |
| vite-frontend/.env.development | ✅ | 待 | 静态通过（dev 指向 127.0.0.1:6365） |
| vite-frontend/.env.production | ✅ | 待 | 静态完成：发现 SCR-005（残留上游地址） |

## S2 构建与发布

| 文件 | 静态 | 动态 | 结论 |
|---|---|---|---|
| go-backend/Makefile | ✅ | 待 | 静态通过 |
| go-backend/patch.py | ✅ 全读 | 待 | P3 备注：str.replace 无匹配静默无操作；但无任何 CI/脚本/文档调用，属手动开发工具 |
| vite-frontend/package.json | ✅ | 待 | 静态通过（pnpm 10.28.1 pin、rolldown-vite） |
| vite-frontend/vite.config.ts | ✅ | 待 | 静态通过；注 AGENTS.md 称 minify:false/treeshake:false 但实际文件无此配置（文档漂移） |
| vite-frontend/liquid-glass.js | ✅（引用关系确认） | 待 | 静态完成：发现 SCR-014（无人引用） |
| vite-frontend/postcss.config.js | ✅ | 待 | 静态通过（tailwind v4 postcss 插件） |
| vite-frontend/tailwind.config.js | ✅（结构+色板） | 待 | 静态通过 |
| doc/package.json | ✅ | 待 | 静态通过；doc/package-lock.json 存在，deploy-docs 缓存键有效 |
| doc/.vitepress/config.mts | ✅ 全读 | 待 | 静态通过（base /flvx-max/、链接均指向 fork） |
| skills/flvx-api（含 postinstall 与 npm 发布链） | ✅ package.json 全读+SKILL.md 结构 | 待 | 静态完成：发现 SCR-004/015 |
| .github/workflows/ci-build.yml | ✅ yaml 解析+全读 | 待 | 静态通过（SCR-017 版本漂移备注；六 job 完整） |
| .github/workflows/docker-build.yml | ✅ yaml 解析+全读 | 待 | 静态完成：发现 SCR-001（含死代码）；其余发版链路完整 |
| .github/workflows/deploy-docs.yml | ✅ | 待 | 静态通过 |
| .github/workflows/publish-skill.yml | ✅ | 待 | 静态完成：发现 SCR-001 |

## F1–F14 功能基准（代码审查 + 实际验证）

状态：R0 未开始；从 Round 1 按 R1 覆盖顺序推进（脚本清单 → F14 → F1-F3 → F4 → F5 → F6 → F7 → F8 → F9 → F10 → F11 → F12 → F13 → 前端页面）。

详细条目见任务书 §2（F1.1–F14.8 共约 150 条），每条结论填入 rounds/R<N>.md 的功能基准表。
