package nftables

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// ErrHostKeyUnpinned 表示节点尚未钉入 SSH 主机密钥指纹（C3-02）：
// 任何特权操作（apply/测连通/计数采集/封禁）在钉入前一律 fail-closed，
// 不得静默放行。钉入路径是节点页「测连通」（TOFU，见 nftables 运行时
// handler 的 nodeNftablesTest），老节点升级后首次测连通即钉入。
var ErrHostKeyUnpinned = errors.New("SSH 主机密钥指纹未钉入：请先在节点页执行测连通钉入指纹后再操作")

// HostKeyMismatchError 表示节点实际呈现的主机密钥指纹与已钉入值不符。
// 这正是中间人/冒名节点的信号，必须硬失败且在任何认证发生之前——
// 密码认证下继续握手等于把节点 SSH 密码送给冒名者。
type HostKeyMismatchError struct {
	Expected string
	Actual   string
}

func (e *HostKeyMismatchError) Error() string {
	return fmt.Sprintf("SSH 主机密钥指纹不匹配（已钉入 %s，节点实际呈现 %s）：已在认证前拒绝连接；如确属节点重装或更换了 SSH 主机密钥，请在节点页测连通时勾选重置主机密钥重新钉入", e.Expected, e.Actual)
}

// hostKeyCallback 按已钉入指纹校验对端呈现的主机密钥：未钉入、
// 不匹配都 fail-closed，绝不退化为不校验。
func hostKeyCallback(pinned string) ssh.HostKeyCallback {
	pinned = strings.TrimSpace(pinned)
	return func(_ string, _ net.Addr, key ssh.PublicKey) error {
		actual := ssh.FingerprintSHA256(key)
		if pinned == "" {
			return ErrHostKeyUnpinned
		}
		if actual != pinned {
			return &HostKeyMismatchError{Expected: pinned, Actual: actual}
		}
		return nil
	}
}

func isHostKeyError(err error) bool {
	if errors.Is(err, ErrHostKeyUnpinned) {
		return true
	}
	var mismatch *HostKeyMismatchError
	return errors.As(err, &mismatch)
}

var errHostKeyProbeCaptured = errors.New("host key probe captured")

// ProbeHostKeyFingerprint 只完成 SSH 传输层握手并取回对端呈现的主机
// 密钥指纹（TOFU 钉入专用）：回调记录指纹后立即中止握手，认证阶段
// 永远不会开始，因此探针不会把任何凭据（尤其密码）发给未经验证的
// 主机。返回 ssh.FingerprintSHA256 形态的指纹串。
func ProbeHostKeyFingerprint(ctx context.Context, cfg SSHConfig) (string, error) {
	host := strings.TrimSpace(cfg.Host)
	if host == "" {
		return "", fmt.Errorf("SSH 主机不能为空")
	}
	timeout := 15 * time.Second
	dialCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	addr := net.JoinHostPort(host, fmt.Sprintf("%d", normalizedSSHPort(cfg.Port)))
	dialer := net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(dialCtx, "tcp", addr)
	if err != nil {
		return "", fmt.Errorf("SSH 连接失败: %w", err)
	}
	defer conn.Close()

	captured := ""
	probeConfig := &ssh.ClientConfig{
		User: "flmx-hostkey-probe",
		HostKeyCallback: func(_ string, _ net.Addr, key ssh.PublicKey) error {
			captured = ssh.FingerprintSHA256(key)
			return errHostKeyProbeCaptured
		},
		Timeout: timeout,
	}
	sshConn, _, _, handshakeErr := ssh.NewClientConn(conn, addr, probeConfig)
	if sshConn != nil {
		_ = sshConn.Close()
	}
	if captured != "" {
		return captured, nil
	}
	if handshakeErr != nil {
		return "", fmt.Errorf("SSH 主机密钥探测失败: %w", handshakeErr)
	}
	return "", fmt.Errorf("SSH 主机密钥探测失败: 对端未呈现主机密钥")
}

type Runner interface {
	ApplyScript(ctx context.Context, cfg SSHConfig, script string) error
	Test(ctx context.Context, cfg SSHConfig) error
	ListTableJSON(ctx context.Context, cfg SSHConfig) ([]byte, error)
}

