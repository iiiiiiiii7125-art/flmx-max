# design-baseline.md — 判定基线（只读，任何一轮不得修改）

来源：用户任务书 v5 §1。任何审计发现与修复建议不得违反本基线。

## 1.1 端口与复用

- 同一端口可在不同节点重复使用（节点 10/20/30 同用 18080）→ 核心特性
- 冲突域 = 同节点 + 同端口 + 其他转发，非全局唯一 → 特性
- 冲突判定不区分 protocol（每转发同端口同时建 _tcp/_udp）→ 正确行为
- 同节点不同 in_ip 用同端口：不支持，一律冲突 → 设计边界
- 多入口禁 inIp；多出口禁 connectIp；单跳多链禁 connectIp → 特性
- 自定义 inPort 须在节点端口范围内；留空随机 → 特性
- 编辑未显式改 IP 时保留 bind IP → 特性
- 自身残留占用才删残留重试；他转发占用硬失败 → 特性
- 若存在唯一索引，必须含 node_id → 约束

## 1.2 其余已声明特性

- JWT 原始 token 无 Bearer；业务 API 用 POST，订阅/flow/system-info 用 GET
- 统一响应 {code,msg,data,ts}，code 0 成功
- SQLite MaxOpenConns(1) + WAL + busy_timeout；事务内必须复用同一 tx
- 前端不写测试；UI 只 import shadcn-bridge/heroui/*
- Passkey 单实例、challenge 存内存
- 订阅能力矩阵缺项；Loon/QX/Base64 不支持链式
- SS/Snell/导入节点共用凭据；订阅流量只统计独立凭据协议
- 导入节点不部署、不计流量、无白名单、无密钥轮换；3 分钟 TCP 探测；Hy2/TUIC 不显示状态
- 掉线 >90s 告警、连续 2 次失败告警、同一问题只告警一次
- nftables 可选，缺失时无统计/白名单/端口跳跃
- 内核安装有官方校验
- REPO 硬编码 fork，release 流水线重写
- 后端 root + docker.sock 是自升级硬要求（设计权衡）
- token 存 localStorage；CORS *；WS CheckOrigin true（设计权衡）
- AES fail-open（设计权衡）
- duplicate node last-writer-wins（自愈设计）
- FOR UPDATE 在 SQLite 被剥离（非缺陷）
- go-gost/x 用 replace

## 1.3 判定流程

1. 有声明且代码一致 → 非缺陷
2. 有声明但代码不符 → 缺陷（引用原文）
3. 无声明但有注释/测试表达意图 → 设计权衡，最多 P3
4. 无依据且违反常识 → 缺陷
5. 拿不准 → 疑似 + 可执行验证方法

## 禁止的修复建议

- 加 in_ip/protocol 维度
- 改全局端口唯一
- 给 JWT 加 Bearer
- 给前端补测试
- 修改 1.1/1.2 的行为
