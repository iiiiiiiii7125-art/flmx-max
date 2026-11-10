package model

// NodePendingUninstall 是远程卸载的待确认登记（C4-G2 面板半）：
// 管理员发起「删除并卸载」时按节点登记一行，agent 的命令回执
// 只代表已受理（acked），真正的卸载终局由 agent 在最后可负责
// 时刻另行上报（succeeded/failed）。终局到达前节点记录不删、
// 状态经节点列表透出；succeeded 后由面板清扫执行级联删除并清
// 登记，failed/unconfirmed 保留至管理员处置（重试卸载，或不带
// 卸载的删除＝强制删记录）。issued_at 是与 agent 终局上报对账
// 的幂等键（node_id＋issued_at）：重试会刷新 issued_at，旧一
// 轮的迟到终局据此被识别并忽略。
type NodePendingUninstall struct {
	ID          int64  `gorm:"primaryKey;autoIncrement"`
	NodeID      int64  `gorm:"column:node_id;not null;uniqueIndex"`
	State       string `gorm:"type:varchar(20);not null;default:'issued'"`
	Reason      string `gorm:"type:varchar(500);not null;default:''"`
	IssuedAt    int64  `gorm:"column:issued_at;not null"`
	CreatedTime int64  `gorm:"column:created_time;not null"`
	UpdatedTime int64  `gorm:"column:updated_time;not null"`
}

func (NodePendingUninstall) TableName() string { return "node_pending_uninstall" }
