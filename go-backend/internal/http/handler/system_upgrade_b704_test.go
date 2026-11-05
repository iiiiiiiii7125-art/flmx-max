package handler

import "testing"

// B7-04: .sha256 校验文件的书写形态容忍——空白/制表分隔、大写
// 哈希、sha256sum 二进制标记都应解析出同一期望值; 真不符与
// 垃圾内容仍 fail-closed (解析为空)。

func TestParseReleaseChecksumToleratesFormats(t *testing.T) {
	const hash = "9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08"
	cases := []struct {
		name string
		body string
		want string
	}{
		{"标准双空格", hash + "  docker-compose.yml", hash},
		{"制表分隔", hash + "\tdocker-compose.yml", hash},
		{"大写哈希", "9F86D081884C7D659A2FEAA0C55AD015A3BF4F1B2B0B822CD15D6C15B0F00A08  docker-compose.yml", hash},
		{"二进制标记", hash + " *docker-compose.yml", hash},
		{"只有哈希带换行", hash + "\n", hash},
		{"垃圾内容", "not-a-checksum-file", ""},
		{"空文件", "", ""},
		{"截断哈希", hash[:63] + "  docker-compose.yml", ""},
	}
	for _, tc := range cases {
		if got := parseReleaseChecksum([]byte(tc.body)); got != tc.want {
			t.Fatalf("%s: parseReleaseChecksum(%q) = %q, want %q", tc.name, tc.body, got, tc.want)
		}
	}
}
