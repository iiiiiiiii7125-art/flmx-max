package model

// NodeUninstall 是一条「待卸载」登记：删除服务器时其 agent 正离线，
// 无法当场下发卸载命令，先按节点 secret 登记在此。agent 之后用同一
// secret 重新连接面板时，面板不把它注册为节点，而是直接下发卸载
// 命令（见 ws 包 serveUninstalledNode）。secret 是节点被删后唯一还
// 能识别这台 agent 的凭据，故以它为唯一键。
type NodeUninstall struct {
	ID          int64  `gorm:"primaryKey;autoIncrement"`
	Secret      string `gorm:"type:varchar(100);not null;uniqueIndex"`
	NodeName    string `gorm:"column:node_name;type:varchar(100);not null;default:''"`
	CreatedTime int64  `gorm:"column:created_time;not null"`
}

func (NodeUninstall) TableName() string { return "node_uninstall" }
