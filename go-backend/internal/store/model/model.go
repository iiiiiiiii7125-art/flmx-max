// Package model defines GORM model structs for all database tables,
// providing a single source of truth for the schema that works
// transparently with both SQLite and PostgreSQL.
package model

import "database/sql"

// ─── Core Business Tables ────────────────────────────────────────────

// User maps to the "user" table. PostgreSQL treats "user" as a reserved
// word, so TableName() is required for correct quoting.
type User struct {
	ID                int64         `gorm:"primaryKey;autoIncrement"`
	User              string        `gorm:"column:user;type:varchar(100);not null"`
	Pwd               string        `gorm:"type:varchar(100);not null"`
	RoleID            int           `gorm:"column:role_id;not null"`
	ExpTime           int64         `gorm:"column:exp_time;not null"`
	Flow              int64         `gorm:"not null"`
	FlowMiB           int64         `gorm:"column:flow_mib;not null;default:0"`
	InFlow            int64         `gorm:"column:in_flow;not null;default:0"`
	OutFlow           int64         `gorm:"column:out_flow;not null;default:0"`
	FlowResetTime     int64         `gorm:"column:flow_reset_time;not null"`
	Num               int           `gorm:"not null"`
	CreatedTime       int64         `gorm:"column:created_time;not null"`
	UpdatedTime       sql.NullInt64 `gorm:"column:updated_time"`
	Status            int           `gorm:"not null"`
	PasswordChangedAt int64         `gorm:"column:password_changed_at;not null;default:0"`
	MaxConn           int           `gorm:"column:max_conn;not null;default:0"`
	// FlowLastResetTime is the unix-ms moment of the last monthly flow
	// reset actually performed by the daily job (R11X-005 due-type
	// catch-up marker). 0 = never recorded (stock/pre-column rows and
	// raw inserts); the reset job resolves 0 via its documented
	// stock-row baseline (see repo.monthlyFlowDue).
	FlowLastResetTime int64 `gorm:"column:flow_last_reset_time;not null;default:0"`
}

func (User) TableName() string { return "user" }

// Forward maps to the "forward" table.
type Forward struct {
	ID                   int64         `gorm:"primaryKey;autoIncrement"`
	UserID               int64         `gorm:"column:user_id;not null"`
	UserName             string        `gorm:"column:user_name;type:varchar(100);not null"`
	Name                 string        `gorm:"type:varchar(100);not null"`
	TunnelID             int64         `gorm:"column:tunnel_id;not null"`
	RemoteAddr           string        `gorm:"column:remote_addr;type:text;not null"`
	Strategy             string        `gorm:"type:varchar(100);not null;default:'fifo'"`
	InFlow               int64         `gorm:"not null;default:0"`
	OutFlow              int64         `gorm:"column:out_flow;not null;default:0"`
	CreatedTime          int64         `gorm:"column:created_time;not null"`
	UpdatedTime          int64         `gorm:"column:updated_time;not null"`
	Status               int           `gorm:"not null"`
	Inx                  int           `gorm:"not null;default:0"`
	SpeedID              sql.NullInt64 `gorm:"column:speed_id"`
	MaxConn              int           `gorm:"column:max_conn;not null;default:0"`
	IPMaxConn            int           `gorm:"column:ip_max_conn;not null;default:0"`
	IPSpeedID            sql.NullInt64 `gorm:"column:ip_speed_id"`
	ProxyProtocol        int           `gorm:"column:proxy_protocol;not null;default:0"`
	ProxyProtocolReceive int           `gorm:"column:proxy_protocol_receive;not null;default:0"`
	ProxyProtocolSend    int           `gorm:"column:proxy_protocol_send;not null;default:0"`
}

func (Forward) TableName() string { return "forward" }

type ForwardPort struct {
	ID        int64          `gorm:"primaryKey;autoIncrement"`
	ForwardID int64          `gorm:"column:forward_id;not null"`
	NodeID    int64          `gorm:"column:node_id;not null"`
	Port      int            `gorm:"not null"`
	InIP      sql.NullString `gorm:"column:in_ip;type:text"`
}

func (ForwardPort) TableName() string { return "forward_port" }

type Node struct {
	ID                      int64          `gorm:"primaryKey;autoIncrement"`
	Name                    string         `gorm:"type:varchar(100);not null"`
	Remark                  sql.NullString `gorm:"column:remark;type:text"`
	ExpiryTime              sql.NullInt64  `gorm:"column:expiry_time"`
	RenewalCycle            sql.NullString `gorm:"column:renewal_cycle;type:varchar(20)"`
	Secret                  string         `gorm:"type:varchar(100);not null"`
	ServerIP                string         `gorm:"column:server_ip;type:varchar(100);not null"`
	ServerIPV4              sql.NullString `gorm:"column:server_ip_v4;type:varchar(100)"`
	ServerIPV6              sql.NullString `gorm:"column:server_ip_v6;type:varchar(100)"`
	ExtraIPs                sql.NullString `gorm:"column:extra_ips;type:text"`
	Port                    string         `gorm:"type:text;not null"`
	InterfaceName           sql.NullString `gorm:"column:interface_name;type:varchar(200)"`
	Version                 sql.NullString `gorm:"type:varchar(100)"`
	HTTP                    int            `gorm:"column:http;not null;default:0"`
	TLS                     int            `gorm:"column:tls;not null;default:0"`
	Socks                   int            `gorm:"not null;default:0"`
	CreatedTime             int64          `gorm:"column:created_time;not null"`
	UpdatedTime             sql.NullInt64  `gorm:"column:updated_time"`
	Status                  int            `gorm:"not null"`
	TCPListenAddr           string         `gorm:"column:tcp_listen_addr;type:varchar(100);not null;default:'[::]'"`
	UDPListenAddr           string         `gorm:"column:udp_listen_addr;type:varchar(100);not null;default:'[::]'"`
	Inx                     int            `gorm:"not null;default:0"`
	IsRemote                int            `gorm:"column:is_remote;default:0"`
	ForwardMode             string         `gorm:"column:forward_mode;type:varchar(20);not null;default:'agent'"`
	RemoteURL               sql.NullString `gorm:"column:remote_url;type:text"`
	RemoteToken             sql.NullString `gorm:"column:remote_token;type:text"`
	RemoteConfig            sql.NullString `gorm:"column:remote_config;type:text"`
	ExpiryReminderDismissed int            `gorm:"column:expiry_reminder_dismissed;not null;default:0"`
	// 移植自 kure29/flyx：节点自选的延迟测试点（JSON），空则跟随全局默认。
	LatencyTargets string `gorm:"column:latency_targets;type:text;not null;default:''"`
}

func (Node) TableName() string { return "node" }

type NodeSSHConfig struct {
	ID          int64          `gorm:"primaryKey;autoIncrement"`
	NodeID      int64          `gorm:"column:node_id;not null;uniqueIndex"`
	Host        string         `gorm:"type:varchar(255);not null"`
	Port        int            `gorm:"not null;default:22"`
	Username    string         `gorm:"type:varchar(100);not null"`
	AuthType    string         `gorm:"column:auth_type;type:varchar(20);not null"`
	Password    sql.NullString `gorm:"type:text"`
	PrivateKey  sql.NullString `gorm:"column:private_key;type:text"`
	Passphrase  sql.NullString `gorm:"type:text"`
	SudoMode    string         `gorm:"column:sudo_mode;type:varchar(20);not null;default:'none'"`
	CreatedTime int64          `gorm:"column:created_time;not null"`
	UpdatedTime int64          `gorm:"column:updated_time;not null"`
}

func (NodeSSHConfig) TableName() string { return "node_ssh_config" }

type NftRuleBinding struct {
	ID          int64  `gorm:"primaryKey;autoIncrement"`
	ForwardID   int64  `gorm:"column:forward_id;not null;uniqueIndex:idx_nft_rule_binding_forward_node;index"`
	NodeID      int64  `gorm:"column:node_id;not null;uniqueIndex:idx_nft_rule_binding_forward_node;index"`
	InPort      int    `gorm:"column:in_port;not null"`
	Protocols   string `gorm:"type:varchar(20);not null;default:'tcp,udp'"`
	TargetAddr  string `gorm:"column:target_addr;type:text;not null"`
	BindIP      string `gorm:"column:bind_ip;type:text;not null;default:''"`
	RuleHash    string `gorm:"column:rule_hash;type:varchar(128);not null;default:''"`
	Status      string `gorm:"type:varchar(20);not null;default:'pending'"`
	LastError   string `gorm:"column:last_error;type:text;not null;default:''"`
	AppliedTime int64  `gorm:"column:applied_time;not null;default:0"`
	CreatedTime int64  `gorm:"column:created_time;not null"`
	UpdatedTime int64  `gorm:"column:updated_time;not null"`
}

