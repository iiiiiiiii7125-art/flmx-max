package handler

import (
	"strings"
	"testing"
)

// FIX2-C #1（V6-01）：升级写 compose 时必须保留存量网络设置。
// FIX-G 把模板子网改到 172.30.0.0/16 只应作用于全新安装；两条升级
// 路径（脚本 activate_panel_update_files 与面板内 replaceCompose）
// 此前整份覆写，存量部署升级即被重建网络（容器 IP 全变、可信代理
// 判定随子网翻转）。模板一律 172.30，存量一律以旧文件为准。

const fix2cExistingCompose = `services:
  backend:
    image: example/backend:1.3.2
networks:
  gost-network:
    name: gost-network
    driver: bridge
    ipam:
      config:
        - subnet: 172.80.0.0/16
`

const fix2cCandidateCompose = `services:
  backend:
    image: example/backend:1.3.3
networks:
  gost-network:
    name: gost-network
    driver: bridge
    ipam:
      config:
        - subnet: 172.30.0.0/16
`

func TestFix2CPreserveComposeNetworksKeepsExistingSubnet(t *testing.T) {
	out := string(preserveComposeNetworks([]byte(fix2cExistingCompose), []byte(fix2cCandidateCompose)))
	if !strings.Contains(out, "172.80.0.0/16") {
		t.Fatalf("existing subnet must survive the upgrade write:\n%s", out)
	}
	if strings.Contains(out, "172.30.0.0/16") {
		t.Fatalf("template subnet must not overwrite an existing deployment:\n%s", out)
	}
	if !strings.Contains(out, "example/backend:1.3.3") {
		t.Fatalf("candidate content (new images) must be kept:\n%s", out)
	}
}

func TestFix2CPreserveComposeNetworksKeepsCustomNameAndV6(t *testing.T) {
	existing := `networks:
  gost-network:
    name: legacy-net
    driver: bridge
    enable_ipv6: true
    ipam:
      config:
        - subnet: 172.80.0.0/16
        - subnet: fd00:dead:beef::/48
`
	out := string(preserveComposeNetworks([]byte(existing), []byte(fix2cCandidateCompose)))
	if !strings.Contains(out, "legacy-net") {
		t.Fatalf("existing network name must be carried over:\n%s", out)
	}
	if !strings.Contains(out, "172.80.0.0/16") || !strings.Contains(out, "fd00:dead:beef::/48") {
		t.Fatalf("both existing subnets must be carried over:\n%s", out)
	}
	if strings.Contains(out, "172.30.0.0/16") {
		t.Fatalf("template subnet must not leak in:\n%s", out)
	}
}

func TestFix2CPreserveComposeNetworksNoSubnetExisting(t *testing.T) {
	// 存量 compose 没有显式子网（docker 自动分配）：候选不得强加
	// 模板子网——写上 172.30 等于升级时重建网络。结果应去掉候选
	// 的 subnet 条目，维持自动分配语义。
	existing := `networks:
  gost-network:
    name: gost-network
    driver: bridge
`
	out := string(preserveComposeNetworks([]byte(existing), []byte(fix2cCandidateCompose)))
	if strings.Contains(out, "172.30.0.0/16") || strings.Contains(out, "subnet:") {
		t.Fatalf("no-subnet deployment must not gain a template subnet:\n%s", out)
	}
}

func TestFix2CPreserveComposeNetworksNoNetworksExisting(t *testing.T) {
	// 存量文件根本没有 networks 段（无法推断）：候选原样返回。
	out := preserveComposeNetworks([]byte("services: {}\n"), []byte(fix2cCandidateCompose))
	if string(out) != fix2cCandidateCompose {
		t.Fatalf("candidate must pass through unchanged, got:\n%s", out)
	}
}
