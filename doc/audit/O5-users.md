# O5 用户管理功能审计报告

**审计员**：用户管理专项
**日期**：2026-10-20
**范围**：用户 CRUD、状态管理、配额、订阅分配、密码管理
**结论**：发现 P1×1 / P2×3 / P3×4

---

## P1 严重

### O5-001：删除用户未清理订阅分配（user_inbound 孤儿数据）

**文件**：`go-backend/internal/store/repo/repository_mutations.go:160` (`DeleteUserCascade`)

**证据**：
```go
func (r *Repository) DeleteUserCascade(userID int64) error {
    return r.db.Transaction(func(tx *gorm.DB) error {
        // 删除：Passkey、ForwardPort、Forward、GroupPermissionGrant、
        //       UserTunnel、UserGroupUser、StatisticsFlow、UserQuota、MonitorPermission
        // 唯独没有删除：UserInbound（订阅分配）！
        return tx.Where("id = ?", userID).Delete(&model.User{}).Error
    })
}
```

**复现步骤**：
1. 管理员给用户 A 分配入站订阅（`POST /api/v1/user-inbound/set`，`{userId: A, assignments: [...]}`）
2. 管理员删除用户 A（`POST /api/v1/user/delete`，`{id: A}`）
3. 查询 `user_inbound` 表：`SELECT * FROM user_inbound WHERE user_id = A;` → 仍有残留行
4. 新建同名用户 B（ID 不同），残留的 `user_id=A` 行不会影响 B，但数据库产生垃圾数据；若未来 ID 复用（SQLite `sqlite_sequence` 重置或手动指定），会导致错误分配

**影响**：孤儿数据堆积；与 1.0.7 修复的"删除入站/中转后 UserInbound 孤儿"同类问题，但这是用户维度的遗漏。

**对比**：删除入站时（`repository_kernel.go:219`）和删除中转时（`:465`）都清理了 `user_inbound`，唯独删除用户时遗漏。

---

## P2 一般

### O5-002：用户名无长度校验（DB varchar(100)）

**文件**：
- `go-backend/internal/http/handler/mutations.go:44` (`userCreate`)
- `go-backend/internal/http/handler/mutations.go:135` (`userUpdate`)

**证据**：两处仅检查 `username == ""`，无最大长度限制。DB 列定义为 `varchar(100)`（`model.go:14`）。

**复现步骤**：
1. `POST /api/v1/user/create`，`{user: "<200个字符>", pwd: "123456"}`
2. SQLite：成功创建（SQLite 不强制 varchar 长度），超长用户名存入
3. PostgreSQL：`ERROR: value too long for type character varying(100)` → 500 错误

**影响**：双数据库行为不一致；超长用户名在前端表格中破坏布局。

### O5-003：用户列表忽略分页参数（全量加载）

**文件**：`go-backend/internal/http/handler/handler.go:582` (`userList`)

**证据**：
```go
var req struct {
    Current int    `json:"current"`
    Size    int    `json:"size"`
    Keyword string `json:"keyword"`
}
// ... 解析后
users, err := h.repo.ListUsers()  // 返回全部用户，无 LIMIT/OFFSET
// keyword 仅在内存中过滤
```

**复现步骤**：
1. 创建 5000 个测试用户
2. `POST /api/v1/user/list`，`{current: 1, size: 20}`
3. 返回全部 5000 条，而非 20 条

**影响**：用户量大时响应慢、内存占用高；前端分页形同虚设。

### O5-004：创建用户部分失败（无事务包裹）

**文件**：`go-backend/internal/http/handler/mutations.go:88-113` (`userCreate`)

**证据**：
```go
userID, err := h.repo.CreateUser(...)  // 步骤1：用户已创建
if err != nil { ... }
if dailyQuotaGB > 0 || monthlyQuotaGB > 0 {
    tx := h.repo.BeginTx()
    // 步骤2：配额配置，若此处失败...
    if err := h.repo.SaveUserQuotaConfigTx(...); err != nil {
        response.WriteJSON(w, response.Err(-2, err.Error()))
        return  // 用户已存在，但配额未配置 → 部分状态
    }
}
groupIDs := asInt64Slice(req["groupIds"])
if len(groupIDs) > 0 {
    // 步骤3：分组，若 AddUserToGroups 静默失败（addErr == nil 才继续，无 else）
}
```

**复现步骤**：
1. `POST /api/v1/user/create`，`{user: "test", pwd: "123456", dailyQuotaGB: 10}`，同时制造配额表写入失败（如磁盘满）
2. 用户 "test" 已创建，但配额配置丢失
3. 管理员看到"创建失败"提示，重试时报"用户名已存在"

**影响**：低频，但会造成"创建失败却占用用户名"的困惑状态。

---

## P3 备注

### O5-005：`num`（设备数限制）接受负值

**文件**：`mutations.go:70`（创建）、`mutations.go:186`（更新）
```go
num := asInt(req["num"], 10)  // 无负值检查
```
负的设备数限制语义不明，建议钳制为 ≥0。

### O5-006：`maxConn` 接受负值

**文件**：`mutations.go:81`（创建）、`mutations.go:192`（更新）
```go
maxConn := asInt(req["maxConn"], 0)  // 无负值检查
```
负的最大连接数可能导致限流逻辑异常。

### O5-007：`status` 无枚举值校验

**文件**：`mutations.go:64`（创建）、`mutations.go:190`（更新）
```go
status := asInt(req["status"], 1)  // 任意整数都接受
```
`status` 预期为 0（禁用）/1（启用），传入 2、-1 等值时行为未定义。

### O5-008：`flowResetTime` 无枚举值校验

**文件**：`mutations.go:72`（创建）、`mutations.go:188`（更新）
流量重置周期（如 1=每月）无有效值校验，非法值可能导致定时任务跳过该用户。

---

## 已验证通过

1. **密码安全**：`security.HashPassword` 为 bcrypt；登录验证用 `security.VerifyPassword`；无明文存储
2. **防重名竞态**：`CreateUser` 有 `mapUniqueViolation` 兜底（`repository_mutations.go:64`），并发创建同名用户时第二个返回"用户名已存在"而非 500
3. **管理员保护**：`userCreate` 硬编码 `roleID := 1`，无法创建管理员；`userUpdate`/`userDelete` 对 `roleID == 0` 返回"请不要作死"
4. **密码变更追踪**：管理员重置（`UpdateUserWithPassword`）和用户自助改密（`UpdateUserNameAndPassword`）均更新 `password_changed_at`，强制改密（M-05）逻辑闭环
5. **密码不泄露**：`ListUsers` 返回的 map 不含 `pwd` 字段
6. **配额重置鉴权**：`/api/v1/user/quota/reset` 在中间件要求 admin（H-12 已修复）
7. **订阅分配鉴权**：`/api/v1/user-inbound/list|set` 中间件 + handler 双重 admin 检查（J-001 已修复）
8. **流量上下限**：`parseTrafficLimit` 拒绝负值，`flowMiB` 有 `MaxInt64/bytesPerMiB` 上限检查
9. **配额非负**：`dailyQuotaGB/monthlyQuotaGB < 0` 时拒绝（创建路径）；更新路径透过 `SaveUserQuotaConfigTx`，建议确认其内部也有校验

## 未覆盖（需说明）

- **用户导入导出**：代码库中无用户批量导入/导出功能（仅整库备份恢复），故无相关审计项
- **批量删除用户**：无批量删除接口，仅单删
- **浏览器实际操作**：无浏览器环境，前端用户页交互未验证
- **PostgreSQL 实测**：O5-002 的 PG 行为为代码推断，未实机验证