type SSHRunner struct {
	Timeout time.Duration
}

func NewSSHRunner() *SSHRunner {
	return &SSHRunner{Timeout: 15 * time.Second}
}

func (r *SSHRunner) Test(ctx context.Context, cfg SSHConfig) error {
	nft := nftBinary(cfg)
	probeName := fmt.Sprintf("flmx_capability_%d", time.Now().UnixNano())
	return r.run(ctx, cfg, buildCapabilityCheckCommand(nft, probeName))
}

func buildCapabilityCheckCommand(nft, probeName string) string {
	script := RenderTable(NodePlan{
		Rules: []Rule{{
			ForwardID:  1,
			InPort:     12345,
			TargetHost: "192.0.2.1",
			TargetPort: 443,
			Protocols:  []string{"tcp", "udp"},
		}},
	})
	script = strings.Replace(script, "table inet "+tableName+" {", "table inet "+probeName+" {", 1)
	return "set -eu\n" +
		"command -v nft >/dev/null 2>&1\n" +
		nft + " --version >/dev/null 2>&1\n" +
		"tmp=$(mktemp /tmp/flmx-nft-capability-XXXXXX.nft)\n" +
		"trap 'rm -f \"$tmp\"' EXIT\n" +
		"cat > \"$tmp\" <<'EOF'\n" + script + "\nEOF\n" +
		"if ! " + nft + " -c -f \"$tmp\"; then\n" +
		"  echo 'nftables cannot validate the generated FLMX rules' >&2\n" +
		"  exit 1\n" +
		"fi"
}

func (r *SSHRunner) ApplyScript(ctx context.Context, cfg SSHConfig, script string) error {
	command := buildApplyCommand(nftBinary(cfg), script)
	return r.run(ctx, cfg, command)
}

func buildApplyCommand(nft, script string) string {
	return "set -eu\n" +
		"tmp=$(mktemp /tmp/flmx-nft-XXXXXX.nft)\n" +
		"batch=$(mktemp /tmp/flmx-nft-batch-XXXXXX.nft) || { rm -f \"$tmp\"; exit 1; }\n" +
		"cleanup() {\n" +
		"  rm -f \"$tmp\" \"$batch\"\n" +
		"}\n" +
		"trap cleanup EXIT\n" +
		"cat > \"$tmp\" <<'EOF'\n" + script + "\nEOF\n" +
		// 改名过渡：新表与 legacy 表（flvx）谁存在就先删谁，再整体加载新表，
		// 避免改名后旧表残留在内核中与新表双份生效。
		": > \"$batch\"\n" +
		"if " + nft + " list table inet " + tableName + " >/dev/null 2>&1; then\n" +
		"  printf '%s\\n' 'delete table inet " + tableName + "' >> \"$batch\"\n" +
		"fi\n" +
		"if " + nft + " list table inet " + legacyTableName + " >/dev/null 2>&1; then\n" +
		"  printf '%s\\n' 'delete table inet " + legacyTableName + "' >> \"$batch\"\n" +
		"fi\n" +
		"cat \"$tmp\" >> \"$batch\"\n" +
		"if ! " + nft + " -c -f \"$batch\"; then\n" +
		"  echo 'nftables rule validation failed; active rules were preserved' >&2\n" +
		"  exit 1\n" +
		"fi\n" +
		nft + " -f \"$batch\""
}

func (r *SSHRunner) ListTableJSON(ctx context.Context, cfg SSHConfig) ([]byte, error) {
	return r.runOutput(ctx, cfg, nftBinary(cfg)+" -j list table inet "+tableName)
}

func (r *SSHRunner) run(ctx context.Context, cfg SSHConfig, command string) error {
	_, err := r.runOutput(ctx, cfg, command)
	return err
}