func (NftRuleBinding) TableName() string { return "nft_rule_binding" }

// Forward status values. Do not write bare integers; use these constants.
// R5-B3 D1-001: ForwardStatusPendingDelete marks a forward whose data-plane
// rule removal failed (e.g. node offline). The row is hidden from user/admin
// lists, excluded from plan building (so reconcile removes its rule), but
// still occupies its (node_id, port) until the retry job or force-delete
// completes the removal. See the status consumption matrix in the R5-B3 report.
const (
	ForwardStatusPaused        = 0
	ForwardStatusActive        = 1
	ForwardStatusPendingDelete = 2
)

// NftPendingDelete tracks retry bookkeeping for forwards stuck in
// ForwardStatusPendingDelete. The forward row itself (status=2) is the marker;
// this side table holds the retry schedule and last error for observability
// (R5-B3 D7 / 补充 E). Rows are removed when the forward is finally deleted
// (reconcile success or force-delete).
type NftPendingDelete struct {
	ForwardID   int64  `gorm:"primaryKey;column:forward_id"`
	NodeID      int64  `gorm:"column:node_id;not null;index"`
	RetryCount  int    `gorm:"column:retry_count;not null;default:0"`
	NextRetryAt int64  `gorm:"column:next_retry_at;not null;index"`
	LastError   string `gorm:"column:last_error;type:text;not null;default:''"`
	CreatedAt   int64  `gorm:"column:created_at;not null"`
	UpdatedAt   int64  `gorm:"column:updated_at;not null"`
}

func (NftPendingDelete) TableName() string { return "nft_pending_delete" }

type NftCounterState struct {
	ID            int64  `gorm:"primaryKey;autoIncrement"`
	NodeID        int64  `gorm:"column:node_id;not null;uniqueIndex:idx_nft_counter_state_key;index"`
	ForwardID     int64  `gorm:"column:forward_id;not null;uniqueIndex:idx_nft_counter_state_key;index"`
	Protocol      string `gorm:"type:varchar(10);not null;uniqueIndex:idx_nft_counter_state_key"`
	Direction     string `gorm:"type:varchar(20);not null;uniqueIndex:idx_nft_counter_state_key"`
	RuleHash      string `gorm:"column:rule_hash;type:varchar(128);not null;default:''"`
	Bytes         int64  `gorm:"not null;default:0"`
	Packets       int64  `gorm:"not null;default:0"`
	CollectedTime int64  `gorm:"column:collected_time;not null;default:0"`
	CreatedTime   int64  `gorm:"column:created_time;not null"`
	UpdatedTime   int64  `gorm:"column:updated_time;not null"`
}

func (NftCounterState) TableName() string { return "nft_counter_state" }

type SpeedLimit struct {
	ID          int64          `gorm:"primaryKey;autoIncrement"`
	Name        string         `gorm:"type:varchar(100);not null"`
	Speed       int            `gorm:"not null"`
	TunnelID    sql.NullInt64  `gorm:"column:tunnel_id"`
	TunnelName  sql.NullString `gorm:"column:tunnel_name;type:varchar(100)"`
	CreatedTime int64          `gorm:"column:created_time;not null"`
	UpdatedTime sql.NullInt64  `gorm:"column:updated_time"`
	Status      int            `gorm:"not null"`
}

func (SpeedLimit) TableName() string { return "speed_limit" }

