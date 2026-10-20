package main

import (
	"encoding/json"
	"fmt"
	"os"
)

// Config 配置结构体
type Config struct {
	Addr   string `json:"addr"`
	Secret string `json:"secret"`
	Http   int    `json:"http"`
	Tls    int    `json:"tls"`
	Socks  int    `json:"socks"`
	// Proxy 是安装时落盘的 GitHub 代理前缀（可选），供 agent 面板内升级时
	// 经代理下载升级包；为空表示直连。面板下发的 proxyUrl 优先于此值。
	Proxy string `json:"proxy,omitempty"`
}

// LoadConfig 加载配置文件
func LoadConfig(configPath string) (*Config, error) {
	// 检查文件是否存在
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return nil, fmt.Errorf("配置文件不存在: %s", configPath)
	}

	// 读取文件内容
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("读取配置文件失败: %v", err)
	}

	// 解析JSON
	var config Config
	if err := json.Unmarshal(data, &config); err != nil {
		return nil, fmt.Errorf("解析配置文件失败: %v", err)
	}

	// 验证必要的配置项
	if config.Addr == "" {
		return nil, fmt.Errorf("服务器地址不能为空")
	}
	// F-089: secret 是与面板通信的 AES 预共享密钥，为空时加密器无法
	// 建立。必须在加载期 fail-closed，而不是带着不可用的加密器启动
	// 后再在各上报路径静默回退明文（且会掩盖配置错误）。
	if config.Secret == "" {
		return nil, fmt.Errorf("节点密钥(secret)不能为空")
	}

	return &config, nil
}
