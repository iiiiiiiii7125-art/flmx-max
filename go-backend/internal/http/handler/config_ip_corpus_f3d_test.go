package handler

import "testing"

// F3-D（LEDGER3 C-F5-01）：配置键 ip 的保存/恢复双面语料。本文件是
// 保存面一半——对 normalizeAndValidateConfigValue("ip", ·) 逐条钉
// verdict；恢复面一半在 repo 包 backup_ip_restore_f3d_test.go，两份
// 语料逐字相同（f3dConfigIPCorpus），任一面谓词漂移即有一侧变红。
// 语料来源＝保存面两段谓词的拒绝/放行形态全集：validateNoEmptyLabelAddr
// （空标签）＋validatePanelInstallAddr→IsValidNodeAddress（scheme 剥离、
// 结构字符集、LDH 字符集、端口数值/范围、全数字点分 IPv4 校验）。

type f3dIPCorpusCase struct {
	value   string
	wantErr bool
}

func f3dConfigIPCorpus() []f3dIPCorpusCase {
	return []f3dIPCorpusCase{
		// 保存面必拒形态。
		{"x.com;id", true},                            // 分号（shell 元字符，字符集外）
		{"panel example.com", true},                   // 内嵌空格（结构字符）
		{"pan_el.example.com", true},                  // 下划线（LDH 字符集外）
		{"panel.example.com:80:443", true},            // 双端口
		{"panel.example.com:99999", true},             // 端口越界
		{"panel.example.com:65536", true},             // 端口越界（边界外一）
		{"panel.example.com:abc", true},               // 端口非数值
		{"panel.example.com:0", true},                 // 端口 0
		{"panel.example.com:", true},                  // 空端口段
		{"999.999.1.1", true},                         // 全数字点分但非合法 IPv4
		{"a..b", true},                                // 空标签
		{"panel.example.com/path", true},              // 路径
		{"panel.example.com?q=1", true},               // 查询
		{"user@panel.example.com", true},              // @（结构字符）
		{"panel.example.com,other.example.com", true}, // 逗号多目标（结构字符）
		{"panel.example.com\nevil.example.com", true}, // 内嵌换行（结构字符）
		{"https://pan_el.example.com", true},          // scheme 剥离后下划线
		{"http://panel.example.com/path", true},       // scheme 剥离后路径
		{"https://panel.example.com:99999", true},     // scheme 形态端口越界
		{"[2001:db8::9", true},                        // 括号不闭合
		// 保存面放行形态（合法集，含既有可存性：回环/RFC1918/括号
		// IPv6/zone id/scheme 前缀/首尾空白）。
		{"", false},
		{"panel.example.com", false},
		{"panel.example.com:8443", false},
		{"panel.example.com:65535", false},
		{"https://panel.example.com", false},
		{"wss://panel.example.com:8443", false},
		{"https://192.0.2.5:8443", false},
		{"192.0.2.5", false},
		{"192.0.2.5:8443", false},
		{"127.0.0.1:6365", false},
		{"10.0.0.8", false},
		{"localhost", false},
		{"localhost:6365", false},
		{"[2001:db8::9]:8443", false},
		{"2001:db8::9", false},
		{"fe80::1%eth0", false},
		{"  panel.example.com:8443  ", false},
	}
}

func TestF3DConfigIPSaveSideCorpus(t *testing.T) {
	for _, tc := range f3dConfigIPCorpus() {
		_, err := normalizeAndValidateConfigValue("ip", tc.value)
		if (err != nil) != tc.wantErr {
			t.Fatalf("save face normalizeAndValidateConfigValue(\"ip\", %q) err = %v, wantErr = %v", tc.value, err, tc.wantErr)
		}
	}
}