type StatisticsFlow struct {
	ID          int64  `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID      int64  `gorm:"column:user_id;not null" json:"userId"`
	Flow        int64  `gorm:"not null" json:"flow"`
	TotalFlow   int64  `gorm:"column:total_flow;not null" json:"totalFlow"`
	Time        string `gorm:"type:varchar(100);not null" json:"time"`
	CreatedTime int64  `gorm:"column:created_time;not null" json:"-"`
}

func (StatisticsFlow) TableName() string { return "statistics_flow" }

type Tunnel struct {
	ID              int64          `gorm:"primaryKey;autoIncrement"`
	Name            string         `gorm:"type:varchar(100);not null"`
	TrafficRatio    float64        `gorm:"column:traffic_ratio;not null;default:1.0"`
	Type            int            `gorm:"not null"`
	Protocol        string         `gorm:"type:varchar(10);not null;default:'tls'"`
	Flow            int64          `gorm:"not null"`
	CreatedTime     int64          `gorm:"column:created_time;not null"`
	UpdatedTime     int64          `gorm:"column:updated_time;not null"`
	Status          int            `gorm:"not null"`
	InIP            sql.NullString `gorm:"column:in_ip;type:text"`
	Inx             int            `gorm:"not null;default:0"`
	IPPreference    string         `gorm:"column:ip_preference;type:varchar(10);not null;default:''"`
	ProbeTargetHost string         `gorm:"column:probe_target_host;type:text;not null;default:''"`
	ProbeTargetPort int            `gorm:"column:probe_target_port;not null;default:0"`
	// Engine runs the tunnel's forwards: "" = the agent's forwarder,
	// "realm" = realm on the entry nodes (port forward tunnels only).
	Engine string `gorm:"column:engine;type:varchar(20);not null;default:''"`
}

func (Tunnel) TableName() string { return "tunnel" }

type UserQuota struct {
	UserID           int64  `gorm:"column:user_id;primaryKey"`
	DailyLimitGB     int64  `gorm:"column:daily_limit_gb;not null;default:0"`
	MonthlyLimitGB   int64  `gorm:"column:monthly_limit_gb;not null;default:0"`
	DailyUsedBytes   int64  `gorm:"column:daily_used_bytes;not null;default:0"`
	MonthlyUsedBytes int64  `gorm:"column:monthly_used_bytes;not null;default:0"`
	DayKey           int64  `gorm:"column:day_key;not null;default:0"`
	MonthKey         int64  `gorm:"column:month_key;not null;default:0"`
	DisabledByQuota  int    `gorm:"column:disabled_by_quota;not null;default:0"`
	DisabledAt       int64  `gorm:"column:disabled_at;not null;default:0"`
	PausedForwardIDs string `gorm:"column:paused_forward_ids;type:text;not null;default:''"`
	CreatedTime      int64  `gorm:"column:created_time;not null"`
	UpdatedTime      int64  `gorm:"column:updated_time;not null"`
}

func (UserQuota) TableName() string { return "user_quota" }

type ChainTunnel struct {
	ID        int64          `gorm:"primaryKey;autoIncrement"`
	TunnelID  int64          `gorm:"column:tunnel_id;not null"`
	ChainType string         `gorm:"column:chain_type;type:varchar(10);not null"`
	NodeID    int64          `gorm:"column:node_id;not null"`
	Port      sql.NullInt64  `gorm:"column:port"`
	Strategy  sql.NullString `gorm:"type:varchar(10)"`
	Inx       sql.NullInt64  `gorm:"column:inx"`
	Protocol  sql.NullString `gorm:"type:varchar(10)"`
	ConnectIP sql.NullString `gorm:"column:connect_ip;type:varchar(45)"`
}

func (ChainTunnel) TableName() string { return "chain_tunnel" }

type UserTunnel struct {
	ID            int64         `gorm:"primaryKey;autoIncrement"`
	UserID        int64         `gorm:"column:user_id;not null;uniqueIndex:idx_user_tunnel_unique"`
	TunnelID      int64         `gorm:"column:tunnel_id;not null;uniqueIndex:idx_user_tunnel_unique"`
	SpeedID       sql.NullInt64 `gorm:"column:speed_id"`
	Num           int           `gorm:"not null"`
	Flow          int64         `gorm:"not null"`
	FlowMiB       int64         `gorm:"column:flow_mib;not null;default:0"`
	InFlow        int64         `gorm:"column:in_flow;not null;default:0"`
	OutFlow       int64         `gorm:"column:out_flow;not null;default:0"`
	FlowResetTime int64         `gorm:"column:flow_reset_time;not null"`
	ExpTime       int64         `gorm:"column:exp_time;not null"`
	Status        int           `gorm:"not null"`
	// FlowLastResetTime: see User.FlowLastResetTime (R11X-005 marker).
	FlowLastResetTime int64 `gorm:"column:flow_last_reset_time;not null;default:0"`
}

func (UserTunnel) TableName() string { return "user_tunnel" }

// UserInbound grants a user access to a proxy inbound for subscription.
// RelayID = 0 means the inbound itself (direct endpoints);
// RelayID > 0 means a specific relay (tunnel forward) of that inbound.
type UserInbound struct {
	ID        int64 `gorm:"primaryKey;autoIncrement"`
	UserID    int64 `gorm:"column:user_id;not null;uniqueIndex:idx_user_inbound_relay_unique"`
	InboundID int64 `gorm:"column:inbound_id;not null;uniqueIndex:idx_user_inbound_relay_unique"`
	RelayID   int64 `gorm:"column:relay_id;not null;default:0;uniqueIndex:idx_user_inbound_relay_unique"`
}

func (UserInbound) TableName() string { return "user_inbound" }

// UserSubscription grants a user access to a managed subscription
// (1.0.12: "订阅分配" binds users to subscriptions from 订阅管理，
// so traffic, expiry and deletion are scoped per subscription).
type UserSubscription struct {
	ID             int64 `gorm:"primaryKey;autoIncrement"`
	UserID         int64 `gorm:"column:user_id;not null;uniqueIndex:idx_user_subscription_unique"`
	SubscriptionID int64 `gorm:"column:subscription_id;not null;uniqueIndex:idx_user_subscription_unique"`
	CreatedTime    int64 `gorm:"column:created_time;not null"`
}

func (UserSubscription) TableName() string { return "user_subscription" }

type TunnelGroup struct {
	ID          int64  `gorm:"primaryKey;autoIncrement"`
	Name        string `gorm:"type:varchar(100);not null;uniqueIndex:idx_tunnel_group_name"`
	CreatedTime int64  `gorm:"column:created_time;not null"`
	UpdatedTime int64  `gorm:"column:updated_time;not null"`
	Status      int    `gorm:"not null"`
}

func (TunnelGroup) TableName() string { return "tunnel_group" }

type UserGroup struct {
	ID          int64  `gorm:"primaryKey;autoIncrement"`
	Name        string `gorm:"type:varchar(100);not null;uniqueIndex:idx_user_group_name"`
	CreatedTime int64  `gorm:"column:created_time;not null"`
	UpdatedTime int64  `gorm:"column:updated_time;not null"`
	Status      int    `gorm:"not null"`
}

func (UserGroup) TableName() string { return "user_group" }

type TunnelGroupTunnel struct {
	ID            int64 `gorm:"primaryKey;autoIncrement"`
	TunnelGroupID int64 `gorm:"column:tunnel_group_id;not null;uniqueIndex:idx_tunnel_group_tunnel_unique"`
	TunnelID      int64 `gorm:"column:tunnel_id;not null;uniqueIndex:idx_tunnel_group_tunnel_unique"`
	CreatedTime   int64 `gorm:"column:created_time;not null"`
}

func (TunnelGroupTunnel) TableName() string { return "tunnel_group_tunnel" }

type UserGroupUser struct {
	ID          int64 `gorm:"primaryKey;autoIncrement"`
	UserGroupID int64 `gorm:"column:user_group_id;not null;uniqueIndex:idx_user_group_user_unique"`
	UserID      int64 `gorm:"column:user_id;not null;uniqueIndex:idx_user_group_user_unique"`
	CreatedTime int64 `gorm:"column:created_time;not null"`
}

func (UserGroupUser) TableName() string { return "user_group_user" }

type GroupPermission struct {
	ID            int64 `gorm:"primaryKey;autoIncrement"`
	UserGroupID   int64 `gorm:"column:user_group_id;not null;uniqueIndex:idx_group_permission_unique"`
	TunnelGroupID int64 `gorm:"column:tunnel_group_id;not null;uniqueIndex:idx_group_permission_unique"`
	CreatedTime   int64 `gorm:"column:created_time;not null"`
}

func (GroupPermission) TableName() string { return "group_permission" }

type GroupPermissionGrant struct {
	ID             int64 `gorm:"primaryKey;autoIncrement"`
	UserGroupID    int64 `gorm:"column:user_group_id;not null;uniqueIndex:idx_group_permission_grant_unique"`
	TunnelGroupID  int64 `gorm:"column:tunnel_group_id;not null;uniqueIndex:idx_group_permission_grant_unique"`
	UserTunnelID   int64 `gorm:"column:user_tunnel_id;not null;uniqueIndex:idx_group_permission_grant_unique"`
	CreatedByGroup int   `gorm:"column:created_by_group;not null;default:0"`
	CreatedTime    int64 `gorm:"column:created_time;not null"`
}

func (GroupPermissionGrant) TableName() string { return "group_permission_grant" }

// MonitorPermission grants a non-admin user access to monitoring endpoints.
// One row per user_id.
type MonitorPermission struct {
	ID          int64 `gorm:"primaryKey;autoIncrement" json:"id"`
	UserID      int64 `gorm:"column:user_id;not null;uniqueIndex:idx_monitor_permission_user" json:"userId"`
	CreatedTime int64 `gorm:"column:created_time;not null" json:"createdTime"`
}

func (MonitorPermission) TableName() string { return "monitor_permission" }

type ViteConfig struct {
	ID    int64  `gorm:"primaryKey;autoIncrement" json:"id"`
	Name  string `gorm:"type:varchar(200);not null;uniqueIndex" json:"name"`
	Value string `gorm:"type:text;not null" json:"value"`
	Time  int64  `gorm:"not null" json:"time"`
}

func (ViteConfig) TableName() string { return "vite_config" }

type Announcement struct {
	ID          int64         `gorm:"primaryKey;autoIncrement" json:"id"`
	Content     string        `gorm:"type:text;not null" json:"content"`
	Enabled     int           `gorm:"not null;default:1" json:"enabled"`
	CreatedTime int64         `gorm:"column:created_time;not null" json:"created_time"`
	UpdatedTime sql.NullInt64 `gorm:"column:updated_time" json:"updated_time,omitempty"`
}

func (Announcement) TableName() string { return "announcement" }

type SchemaVersion struct {
	Version int `gorm:"not null;default:0"`
}

func (SchemaVersion) TableName() string { return "schema_version" }

type PeerShare struct {
	ID             int64  `gorm:"primaryKey;autoIncrement" json:"id"`
	Name           string `gorm:"type:text;not null" json:"name"`
	NodeID         int64  `gorm:"column:node_id;not null" json:"nodeId"`
	Token          string `gorm:"type:text;not null;uniqueIndex" json:"token"`
	MaxBandwidth   int64  `gorm:"column:max_bandwidth;default:0" json:"maxBandwidth"`
	ExpiryTime     int64  `gorm:"column:expiry_time;default:0" json:"expiryTime"`
	PortRangeStart int    `gorm:"column:port_range_start;default:0" json:"portRangeStart"`
	PortRangeEnd   int    `gorm:"column:port_range_end;default:0" json:"portRangeEnd"`
	CurrentFlow    int64  `gorm:"column:current_flow;default:0" json:"currentFlow"`
	IsActive       int    `gorm:"column:is_active;default:1" json:"isActive"`
	CreatedTime    int64  `gorm:"column:created_time;not null" json:"createdTime"`
	UpdatedTime    int64  `gorm:"column:updated_time;not null" json:"updatedTime"`
	AllowedDomains string `gorm:"column:allowed_domains;type:text;default:''" json:"allowedDomains"`
	AllowedIPs     string `gorm:"column:allowed_ips;type:text;default:''" json:"allowedIps"`
	// D3-04: 本 share 的 middle/exit 角色服务凭据（每 share 独立）。
	// 角色服务 handler 恒带此 auth，消费方经 Connect 取得后在建链时
	// 写入 relay connector。存量 share 由 repo 启动回填 + handler 懒
	// 补发，见 migration_fix132_v3.go 的 backfill 与
	// ensurePeerShareRelayCredentials。
	RelayUsername string `gorm:"column:relay_username;type:text;default:''" json:"relayUsername"`
	RelayPassword string `gorm:"column:relay_password;type:text;default:''" json:"relayPassword"`
}

func (PeerShare) TableName() string { return "peer_share" }

type PeerShareRuntime struct {
	ID            int64  `gorm:"primaryKey;autoIncrement"`
	ShareID       int64  `gorm:"column:share_id;not null;index:idx_peer_share_runtime_share_node_status"`
	NodeID        int64  `gorm:"column:node_id;not null;index:idx_peer_share_runtime_share_node_status"`
	ReservationID string `gorm:"column:reservation_id;type:text;not null;uniqueIndex"`
	// D3-05: resource_key 的唯一性是 (share_id, resource_key) 复合口径，
	// 由版本化迁移 fix132_v3 维护（与 R5-B4 同规矩：唯一索引不放 model
	// tag，避免 AutoMigrate 在存量数据上静默建索引失败）。旧单列全局
	// 唯一索引会让第二个消费方面板必然撞键。
	ResourceKey    string `gorm:"column:resource_key;type:text;not null"`
	BindingID      string `gorm:"column:binding_id;type:text;not null;default:'';index:idx_peer_share_runtime_binding_id"`
	Role           string `gorm:"type:text;not null;default:''"`
	ChainName      string `gorm:"column:chain_name;type:text;not null;default:''"`
	ServiceName    string `gorm:"column:service_name;type:text;not null;default:''"`
	Protocol       string `gorm:"type:text;not null;default:'tls'"`
	Strategy       string `gorm:"type:text;not null;default:'round'"`
	Port           int    `gorm:"not null;default:0"`
	Target         string `gorm:"type:text;not null;default:''"`
	Applied        int    `gorm:"not null;default:0"`
	ReleasePending int    `gorm:"column:release_pending;not null;default:0"`
	Status         int    `gorm:"not null;default:1;index:idx_peer_share_runtime_share_node_status"`
	CreatedTime    int64  `gorm:"column:created_time;not null"`
	UpdatedTime    int64  `gorm:"column:updated_time;not null"`
}

func (PeerShareRuntime) TableName() string { return "peer_share_runtime" }

type FederationTunnelBinding struct {
	ID              int64  `gorm:"primaryKey;autoIncrement"`
	TunnelID        int64  `gorm:"column:tunnel_id;not null;uniqueIndex:idx_federation_tunnel_binding_unique;index:idx_federation_tunnel_binding_tunnel"`
	NodeID          int64  `gorm:"column:node_id;not null;uniqueIndex:idx_federation_tunnel_binding_unique"`
	ChainType       int    `gorm:"column:chain_type;not null;uniqueIndex:idx_federation_tunnel_binding_unique"`
	HopInx          int    `gorm:"column:hop_inx;not null;default:0;uniqueIndex:idx_federation_tunnel_binding_unique"`
	RemoteURL       string `gorm:"column:remote_url;type:text;not null"`
	ResourceKey     string `gorm:"column:resource_key;type:text;not null;uniqueIndex"`
	RemoteBindingID string `gorm:"column:remote_binding_id;type:text;not null"`
	AllocatedPort   int    `gorm:"column:allocated_port;not null"`
	Status          int    `gorm:"not null;default:1;index:idx_federation_tunnel_binding_tunnel"`
	CreatedTime     int64  `gorm:"column:created_time;not null"`
	UpdatedTime     int64  `gorm:"column:updated_time;not null"`
}

func (FederationTunnelBinding) TableName() string { return "federation_tunnel_binding" }

// ─── Backup / Import-Export Structs ──────────────────────────────────
// These are not GORM models; they define the JSON wire format for the
// backup/restore API and MUST keep their existing json tags unchanged.

// BackupData represents the full backup structure.
type BackupData struct {
	Version      string              `json:"version"`
	ExportedAt   int64               `json:"exportedAt"`
	Users        []UserBackup        `json:"users,omitempty"`
	Nodes        []NodeBackup        `json:"nodes,omitempty"`
	Tunnels      []TunnelBackup      `json:"tunnels,omitempty"`
	Forwards     []ForwardBackup     `json:"forwards,omitempty"`
	UserTunnels  []UserTunnelBackup  `json:"userTunnels,omitempty"`
	SpeedLimits  []SpeedLimitBackup  `json:"speedLimits,omitempty"`
	TunnelGroups []TunnelGroupBackup `json:"tunnelGroups,omitempty"`
	UserGroups   []UserGroupBackup   `json:"userGroups,omitempty"`
	Permissions  []PermissionBackup  `json:"permissions,omitempty"`
	Configs      map[string]string   `json:"configs,omitempty"`
	UserInbounds []UserInboundBackup `json:"userInbounds,omitempty"`
	// O7-007：announcement 整表备份（公告丢失修复）
	Announcements []AnnouncementBackup `json:"announcements,omitempty"`
	// O7-008：monitor_permission 整表备份（监控权限丢失修复）
	MonitorPermissions []MonitorPermissionBackup `json:"monitorPermissions,omitempty"`
	// B4/F-004：user_subscription 整表备份（1.0.12 订阅分配关系；
	// 此前整表漏列，恢复后全部分配丢失）
	UserSubscriptions []UserSubscriptionBackup `json:"userSubscriptions,omitempty"`
	// B4/F-030：service_monitor 监控定义整表备份（此前只有结果表口径，
	// 定义未备，恢复后监控项全丢）
	ServiceMonitors []ServiceMonitorBackup `json:"serviceMonitors,omitempty"`
	// FIX132-C（A3-05/BC-28）：node_ssh_config 与 peer_share 定义进
	// 备份（随 nodes 段导出/导入）。此前恢复出的实例节点在、SSH
	// 凭据与联邦共享定义不在，子系统静默不可用；备份文件因此与
	// 节点 secret/订阅 token 同敏感级，文档与导出提示须明示。
	NodeSSHConfigs []NodeSSHConfigBackup `json:"nodeSSHConfigs,omitempty"`
	PeerShares     []PeerShareBackup     `json:"peerShares,omitempty"`
	// Inbounds carries node kernels, their inbounds (imported nodes too),
	// relay links and traffic counter baselines; restored as one unit.
	Inbounds *InboundsBackup `json:"inbounds,omitempty"`
}

// NodeSSHConfigBackup 是 A3-05 补齐的节点 SSH 配置备份结构
// （字段对照 model.NodeSSHConfig；以 node_id 为业务键恢复）。
type NodeSSHConfigBackup struct {
	NodeID      int64  `json:"nodeId"`
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Username    string `json:"username"`
	AuthType    string `json:"authType"`
	Password    string `json:"password,omitempty"`
	PrivateKey  string `json:"privateKey,omitempty"`
	Passphrase  string `json:"passphrase,omitempty"`
	SudoMode    string `json:"sudoMode"`
	CreatedTime int64  `json:"createdTime"`
}

// PeerShareBackup 是 A3-05 补齐的面板共享定义备份结构
// （字段对照 model.PeerShare；runtime/resource 等运行时表仍按
// 登记排除，不随备份）。
type PeerShareBackup struct {
	ID             int64  `json:"id"`
	Name           string `json:"name"`
	NodeID         int64  `json:"nodeId"`
	Token          string `json:"token"`
	MaxBandwidth   int64  `json:"maxBandwidth"`
	ExpiryTime     int64  `json:"expiryTime"`
	PortRangeStart int    `json:"portRangeStart"`
	PortRangeEnd   int    `json:"portRangeEnd"`
	CurrentFlow    int64  `json:"currentFlow"`
	IsActive       int    `json:"isActive"`
	CreatedTime    int64  `json:"createdTime"`
	UpdatedTime    int64  `json:"updatedTime,omitempty"`
	AllowedDomains string `json:"allowedDomains,omitempty"`
	AllowedIPs     string `json:"allowedIps,omitempty"`
	// FIX2-B（V3-01）：FIX-I 的 relay 凭据列随备份往返。缺失时全新
	// 恢复后提供方补发新凭据、消费方存量凭据失配至其重新 Connect
	// 才自愈，且回执无提示。
	RelayUsername string `json:"relayUsername,omitempty"`
	RelayPassword string `json:"relayPassword,omitempty"`
}

type InboundsBackup struct {
	Kernels       []KernelBackup         `json:"kernels"`
	Inbounds      []InboundBackup        `json:"inbounds"`
	Relays        []InboundRelayBackup   `json:"relays"`
	Counters      []InboundCounterBackup `json:"counters,omitempty"`
	Subscriptions []SubscriptionBackup   `json:"subscriptions,omitempty"`
	// FIX132-C（A3-04）：订阅计数基线随备份（与入站基线 Counters
	// 同等待遇）；恢复时订阅 id 经 subRemap 按 token 落位。
	SubscriptionCounters []SubscriptionCounterBackup `json:"subscriptionCounters,omitempty"`
	Sources              []RemoteSourceBackup        `json:"sources,omitempty"`
	// Users are shared users from 3.0.10 backups; imported as share
	// subscriptions.
	Users []SubUserBackup `json:"users,omitempty"`
}

type SubscriptionBackup struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Kind        string `json:"kind"`
	Token       string `json:"token"`
	UUID        string `json:"uuid"`
	Password    string `json:"password"`
	SourceAll   int    `json:"sourceAll"`
	InboundIDs  string `json:"inboundIds"`
	SourceIDs   string `json:"sourceIds,omitempty"`
	Options     string `json:"options"`
	ExpireTime  int64  `json:"expireTime"`
	Enabled     int    `json:"enabled"`
	Remark      string `json:"remark"`
	Inx         int    `json:"inx"`
	CreatedTime int64  `json:"createdTime"`
	// FIX2-B（D4-06）：待删墓碑随备份往返，恢复后不得复活成可拉取
	// 的正常订阅（openSubscription 对 PendingDelete=1 返回 404）。
	PendingDelete int `json:"pendingDelete,omitempty"`
	// Traffic (share subscriptions).
	UploadBytes      int64 `json:"uploadBytes,omitempty"`
	DownloadBytes    int64 `json:"downloadBytes,omitempty"`
	TrafficLimit     int64 `json:"trafficLimit,omitempty"`
	ResetDay         int   `json:"resetDay,omitempty"`
	TrafficResetTime int64 `json:"trafficResetTime,omitempty"`
	TrafficSince     int64 `json:"trafficSince,omitempty"`
}

type SubUserBackup struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Token       string `json:"token"`
	UUID        string `json:"uuid"`
	Password    string `json:"password"`
	InboundIDs  string `json:"inboundIds"`
	ExpireTime  int64  `json:"expireTime"`
	Enabled     int    `json:"enabled"`
	Remark      string `json:"remark"`
	CreatedTime int64  `json:"createdTime"`
}

type KernelBackup struct {
	ID          int64  `json:"id"`
	NodeID      int64  `json:"nodeId"`
	KernelType  string `json:"kernelType"`
	Version     string `json:"version"`
	Enabled     int    `json:"enabled"`
	CreatedTime int64  `json:"createdTime"`
	// FIX2-B（D4-05/V3-02）：已装版本随备份往返。此前 DTO 无此字段，
	// 导入 item 恒空而 DoUpdates 含 installed_version——全新恢复丢
	// 值、同实例重复导入更会把已有值主动清空。
	InstalledVersion string `json:"installedVersion,omitempty"`
}

type InboundBackup struct {
	ID                  int64  `json:"id"`
	KernelID            int64  `json:"kernelId"`
	NodeID              int64  `json:"nodeId"`
	Name                string `json:"name"`
	Protocol            string `json:"protocol"`
	Listen              string `json:"listen"`
	Port                int    `json:"port"`
	Transport           string `json:"transport"`
	Security            string `json:"security"`
	Settings            string `json:"settings"`
	Extra               string `json:"extra"`
	ShareAddressMode    string `json:"shareAddressMode,omitempty"`
	ShareHost           string `json:"shareHost"`
	Remark              string `json:"remark"`
	RelayOnly           int    `json:"relayOnly"`
	AcceptProxyProtocol int    `json:"acceptProxyProtocol"`
	RelayWhitelist      int    `json:"relayWhitelist"`
	WhitelistExtra      string `json:"whitelistExtra"`
	Enabled             int    `json:"enabled"`
	UploadBytes         int64  `json:"uploadBytes"`
	DownloadBytes       int64  `json:"downloadBytes"`
	Inx                 int    `json:"inx"`
	CreatedTime         int64  `json:"createdTime"`
	SourceID            int64  `json:"sourceId,omitempty"`
	OutboundID          int64  `json:"outboundId,omitempty"`
	SplitRules          string `json:"splitRules,omitempty"`
	OutboundIPStrategy  string `json:"outboundIpStrategy,omitempty"`
	// MeteringEnabled is the origin inbound's SS subscription-metering
	// switch. Companion rows themselves are derived state and are never
	// exported (restore re-materializes them via reconcile), so there is
	// deliberately no companion marker here.
	MeteringEnabled int `json:"meteringEnabled,omitempty"`
}

type RemoteSourceBackup struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	URL           string `json:"url"`
	IntervalHours int    `json:"intervalHours"`
	RelayOnly     int    `json:"relayOnly"`
	Kind          string `json:"kind,omitempty"`
	Role          string `json:"role,omitempty"`
	Enabled       int    `json:"enabled"`
	CreatedTime   int64  `json:"createdTime"`
	// FIX2-B（D4-04）：机场套餐 Subscription-Userinfo 随备份往返；
	// file 源无刷新自愈，丢失即永久。
	Userinfo string `json:"userinfo,omitempty"`
}

type InboundRelayBackup struct {
	ID          int64  `json:"id"`
	InboundID   int64  `json:"inboundId"`
	ForwardID   int64  `json:"forwardId"`
	Name        string `json:"name"`
	ShareHost   string `json:"shareHost"`
	Managed     int    `json:"managed"`
	CreatedTime int64  `json:"createdTime"`
}

type InboundCounterBackup struct {
	InboundID int64  `json:"inboundId"`
	Protocol  string `json:"protocol"`
	Direction string `json:"direction"`
	Epoch     string `json:"epoch"`
	Bytes     int64  `json:"bytes"`
}

// SubscriptionCounterBackup 是 A3-04 补齐的订阅计数基线备份结构
// （字段对照 model.SubscriptionCounter；SubscriptionID 为备份侧 id，
// 恢复时经 subRemap 按 token 重映射落位）。
type SubscriptionCounterBackup struct {
	NodeID         int64  `json:"nodeId"`
	SubscriptionID int64  `json:"subscriptionId"`
	Direction      string `json:"direction"`
	Epoch          string `json:"epoch"`
	Bytes          int64  `json:"bytes"`
}

type UserBackup struct {
	ID                int64  `json:"id"`
	User              string `json:"user"`
	Pwd               string `json:"pwd"`
	RoleID            int    `json:"roleId"`
	ExpTime           int64  `json:"expTime"`
	Flow              int64  `json:"flow"`
	FlowMiB           int64  `json:"flowMiB,omitempty"`
	InFlow            int64  `json:"inFlow"`
	OutFlow           int64  `json:"outFlow"`
	FlowResetTime     int64  `json:"flowResetTime"`
	DailyQuotaGB      int64  `json:"dailyQuotaGB,omitempty"`
	MonthlyQuotaGB    int64  `json:"monthlyQuotaGB,omitempty"`
	DisabledByQuota   int    `json:"disabledByQuota,omitempty"`
	QuotaDisabledAt   int64  `json:"quotaDisabledAt,omitempty"`
	// FIX132-C（D4-01）：配额已用量与暂停清单保真。旧备份无此四字段
	// 时导入走旧口径（导入日窗口 + 已用量清零）；新备份原样恢复
	// 窗口键与已用量（跨日恢复由配额视图的滚动归零兜底），暂停清单
	// 是配额到期解封的唯一依据，丢失即永不自动解封。
	DailyUsedBytes    int64  `json:"dailyUsedBytes,omitempty"`
	MonthlyUsedBytes  int64  `json:"monthlyUsedBytes,omitempty"`
	PausedForwardIDs  string `json:"pausedForwardIds,omitempty"`
	QuotaDayKey       int64  `json:"quotaDayKey,omitempty"`
	QuotaMonthKey     int64  `json:"quotaMonthKey,omitempty"`
	Num               int    `json:"num"`
	CreatedTime       int64  `json:"createdTime"`
	UpdatedTime       int64  `json:"updatedTime,omitempty"`
	Status            int    `json:"status"`
	PasswordChangedAt int64  `json:"passwordChangedAt,omitempty"`
	MaxConn           int    `json:"maxConn,omitempty"`
}

type UserInboundBackup struct {
	ID        int64 `json:"id"`
	UserID    int64 `json:"userId"`
	InboundID int64 `json:"inboundId"`
	RelayID   int64 `json:"relayId"`
}

// UserSubscriptionBackup 是 B4/F-004 补齐的订阅分配备份结构
// （字段对照 model.UserSubscription）。
type UserSubscriptionBackup struct {
	ID             int64 `json:"id"`
	UserID         int64 `json:"userId"`
	SubscriptionID int64 `json:"subscriptionId"`
	CreatedTime    int64 `json:"createdTime"`
}

// ServiceMonitorBackup 是 B4/F-030 补齐的监控定义备份结构
// （字段对照 model.ServiceMonitor；结果表 service_monitor_result
// 是遥测时序，不在备份范围）。
type ServiceMonitorBackup struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Type        string `json:"type"`
	Target      string `json:"target"`
	IntervalSec int    `json:"intervalSec"`
	TimeoutSec  int    `json:"timeoutSec"`
	NodeID      int64  `json:"nodeId"`
	Enabled     int    `json:"enabled"`
	CreatedTime int64  `json:"createdTime"`
	UpdatedTime int64  `json:"updatedTime,omitempty"`
}

// AnnouncementBackup 是 O7-007 补齐的公告备份结构（字段对照 model.Announcement）。
type AnnouncementBackup struct {
	ID          int64  `json:"id"`
	Content     string `json:"content"`
	Enabled     int    `json:"enabled"`
	CreatedTime int64  `json:"createdTime"`
	UpdatedTime int64  `json:"updatedTime,omitempty"`
}

// MonitorPermissionBackup 是 O7-008 补齐的监控权限备份结构（字段对照 model.MonitorPermission）。
type MonitorPermissionBackup struct {
	ID          int64 `json:"id"`
	UserID      int64 `json:"userId"`
	CreatedTime int64 `json:"createdTime"`
}

type NodeBackup struct {
	ID             int64  `json:"id"`
	Name           string `json:"name"`
	Remark         string `json:"remark,omitempty"`
	ExpiryTime     int64  `json:"expiryTime,omitempty"`
	RenewalCycle   string `json:"renewalCycle,omitempty"`
	Secret         string `json:"secret"`
	ServerIP       string `json:"serverIp"`
	ServerIPv4     string `json:"serverIpV4,omitempty"`
	ServerIPv6     string `json:"serverIpV6,omitempty"`
	ExtraIPs       string `json:"extraIPs,omitempty"`
	Port           string `json:"port"`
	InterfaceName  string `json:"interfaceName,omitempty"`
	Version        string `json:"version,omitempty"`
	HTTP           int    `json:"http"`
	TLS            int    `json:"tls"`
	Socks          int    `json:"socks"`
	CreatedTime    int64  `json:"createdTime"`
	UpdatedTime    int64  `json:"updatedTime,omitempty"`
	Status         int    `json:"status"`
	TCPListenAddr  string `json:"tcpListenAddr"`
	UDPListenAddr  string `json:"udpListenAddr"`
	Inx            int    `json:"inx"`
	IsRemote       int    `json:"isRemote"`
	RemoteURL      string `json:"remoteUrl,omitempty"`
	RemoteToken    string `json:"remoteToken,omitempty"`
	RemoteConfig   string `json:"remoteConfig,omitempty"`
	LatencyTargets string `json:"latencyTargets,omitempty"`
	ForwardMode    string `json:"forwardMode,omitempty"`
	// O7-006：恢复后保留已忽略的到期提醒状态
	ExpiryReminderDismissed int `json:"expiryReminderDismissed,omitempty"`
}

type TunnelBackup struct {
	ID              int64               `json:"id"`
	Name            string              `json:"name"`
	TrafficRatio    float64             `json:"trafficRatio"`
	Type            int                 `json:"type"`
	Protocol        string              `json:"protocol"`
	Flow            int64               `json:"flow"`
	CreatedTime     int64               `json:"createdTime"`
	UpdatedTime     int64               `json:"updatedTime"`
	Status          int                 `json:"status"`
	InIP            string              `json:"inIp,omitempty"`
	Inx             int                 `json:"inx"`
	IPPreference    string              `json:"ipPreference,omitempty"`
	ProbeTargetHost string              `json:"probeTargetHost,omitempty"`
	ProbeTargetPort int                 `json:"probeTargetPort,omitempty"`
	Engine          string              `json:"engine,omitempty"`
	ChainTunnels    []ChainTunnelBackup `json:"chainTunnels,omitempty"`
}

type ChainTunnelBackup struct {
	ID        int64  `json:"id"`
	TunnelID  int64  `json:"tunnelId"`
	ChainType string `json:"chainType"`
	NodeID    int64  `json:"nodeId"`
	// BUG-A2-3：port/strategy/inx/protocol/connect_ip 在 chain_tunnel 中
	// 均可空，必须以指针携带 NULL 语义。旧实现用普通 int/string +
	// omitempty 导出，NULL 与零值不可区分，导入时一律写成有效值
	// （port 写 0），两条同节点 NULL 端口链路即撞 (node_id, port)
	// 唯一索引导致整单恢复回滚。旧备份的普通数值可正常反序列化为
	// 指针；旧备份缺失的字段按 NULL 恢复（旧导出已把 NULL 与 0/''
	// 合并落盘，无法再区分——这些列的 0/'' 本就不是合法业务值，
	// inx=0 除外，该一位精度损失为旧格式既成事实）。
	Port      *int    `json:"port,omitempty"`
	Strategy  *string `json:"strategy,omitempty"`
	Inx       *int    `json:"inx,omitempty"`
	Protocol  *string `json:"protocol,omitempty"`
	ConnectIP *string `json:"connectIp,omitempty"`
}

type ForwardBackup struct {
	ID                   int64                `json:"id"`
	UserID               int64                `json:"userId"`
	UserName             string               `json:"userName"`
	Name                 string               `json:"name"`
	TunnelID             int64                `json:"tunnelId"`
	RemoteAddr           string               `json:"remoteAddr"`
	Strategy             string               `json:"strategy"`
	InFlow               int64                `json:"inFlow"`
	OutFlow              int64                `json:"outFlow"`
	CreatedTime          int64                `json:"createdTime"`
	UpdatedTime          int64                `json:"updatedTime"`
	Status               int                  `json:"status"`
	Inx                  int                  `json:"inx"`
	SpeedID              *int64               `json:"speedId,omitempty"`
	MaxConn              int                  `json:"maxConn,omitempty"`
	IPMaxConn            int                  `json:"ipMaxConn,omitempty"`
	IPSpeedID            *int64               `json:"ipSpeedId,omitempty"`
	ForwardPorts         *[]ForwardPortBackup `json:"forwardPorts,omitempty"`
	ProxyProtocol        int                  `json:"proxyProtocol"`
	ProxyProtocolReceive int                  `json:"proxyProtocolReceive,omitempty"`
	ProxyProtocolSend    int                  `json:"proxyProtocolSend,omitempty"`
}

type ForwardPortBackup struct {
	NodeID int64  `json:"nodeId"`
	Port   int    `json:"port"`
	InIP   string `json:"inIp,omitempty"`
}

type UserTunnelBackup struct {
	ID            int64 `json:"id"`
	UserID        int64 `json:"userId"`
	TunnelID      int64 `json:"tunnelId"`
	SpeedID       int64 `json:"speedId,omitempty"`
	Num           int   `json:"num"`
	Flow          int64 `json:"flow"`
	FlowMiB       int64 `json:"flowMiB,omitempty"`
	InFlow        int64 `json:"inFlow"`
	OutFlow       int64 `json:"outFlow"`
	FlowResetTime int64 `json:"flowResetTime"`
	ExpTime       int64 `json:"expTime"`
	Status        int   `json:"status"`
}

type SpeedLimitBackup struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Speed       int64  `json:"speed"`
	TunnelID    *int64 `json:"tunnelId,omitempty"`
	TunnelName  string `json:"tunnelName,omitempty"`
	CreatedTime int64  `json:"createdTime"`
	UpdatedTime int64  `json:"updatedTime,omitempty"`
	Status      int    `json:"status"`
}

type TunnelGroupBackup struct {
	ID          int64   `json:"id"`
	Name        string  `json:"name"`
	CreatedTime int64   `json:"createdTime"`
	UpdatedTime int64   `json:"updatedTime"`
	Status      int     `json:"status"`
	Tunnels     []int64 `json:"tunnels,omitempty"`
}

type UserGroupBackup struct {
	ID          int64   `json:"id"`
	Name        string  `json:"name"`
	CreatedTime int64   `json:"createdTime"`
	UpdatedTime int64   `json:"updatedTime"`
	Status      int     `json:"status"`
	Users       []int64 `json:"users,omitempty"`
}

type PermissionBackup struct {
	ID             int64                   `json:"id"`
	UserGroupID    int64                   `json:"userGroupId"`
	TunnelGroupID  int64                   `json:"tunnelGroupId"`
	CreatedTime    int64                   `json:"createdTime"`
	CreatedByGroup int                     `json:"createdByGroup"`
	Grants         []PermissionGrantBackup `json:"grants,omitempty"`
}

type PermissionGrantBackup struct {
	ID             int64 `json:"id"`
	UserGroupID    int64 `json:"userGroupId"`
	TunnelGroupID  int64 `json:"tunnelGroupId"`
	UserTunnelID   int64 `json:"userTunnelId"`
	CreatedTime    int64 `json:"createdTime"`
	CreatedByGroup int   `json:"createdByGroup"`
}

// RevokedImportPair identifies one user_tunnel revoked during backup
// import (R3X-005 facet (a)): the handler cleans up the forwards of
// each returned pair, mirroring the runtime revoke paths.
type RevokedImportPair struct {
	UserID   int64 `json:"userId"`
	TunnelID int64 `json:"tunnelId"`
}

// ImportResult contains the result of an import operation.
type ImportResult struct {
	UsersImported        int `json:"usersImported"`
	NodesImported        int `json:"nodesImported"`
	TunnelsImported      int `json:"tunnelsImported"`
	ForwardsImported     int `json:"forwardsImported"`
	UserTunnelsImported  int `json:"userTunnelsImported"`
	SpeedLimitsImported  int `json:"speedLimitsImported"`
	TunnelGroupsImported int `json:"tunnelGroupsImported"`
	UserGroupsImported   int `json:"userGroupsImported"`
	PermissionsImported  int `json:"permissionsImported"`
	ConfigsImported      int `json:"configsImported"`
	InboundsImported     int `json:"inboundsImported"`
	UserInboundsImported int `json:"userInboundsImported"`
	// R13X-004：inbounds 段子段导入计数（与既有 *Imported 字段同形，
	// 无 omitempty，旧客户端忽略新增字段）。计数口径与
	// InboundsImported 一致：只计实落行，跳过行走 SkippedRows。
	InboundKernelsImported              int `json:"inboundKernelsImported"`
	InboundRelaysImported               int `json:"inboundRelaysImported"`
	InboundCountersImported             int `json:"inboundCountersImported"`
	InboundSubscriptionsImported        int `json:"inboundSubscriptionsImported"`
	InboundSubscriptionCountersImported int `json:"inboundSubscriptionCountersImported"`
	InboundSourcesImported              int `json:"inboundSourcesImported"`
	InboundLegacyUsersImported          int `json:"inboundLegacyUsersImported"`
	// O7-007/O7-008：公告与监控权限导入计数
	AnnouncementsImported      int `json:"announcementsImported"`
	MonitorPermissionsImported int `json:"monitorPermissionsImported"`
	// B4/F-004/F-030：订阅分配与监控定义导入计数
	UserSubscriptionsImported int `json:"userSubscriptionsImported"`
	// BUG-A2-4：订阅按 token 重建换 id 时分配经 subRemap 重写落位；
	// 确实无法落位（目标订阅不存在）的分配被跳过并在此如实计数，
	// 不再混入 UserSubscriptionsImported 假报成功。
	UserSubscriptionsSkipped int         `json:"userSubscriptionsSkipped,omitempty"`
	ServiceMonitorsImported  int         `json:"serviceMonitorsImported"`
	// FIX132-C（A3-05）：SSH 配置与面板共享定义的恢复计数与受影响
	// 节点清单——恢复后这些节点的数据面待重新校验下发，回执如实
	// 披露（降级标记），不再静默降级。
	NodeSSHConfigsImported int   `json:"nodeSSHConfigsImported"`
	PeerSharesImported     int   `json:"peerSharesImported"`
	RestoredSSHNodeIDs     []int64 `json:"restoredSSHNodeIds,omitempty"`
	// FIX132-C（D4-03）：各段因引用不存在被跳过的行数（键为备份段
	// 名），与 ImportWarnings 一起构成恢复回执的完整披露；订阅段的
	// 跳过仍走上方 UserSubscriptionsSkipped 既有字段。
	SkippedRows    map[string]int `json:"skippedRows,omitempty"`
	ImportWarnings []string       `json:"importWarnings,omitempty"`
	// R3X-005 facet (a)：导入在分组段成员替换 / 停用收敛中撤销的
	// 组建授权（user_tunnel 已删行）清单，handler 据此逐对清理
	// 转发（与运行时全部撤销路径同口径）；无撤销时省略。本批路线
	// 定死为回传清理，不采「回执明示待清理」的弱路线。
	RevokedUserTunnels []RevokedImportPair `json:"revokedUserTunnels,omitempty"`
	// R13X-001：备份导入后的转发重推结果。RepushNodeIDs 为本次实际
	// 重推的在线节点，RepushFailedNodeIDs 为重推失败的节点（失败不
	// 静默：导入本身已成功，故失败只披露、不改写导入成功结论；
	// 失败节点重连时经 onNodeOnline 全量重布再次收敛）。
	RepushNodeIDs       []int64     `json:"repushNodeIds,omitempty"`
	RepushFailedNodeIDs []int64     `json:"repushFailedNodeIds,omitempty"`
	AutoBackup          *BackupData `json:"autoBackup,omitempty"`
}

// ─── View Structs (used by Repository, not GORM models) ─────────────
// These are used for JOIN query results that don't map 1:1 to a table.

// ForwardRecord is a minimal forward view used by control plane and flow policy.
type ForwardRecord struct {
	ID                   int64
	UserID               int64
	UserName             string
	Name                 string
	TunnelID             int64
	RemoteAddr           string
	Strategy             string
	Status               int
	SpeedID              sql.NullInt64
	MaxConn              int
	IPMaxConn            int
	IPSpeedID            sql.NullInt64
	ProxyProtocol        int
	ProxyProtocolReceive int
	ProxyProtocolSend    int
}

// TunnelRecord is a minimal tunnel view used by control plane.
type TunnelRecord struct {
	ID              int64
	Type            int
	Status          int
	Flow            int64
	TrafficRatio    float64
	Protocol        string
	ProbeTargetHost string
	ProbeTargetPort int
	Engine          string
}

type UserQuotaView struct {
	UserID           int64
	DailyLimitGB     int64
	MonthlyLimitGB   int64
	DailyUsedBytes   int64
	MonthlyUsedBytes int64
	DayKey           int64
	MonthKey         int64
	DisabledByQuota  int
	DisabledAt       int64
	PausedForwardIDs string
}

// ForwardPortRecord is a forward port mapping used by control plane.
type ForwardPortRecord struct {
	NodeID int64
	Port   int
	InIP   string
}

// NodeRecord is a node view used by control plane.
type NodeRecord struct {
	ID            int64
	Name          string
	ServerIP      string
	ServerIPv4    string
	ServerIPv6    string
	ExtraIPs      string
	Status        int
	PortRange     string
	TCPListenAddr string
	UDPListenAddr string
	InterfaceName string
	IsRemote      int
	ForwardMode   string
	RemoteURL     string
	RemoteToken   string
	RemoteConfig  string
}

type ChainNodeRecord struct {
	ChainType int
	Inx       int64
	NodeID    int64
	Port      int
	NodeName  string
	Protocol  string
	Strategy  string
	ConnectIP string
}

type UserTunnelLimiterInfo struct {
	UserTunnelID int64
	LimiterID    *int64
	Speed        *int
}

// UserFlowSnapshot holds a user's current flow counters (used by stats job).
type UserFlowSnapshot struct {
	UserID  int64
	InFlow  int64
	OutFlow int64
}

// ExpiredUserTunnel holds minimal info for an expired user_tunnel row.
type ExpiredUserTunnel struct {
	ID       int64
	UserID   int64
	TunnelID int64
}

// UserTunnelDetail is a joined view of user_tunnel + tunnel + speed_limit.
type UserTunnelDetail struct {
	ID            int64
	UserID        int64
	TunnelID      int64
	TunnelName    string
	Status        int
	TunnelFlow    int
	Flow          int64
	FlowMiB       int64 `gorm:"column:flow_mib"`
	InFlow        int64
	OutFlow       int64
	Num           int
	FlowResetTime int64
	ExpTime       int64
	SpeedID       sql.NullInt64
	SpeedLimit    sql.NullString
	Speed         sql.NullInt64
}

// UserForwardDetail is a joined view of forward + tunnel.
type UserForwardDetail struct {
	ID         int64
	Name       string
	TunnelID   int64
	TunnelName string
	InIP       string
	InPort     sql.NullInt64
	RemoteAddr string
	InFlow     int64
	OutFlow    int64
	Status     int
	CreatedAt  int64
}

type NodeMetric struct {
	ID          int64   `gorm:"primaryKey;autoIncrement" json:"id"`
	NodeID      int64   `gorm:"column:node_id;not null;index:idx_node_metric_node_time,priority:1" json:"nodeId"`
	Timestamp   int64   `gorm:"not null;index:idx_node_metric_node_time,priority:2;index:idx_node_metric_time" json:"timestamp"`
	CPUUsage    float64 `gorm:"column:cpu_usage" json:"cpuUsage"`
	MemUsage    float64 `gorm:"column:mem_usage" json:"memoryUsage"`
	DiskUsage   float64 `gorm:"column:disk_usage" json:"diskUsage"`
	NetInBytes  int64   `gorm:"column:net_in_bytes" json:"netInBytes"`
	NetOutBytes int64   `gorm:"column:net_out_bytes" json:"netOutBytes"`
	NetInSpeed  int64   `gorm:"column:net_in_speed" json:"netInSpeed"`
	NetOutSpeed int64   `gorm:"column:net_out_speed" json:"netOutSpeed"`
	Load1       float64 `gorm:"column:load1" json:"load1"`
	Load5       float64 `gorm:"column:load5" json:"load5"`
	Load15      float64 `gorm:"column:load15" json:"load15"`
	TCPConns    int64   `gorm:"column:tcp_conns" json:"tcpConns"`
	UDPConns    int64   `gorm:"column:udp_conns" json:"udpConns"`
	Uptime      int64   `gorm:"column:uptime" json:"uptime"`
}

func (NodeMetric) TableName() string { return "node_metric" }

// NodeTraffic accumulates a node's whole-machine NIC byte counters into
// hourly buckets (deltas of the agent-reported cumulative counters).
// This is node NIC traffic — it includes forwarding, kernel and system
// traffic of the whole machine and is a different metric from tunnel
// billing / subscription quota counters; never mix the two. Telemetry
// time series like NodeMetric: retention is a fixed 400 days and the
// table is intentionally NOT part of backups (same precedent as
// node_latency_sample).
type NodeTraffic struct {
	ID       int64 `gorm:"primaryKey;autoIncrement" json:"id"`
	NodeID   int64 `gorm:"column:node_id;not null;uniqueIndex:idx_node_traffic_node_hour,priority:1" json:"nodeId"`
	Hour     int64 `gorm:"not null;uniqueIndex:idx_node_traffic_node_hour,priority:2" json:"hour"`
	InBytes  int64 `gorm:"column:in_bytes;not null;default:0" json:"inBytes"`
	OutBytes int64 `gorm:"column:out_bytes;not null;default:0" json:"outBytes"`
}

func (NodeTraffic) TableName() string { return "node_traffic" }

// NodeLatencySample is one measurement of a node's latency to a reference
// point, kept for the latency trend chart. Retention is capped at 7 days
// (see IngestionService.pruneMetricsAt) so the table cannot grow without
// bound when many nodes measure many points.
type NodeLatencySample struct {
	ID          int64   `gorm:"primaryKey;autoIncrement"`
	NodeID      int64   `gorm:"column:node_id;not null;index:idx_latency_sample_node_time,priority:1"`
	CreatedTime int64   `gorm:"column:created_time;not null;index:idx_latency_sample_node_time,priority:2;index"`
	Point       string  `gorm:"column:point;type:varchar(64);not null"`
	OK          int     `gorm:"column:ok;not null;default:0"`
	LatencyMs   int64   `gorm:"column:latency_ms;not null;default:0"`
	LossPct     float64 `gorm:"column:loss_pct;not null;default:0"`
}

func (NodeLatencySample) TableName() string { return "node_latency_sample" }

type TunnelMetric struct {
	ID           int64   `gorm:"primaryKey;autoIncrement" json:"id"`
	// FIX132-C（A3-S2）：(tunnel_id, node_id, timestamp) 的唯一性由
	// v6 去重迁移的 uidx_tunnel_metric_bucket 单独持有，模型层不再
	// 声明唯一索引——AutoMigrate 在去重迁移之前运行，存量重复桶行
	// 会让它建唯一索引时直接失败、升级启动被堵死；且同列双唯一
	// 索引本就冗余。存量库已有的同名索引 AutoMigrate 不会删除，
	// 无害保留。
	TunnelID     int64   `gorm:"column:tunnel_id;not null" json:"tunnelId"`
	NodeID       int64   `gorm:"column:node_id;not null" json:"nodeId"`
	Timestamp    int64   `gorm:"not null;index:idx_tunnel_metric_time" json:"timestamp"`
	BytesIn      int64   `gorm:"column:bytes_in" json:"bytesIn"`
	BytesOut     int64   `gorm:"column:bytes_out" json:"bytesOut"`
	Connections  int64   `gorm:"column:connections" json:"connections"`
	Errors       int64   `gorm:"column:errors" json:"errors"`
	AvgLatencyMs float64 `gorm:"column:avg_latency_ms" json:"avgLatencyMs"`
}

func (TunnelMetric) TableName() string { return "tunnel_metric" }

type ServiceMonitor struct {
	ID          int64  `gorm:"primaryKey;autoIncrement" json:"id"`
	Name        string `gorm:"type:varchar(100);not null" json:"name"`
	Type        string `gorm:"type:varchar(20);not null" json:"type"`
	Target      string `gorm:"type:text;not null" json:"target"`
	IntervalSec int    `gorm:"column:interval_sec;not null;default:60" json:"intervalSec"`
	TimeoutSec  int    `gorm:"column:timeout_sec;not null;default:5" json:"timeoutSec"`
	NodeID      int64  `gorm:"column:node_id;index" json:"nodeId"`
	Enabled     int    `gorm:"not null;default:1" json:"enabled"`
	CreatedTime int64  `gorm:"column:created_time;not null" json:"createdTime"`
	UpdatedTime int64  `gorm:"column:updated_time;not null" json:"updatedTime"`
}

func (ServiceMonitor) TableName() string { return "service_monitor" }

type ServiceMonitorResult struct {
	ID           int64   `gorm:"primaryKey;autoIncrement" json:"id"`
	MonitorID    int64   `gorm:"column:monitor_id;not null;index:idx_monitor_result_monitor_time,priority:1" json:"monitorId"`
	NodeID       int64   `gorm:"column:node_id;not null;index" json:"nodeId"`
	Timestamp    int64   `gorm:"not null;index:idx_monitor_result_monitor_time,priority:2" json:"timestamp"`
	Success      int     `gorm:"not null" json:"success"`
	LatencyMs    float64 `gorm:"column:latency_ms" json:"latencyMs"`
	StatusCode   int     `gorm:"column:status_code" json:"statusCode"`
	ErrorMessage string  `gorm:"column:error_message;type:text" json:"errorMessage"`
}

func (ServiceMonitorResult) TableName() string { return "service_monitor_result" }

// TunnelQuality stores periodic probe results for a tunnel.
// Unlike the old upsert model, rows accumulate for history/charting.
// Old rows are pruned periodically (default: keep 24h).
type TunnelQuality struct {
	ID                 int64   `gorm:"primaryKey;autoIncrement" json:"id"`
	TunnelID           int64   `gorm:"column:tunnel_id;not null;index:idx_tunnel_quality_tunnel_time,priority:1" json:"tunnelId"`
	EntryToExitLatency float64 `gorm:"column:entry_to_exit_latency" json:"entryToExitLatency"`
	ExitToBingLatency  float64 `gorm:"column:exit_to_bing_latency" json:"exitToBingLatency"`
	EntryToExitLoss    float64 `gorm:"column:entry_to_exit_loss" json:"entryToExitLoss"`
	ExitToBingLoss     float64 `gorm:"column:exit_to_bing_loss" json:"exitToBingLoss"`
	Success            int     `gorm:"not null;default:1" json:"success"`
	ErrorMessage       string  `gorm:"column:error_message;type:text" json:"errorMessage,omitempty"`
	Timestamp          int64   `gorm:"not null;index:idx_tunnel_quality_tunnel_time,priority:2;index:idx_tunnel_quality_time" json:"timestamp"`
	ChainDetails       string  `gorm:"column:chain_details;type:text" json:"chainDetails,omitempty"`
}

func (TunnelQuality) TableName() string { return "tunnel_quality" }

// PeerShareResource is the durable desired state for a namespaced peer command.
// Rows are retained as tombstones until deletion has been acknowledged.
type PeerShareResource struct {
	ID                  int64  `gorm:"primaryKey;autoIncrement"`
	ShareID             int64  `gorm:"column:share_id;not null;uniqueIndex:idx_peer_share_resource_key"`
	NodeID              int64  `gorm:"column:node_id;not null;index"`
	Kind                string `gorm:"type:text;not null;uniqueIndex:idx_peer_share_resource_key"`
	OriginalName        string `gorm:"column:original_name;type:text;not null;uniqueIndex:idx_peer_share_resource_key"`
	RuntimeName         string `gorm:"column:runtime_name;type:text;not null;index"`
	LegacyNames         string `gorm:"column:legacy_names;type:text;not null;default:''"`
	LegacyServiceBase   string `gorm:"column:legacy_service_base;type:text;not null;default:''"`
	// FIX132-C（R4-P1）：此列不得带 GORM 默认值。原 default:false
	// 在 SQLite 建表渲染为 `numeric NOT NULL DEFAULT false`，
	// pgloader 翻译到 PG 时以 22P02 拒绝、整条 SQLite→PG 官方迁移
	// 在建表期 FATAL（R4 于 PG 16 实测，全库仅此 1 列）。备选均已
	// 实测否决：default:0 会被 bool 默认值解析重新渲染回 false；
	// 表达式默认 (0) 在 SQLite 可行，但 PG 原生建表会生成
	// `boolean DEFAULT (0)` 被 PG 拒绝。去掉默认值后两方言建表
	// 均为「NOT NULL 无默认」，该表全部写入都经 GORM 显式带值
	//（peer_share_resources.go 的 upsert），语义不变；存量
	// numeric 列（0/1）读 bool 亦不受影响。
	ReleaseLegacyFamily bool `gorm:"column:release_legacy_family;not null"`
	Config              string `gorm:"type:text;not null;default:''"`
	DesiredState        string `gorm:"column:desired_state;type:text;not null;default:'active'"`
	Applied             int    `gorm:"not null;default:0"`
	UpdatedTime         int64  `gorm:"column:updated_time;not null"`
}

func (PeerShareResource) TableName() string { return "peer_share_resource" }