func (r *SSHRunner) runOutput(ctx context.Context, cfg SSHConfig, command string) ([]byte, error) {
	timeout := r.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	clientConfig, err := buildSSHClientConfig(cfg)
	if err != nil {
		return nil, err
	}

	addr := net.JoinHostPort(strings.TrimSpace(cfg.Host), fmt.Sprintf("%d", normalizedSSHPort(cfg.Port)))
	dialer := net.Dialer{Timeout: timeout}
	conn, err := dialer.DialContext(runCtx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("SSH 连接失败: %w", err)
	}
	defer conn.Close()

	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, clientConfig)
	if err != nil {
		// 主机密钥类失败（未钉入/指纹不符）自带完整人话与处置路径，
		// 原样回出，不套「认证失败」外壳误导排障方向（C3-02）。
		if isHostKeyError(err) {
			return nil, err
		}
		return nil, fmt.Errorf("SSH 认证失败: %w", err)
	}
	client := ssh.NewClient(sshConn, chans, reqs)
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("SSH 会话创建失败: %w", err)
	}
	defer session.Close()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	done := make(chan error, 1)
	go func() {
		done <- session.Run(command)
	}()

	select {
	case <-runCtx.Done():
		_ = session.Close()
		return nil, fmt.Errorf("SSH 命令超时: %w", runCtx.Err())
	case err := <-done:
		if err != nil {
			message := strings.TrimSpace(stderr.String())
			if message != "" {
				return nil, fmt.Errorf("远程执行失败: %s: %w", message, err)
			}
			return nil, fmt.Errorf("远程执行失败: %w", err)
		}
		return stdout.Bytes(), nil
	}
}

func buildSSHClientConfig(cfg SSHConfig) (*ssh.ClientConfig, error) {
	if strings.TrimSpace(cfg.Host) == "" {
		return nil, fmt.Errorf("SSH 主机不能为空")
	}
	if strings.TrimSpace(cfg.Username) == "" {
		return nil, fmt.Errorf("SSH 用户名不能为空")
	}

	auth, err := authMethods(cfg)
	if err != nil {
		return nil, err
	}
	if len(auth) == 0 {
		return nil, fmt.Errorf("SSH 认证方式不能为空")
	}

	return &ssh.ClientConfig{
		User:            strings.TrimSpace(cfg.Username),
		Auth:            auth,
		HostKeyCallback: hostKeyCallback(cfg.HostKeyFingerprint),
		Timeout:         15 * time.Second,
	}, nil
}

func authMethods(cfg SSHConfig) ([]ssh.AuthMethod, error) {
	switch strings.ToLower(strings.TrimSpace(cfg.AuthType)) {
	case "":
		if strings.TrimSpace(cfg.PrivateKey) == "" {
			return nil, fmt.Errorf("SSH 私钥不能为空")
		}
		signer, err := parsePrivateKey(cfg.PrivateKey, cfg.Passphrase)
		if err != nil {
			return nil, err
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil
	case "password":
		if cfg.Password == "" {
			return nil, fmt.Errorf("SSH 密码不能为空")
		}
		return []ssh.AuthMethod{ssh.Password(cfg.Password)}, nil
	case "private_key":
		if strings.TrimSpace(cfg.PrivateKey) == "" {
			return nil, fmt.Errorf("SSH 私钥不能为空")
		}
		signer, err := parsePrivateKey(cfg.PrivateKey, cfg.Passphrase)
		if err != nil {
			return nil, err
		}
		return []ssh.AuthMethod{ssh.PublicKeys(signer)}, nil
	default:
		return nil, fmt.Errorf("不支持的 SSH 认证方式: %s", cfg.AuthType)
	}
}

func parsePrivateKey(privateKey, passphrase string) (ssh.Signer, error) {
	if passphrase != "" {
		signer, err := ssh.ParsePrivateKeyWithPassphrase([]byte(privateKey), []byte(passphrase))
		if err != nil {
			return nil, fmt.Errorf("SSH 私钥解析失败: %w", err)
		}
		return signer, nil
	}
	signer, err := ssh.ParsePrivateKey([]byte(privateKey))
	if err != nil {
		return nil, fmt.Errorf("SSH 私钥解析失败: %w", err)
	}
	return signer, nil
}

func nftCommand(cfg SSHConfig, command string) string {
	return "sh -lc " + sshQuote(command)
}

func nftBinary(cfg SSHConfig) string {
	if strings.EqualFold(strings.TrimSpace(cfg.SudoMode), "sudo") {
		return "sudo -n nft"
	}
	return "nft"
}

func sshQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func normalizedSSHPort(port int) int {
	if port <= 0 {
		return 22
	}
	return port
}
