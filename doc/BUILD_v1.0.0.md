# flvx-max v1.0.0 — 构建说明

## 来源

- 基座：flvx-mod 最新 main（0fcbe5b，含 1.0.7 孤儿数据修复、IDOR 修复等）
- 叠加：80 轮审计的全部修复（`audit/work` 分支）

## 审计修复清单（11 项）

### 安全修复
1. **F-AUDIT-009**：`JWT_SECRET` 为空时生成 32 字节临时密钥，不再以空字符串作 HMAC
2. **F-AUDIT-015**：WebSocket 三入口加 10MB 消息大小限制（防内存 DoS）
3. **F-AUDIT-016**：HTTP `decodeJSON` 加 10MB body 限制（防内存 DoS）
4. **F-AUDIT-014**：panic 详情不再返回客户端，只服务端日志

### 功能修复
5. **F-AUDIT-001**：`getMyInbounds` 过滤父入站已禁用的中转
6. **F-AUDIT-013**：密码最小 6 位校验（`updatePassword` + `userCreate`，按 rune 计）

### 测试修复
7. **F-AUDIT-005/017/018**：测试用旧管理员凭据 `admin_user` → `admin@user`
8. **新增**：IDOR 签名回归测试（`TestSubURLSig*`）
9. **新增**：body 大小限制回归测试、密码 rune 计数测试

### 代码质量
10. **F-AUDIT-002**：gofmt
11. **F-AUDIT-007**：`assign.tsx` a11y（checkbox aria-label）

## 验证

- `go build ./...` 通过
- `go test ./...` 全绿（含新增回归测试）
- `go vet` 干净

## 与 flvx-mod 的关系

flvx-max 是独立仓库，不影响 flvx-mod。原仓库未做任何修改。
