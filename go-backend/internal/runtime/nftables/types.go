package nftables

const (
	ModeAgent    = "agent"
	ModeNftables = "nftables"

	StatusPending = "pending"
	StatusApplied = "applied"
	StatusError   = "error"

	CounterDirectionDNAT       = "dnat"
	CounterDirectionToTarget   = "to-target"
	CounterDirectionFromTarget = "from-target"
)

type Target struct {
	Host string
	Port int
}

type Rule struct {
	ForwardID  int64
	InPort     int
	BindIP     string
	TargetHost string
	TargetPort int
	Protocols  []string
}

type NodePlan struct {
	NodeID int64
	// InstanceID is this panel install's instance key (FLMX_INSTANCE;
	// "" is the default instance). It selects the nft table the plan
	// renders into (see TableNameForInstance): two panels sharing a
	// node must not render into the same table. Zero value reads as
	// the default instance, never the plain table.
	InstanceID string
	// InstallID is this panel install's per-install identity
	// (FLMX_INSTALL_ID, a UUID generated once at install time and
	// persisted in the deployment .env; R18-C3-02). It joins the
	// instance key in the table derivation, so two default-instance
	// panels on different hosts get different tables. Empty means
	// "no identity configured": the legacy instance-only derivation
	// applies, and deployed installs keep their table.
	InstallID string
	// MigratePlainTables is the B-01 ownership proof for the one-shot
	// upgrade migration (see buildApplyCommand): true only when this
	// panel install has previously applied nft rules to this node
	// successfully (a persisted nft_rule_binding row exists — the
	// panel-side analogue of the agent's persisted signature). The
	// handler sets it from the DB; the runtime never invents it.
	// False (zero value) means "no proof": a plain table on the node
	// must be left alone, it may be another panel's live table.
	MigratePlainTables bool
	Rules              []Rule
}

type SSHConfig struct {
	Host       string
	Port       int
	Username   string
	AuthType   string
	Password   string
	PrivateKey string
	Passphrase string
	SudoMode   string
	// HostKeyFingerprint is the pinned SSH host key fingerprint in
	// ssh.FingerprintSHA256 form ("SHA256:..."). Empty means unpinned:
	// every operation fails closed (C3-02) until the node's 测连通 flow
	// pins the presented key via ProbeHostKeyFingerprint (TOFU).
	HostKeyFingerprint string
}

type ApplyResult struct {
	NodeID int64
	Script string
	Hashes map[int64]string
}
