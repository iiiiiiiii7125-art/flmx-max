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
	Rules  []Rule
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
