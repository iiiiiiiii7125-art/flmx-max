package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/go-gost/core/observer/stats"
	"github.com/go-gost/x/config"
	"github.com/go-gost/x/internal/util/crypto"
	"github.com/go-gost/x/registry"
)

var httpReportURL string
var configReportURL string
var httpAESCrypto *crypto.AESCrypto // 新增：HTTP上报加密器
var reportURLPreferenceMutex sync.RWMutex
var preferredUploadURL string
var preferredConfigURL string
var reportDo = func(ctx context.Context, req *http.Request, timeout time.Duration) (*http.Response, error) {
	client := &http.Client{
		Timeout: timeout,
	}
	return client.Do(req.WithContext(ctx))
}

// TrafficReportItem 流量报告项（压缩格式）
// 线格式方向（N-01 钉死）：U=gost OutputBytes=远端→客户端方向字节，
// D=gost InputBytes=客户端→远端方向字节。字段名 U/D 是历史命名，
// 与「客户端上行/下行」字面相反，面板按 U=出、D=入解读，勿改映射。
type TrafficReportItem struct {
	N string `json:"n"` // 服务名（name缩写）
	U int64  `json:"u"` // 远端→客户端字节（gost OutputBytes）
	D int64  `json:"d"` // 客户端→远端字节（gost InputBytes）
}

func SetHTTPReportURL(addr string, secret string) {
	uploadURLs, configURLs := buildReportURLCandidates(addr, secret)
	if len(uploadURLs) > 0 {
		httpReportURL = strings.Join(uploadURLs, ",")
	}
	if len(configURLs) > 0 {
		configReportURL = strings.Join(configURLs, ",")
	}
	reportURLPreferenceMutex.Lock()
	preferredUploadURL = ""
	preferredConfigURL = ""
	reportURLPreferenceMutex.Unlock()

	// 创建 AES 加密器
	var err error
	httpAESCrypto, err = crypto.NewAESCrypto(secret)
	if err != nil {
		fmt.Printf("❌ 创建 HTTP AES 加密器失败: %v\n", err)
		httpAESCrypto = nil
	} else {
		fmt.Printf("🔐 HTTP AES 加密器创建成功\n")
	}
}

func buildReportURLCandidates(addr string, secret string) (upload []string, config []string) {
	normalizedAddr, explicitScheme := normalizeReportAddress(addr)
	if normalizedAddr == "" {
		normalizedAddr = strings.TrimSpace(addr)
	}

	schemes := []string{"https", "http"}
	if mappedScheme := mapToHTTPScheme(explicitScheme); mappedScheme == "http" {
		schemes = []string{"http", "https"}
	}

	upload = []string{
		schemes[0] + "://" + normalizedAddr + "/flow/upload?secret=" + secret,
		schemes[1] + "://" + normalizedAddr + "/flow/upload?secret=" + secret,
	}
	config = []string{
		schemes[0] + "://" + normalizedAddr + "/flow/config?secret=" + secret,
		schemes[1] + "://" + normalizedAddr + "/flow/config?secret=" + secret,
	}
	return upload, config
}

func normalizeReportAddress(addr string) (string, string) {
	raw := strings.TrimSpace(addr)
	if raw == "" {
		return "", ""
	}

	scheme := ""
	if idx := strings.Index(raw, "://"); idx > 0 {
		scheme = strings.ToLower(strings.TrimSpace(raw[:idx]))
		if parsed, err := url.Parse(raw); err == nil {
			if host := strings.TrimSpace(parsed.Host); host != "" {
				return host, scheme
			}
		}
		raw = raw[idx+3:]
	}

	if idx := strings.IndexAny(raw, "/?#"); idx >= 0 {
		raw = raw[:idx]
	}
	return strings.TrimSpace(raw), scheme
}

func mapToHTTPScheme(scheme string) string {
	switch strings.ToLower(strings.TrimSpace(scheme)) {
	case "https", "wss":
		return "https"
	case "http", "ws":
		return "http"
	default:
		return ""
	}
}

// sanitizeReportURL masks the node secret embedded in report URLs before
// the URL reaches any log line or aggregated error (D1-01). The secret is
// the node's identity credential and the AES key seed; the WebSocket side
// already sanitized its URLs (sanitizeWebSocketURL), the HTTP report side
// printed them in clear — same masking policy: secret value becomes ***.
func sanitizeReportURL(rawURL string) string {
	// Textual masking: keeps the rest of the URL byte-for-byte intact and
	// works even for malformed URLs. Report URLs always carry the secret
	// as a query parameter.
	idx := strings.Index(rawURL, "secret=")
	if idx < 0 {
		return rawURL
	}
	start := idx + len("secret=")
	end := strings.IndexAny(rawURL[start:], "&# ")
	if end < 0 {
		return rawURL[:start] + "***"
	}
	return rawURL[:start] + "***" + rawURL[start+end:]
}

func loadPreferredURL(preferred *string) string {
	if preferred == nil {
		return ""
	}

	reportURLPreferenceMutex.RLock()
	defer reportURLPreferenceMutex.RUnlock()
	return *preferred
}

func storePreferredURL(preferred *string, value string) {
	if preferred == nil {
		return
	}

	reportURLPreferenceMutex.Lock()
	defer reportURLPreferenceMutex.Unlock()
	*preferred = value
}

func prioritizeURLs(urls []string, preferred string) []string {
	ordered := append([]string(nil), urls...)
	if preferred == "" || len(ordered) < 2 {
		return ordered
	}

	for i, targetURL := range ordered {
		if targetURL == preferred {
			if i > 0 {
				ordered[0], ordered[i] = ordered[i], ordered[0]
			}
			break
		}
	}

	return ordered
}

func postJSONWithFallback(ctx context.Context, urls []string, requestBody []byte, userAgent string, timeout time.Duration, preferred *string) (bool, error) {
	if len(urls) == 0 {
		return false, fmt.Errorf("上报URL未设置")
	}

	orderedURLs := prioritizeURLs(urls, loadPreferredURL(preferred))

	var errs []string
	for i, targetURL := range orderedURLs {
		// D1-01: 日志与聚合错误只出现脱敏后的 URL（真实 URL 仅用于请求本身）。
		displayURL := sanitizeReportURL(targetURL)
		req, err := http.NewRequest("POST", targetURL, bytes.NewBuffer(requestBody))
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s => 创建请求失败: %v", displayURL, err))
			if i < len(orderedURLs)-1 {
				fmt.Printf("⚠️ HTTP上报尝试失败，准备回退: %s => 创建请求失败: %v\n", displayURL, err)
			}
			continue
		}

		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", userAgent)

		resp, err := reportDo(ctx, req, timeout)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s => 请求失败: %v", displayURL, err))
			if i < len(orderedURLs)-1 {
				fmt.Printf("⚠️ HTTP上报尝试失败，准备回退: %s => 请求失败: %v\n", displayURL, err)
			}
			continue
		}

		var responseBytes bytes.Buffer
		_, readErr := responseBytes.ReadFrom(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			errs = append(errs, fmt.Sprintf("%s => 读取响应失败: %v", displayURL, readErr))
			if i < len(orderedURLs)-1 {
				fmt.Printf("⚠️ HTTP上报尝试失败，准备回退: %s => 读取响应失败: %v\n", displayURL, readErr)
			}
			continue
		}

		if resp.StatusCode != http.StatusOK {
			errs = append(errs, fmt.Sprintf("%s => HTTP响应错误: %d %s", displayURL, resp.StatusCode, resp.Status))
			if i < len(orderedURLs)-1 {
				fmt.Printf("⚠️ HTTP上报尝试失败，准备回退: %s => HTTP响应错误: %d %s\n", displayURL, resp.StatusCode, resp.Status)
			}
			continue
		}

		responseText := strings.TrimSpace(responseBytes.String())
		if responseText == "ok" {
			if i > 0 {
				fmt.Printf("↪️ HTTP上报已自动回退到: %s\n", displayURL)
			}
			storePreferredURL(preferred, targetURL)
			return true, nil
		}

		errs = append(errs, fmt.Sprintf("%s => 服务器响应: %s (期望: ok)", displayURL, responseText))
		if i < len(orderedURLs)-1 {
			fmt.Printf("⚠️ HTTP上报尝试失败，准备回退: %s => 服务器响应: %s (期望: ok)\n", displayURL, responseText)
		}
	}

	return false, fmt.Errorf("发送HTTP请求失败: %s", strings.Join(errs, " | "))
}

// encryptReportPayload 加密上报数据（F-089: fail-closed）。
// 加密器缺失或加密失败时返回错误——上报宁可失败待重试，也不得静默
// 回退明文发送。旧实现三处发送函数都是 fail-open，与全仓下载校验
// fail-closed 的基调不一致，且会掩盖 secret 为空/不一致的配置错误。
func encryptReportPayload(data []byte) ([]byte, error) {
	if httpAESCrypto == nil {
		return nil, fmt.Errorf("上报加密器未初始化（节点 secret 为空或无效），拒绝明文发送")
	}
	encryptedData, err := httpAESCrypto.Encrypt(data)
	if err != nil {
		return nil, fmt.Errorf("加密上报数据失败: %v", err)
	}
	encryptedMessage := map[string]interface{}{
		"encrypted": true,
		"data":      encryptedData,
		"timestamp": time.Now().Unix(),
	}
	return json.Marshal(encryptedMessage)
}

// sendBatchTrafficReport 批量发送多个服务的流量报告到HTTP接口
func sendBatchTrafficReport(ctx context.Context, reportItems []TrafficReportItem) (bool, error) {
	if httpReportURL == "" {
		return false, fmt.Errorf("流量上报URL未设置")
	}

	jsonData, err := json.Marshal(reportItems)
	if err != nil {
		return false, fmt.Errorf("序列化报告数据失败: %v", err)
	}

	requestBody, err := encryptReportPayload(jsonData)
	if err != nil {
		return false, err
	}

	return postJSONWithFallback(
		ctx,
		strings.Split(httpReportURL, ","),
		requestBody,
		"GOST-Traffic-Reporter/1.0",
		5*time.Second,
		&preferredUploadURL,
	)
}

// sendConfigReport 发送配置报告到HTTP接口
func sendConfigReport(ctx context.Context) (bool, error) {
	if configReportURL == "" {
		return false, fmt.Errorf("配置上报URL未设置")
	}

	// 获取配置数据
	configData, err := getConfigData()
	if err != nil {
		return false, fmt.Errorf("获取配置数据失败: %v", err)
	}

	requestBody, err := encryptReportPayload(configData)
	if err != nil {
		return false, err
	}

	return postJSONWithFallback(
		ctx,
		strings.Split(configReportURL, ","),
		requestBody,
		"Config-Reporter/1.0",
		10*time.Second,
		&preferredConfigURL,
	)
}

// StartConfigReporter 启动配置定时上报器（每10分钟上报一次）
func StartConfigReporter(ctx context.Context) {
	if configReportURL == "" {
		fmt.Printf("⚠️ 配置上报URL未设置，跳过定时上报\n")
		return
	}

	fmt.Printf("🚀 配置定时上报器已启动，每10分钟上报一次（WebSocket连接稳定后启动）\n")

	// 创建10分钟定时器
	ticker := time.NewTicker(10 * time.Minute)
	defer ticker.Stop()

	// 立即执行一次配置上报
	go func() {
		success, err := sendConfigReport(ctx)
		if err != nil {
			fmt.Printf("❌ 初始配置上报失败: %v\n", err)
		} else if success {
			fmt.Printf("✅ 初始配置上报成功\n")
		}
	}()

	// 定时上报循环
	for {
		select {
		case <-ticker.C:
			go func() {
				success, err := sendConfigReport(ctx)
				if err != nil {
					fmt.Printf("❌ 定时配置上报失败: %v\n", err)
				} else if success {
					fmt.Printf("✅ 定时配置上报成功\n")
				}
			}()

		case <-ctx.Done():
			fmt.Printf("⏹️ 配置定时上报器已停止\n")
			return
		}
	}
}

// serviceStatus 接口定义
type serviceStatus interface {
	Status() *Status
}

// getConfigResponse 配置响应结构
type getConfigResponse struct {
	Config *config.Config `json:"config"`
}

// getConfigData 获取配置数据（避免循环依赖）
func getConfigData() ([]byte, error) {
	// Reporting is read-only: enriching a detached snapshot must not persist
	// stale state over a config currently being reloaded or acknowledged.
	cfg := config.Global()
	for _, svc := range cfg.Services {
		if svc == nil {
			continue
		}
		s := registry.ServiceRegistry().Get(svc.Name)
		ss, ok := s.(serviceStatus)
		if ok && ss != nil {
			status := ss.Status()
			svc.Status = &config.ServiceStatus{
				CreateTime: status.CreateTime().Unix(),
				State:      string(status.State()),
			}
			if st := status.Stats(); st != nil {
				svc.Status.Stats = &config.ServiceStats{
					TotalConns:   st.Get(stats.KindTotalConns),
					CurrentConns: st.Get(stats.KindCurrentConns),
					TotalErrs:    st.Get(stats.KindTotalErrs),
					InputBytes:   st.Get(stats.KindInputBytes),
					OutputBytes:  st.Get(stats.KindOutputBytes),
				}
			}
			for _, ev := range status.Events() {
				if !ev.Time.IsZero() {
					svc.Status.Events = append(svc.Status.Events, config.ServiceEvent{
						Time: ev.Time.Unix(),
						Msg:  ev.Message,
					})
				}
			}
		}
	}

	var resp getConfigResponse
	resp.Config = cfg

	buf := &bytes.Buffer{}
	resp.Config.Write(buf, "json")
	return buf.Bytes(), nil
}

// sendBatchTrafficReportWithSeq 带 seq 的上报（R5-B6 E-001）。
// seq 通过 HTTP 头 X-Flvx-Seq 传递（旧面板忽略未知头，body 保持数组格式）。
func sendBatchTrafficReportWithSeq(ctx context.Context, reportItems []TrafficReportItem, seq int64) (bool, error) {
	if httpReportURL == "" {
		return false, fmt.Errorf("流量上报URL未设置")
	}

	// Body 保持旧格式（数组），保证旧面板可解析。
	jsonData, err := json.Marshal(reportItems)
	if err != nil {
		return false, fmt.Errorf("序列化报告数据失败: %v", err)
	}

	requestBody, err := encryptReportPayload(jsonData)
	if err != nil {
		return false, err
	}

	// 改名过渡：seq 头双发（新头 X-Flmx-Seq + 旧头 X-Flvx-Seq），
	// 新旧面板都能读到，去重逻辑不受版本错位影响。
	seqStr := fmt.Sprintf("%d", seq)
	return postJSONWithFallbackAndHeaders(
		ctx,
		strings.Split(httpReportURL, ","),
		requestBody,
		"GOST-Traffic-Reporter/1.0",
		5*time.Second,
		&preferredUploadURL,
		map[string]string{"X-Flmx-Seq": seqStr, "X-Flvx-Seq": seqStr},
	)
}

// postJSONWithFallbackAndHeaders 带额外头的上报（R5-B6 E-001）。
func postJSONWithFallbackAndHeaders(ctx context.Context, urls []string, requestBody []byte, userAgent string, timeout time.Duration, preferred *string, headers map[string]string) (bool, error) {
	if len(urls) == 0 {
		return false, fmt.Errorf("上报URL未设置")
	}

	orderedURLs := prioritizeURLs(urls, loadPreferredURL(preferred))

	var errs []string
	for i, targetURL := range orderedURLs {
		// D1-01: 日志与聚合错误只出现脱敏后的 URL（真实 URL 仅用于请求本身）。
		displayURL := sanitizeReportURL(targetURL)
		req, err := http.NewRequest("POST", targetURL, bytes.NewBuffer(requestBody))
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s => 创建请求失败: %v", displayURL, err))
			continue
		}

		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", userAgent)
		for k, v := range headers {
			req.Header.Set(k, v)
		}

		resp, err := reportDo(ctx, req, timeout)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s => 请求失败: %v", displayURL, err))
			continue
		}

		var responseBytes bytes.Buffer
		_, readErr := responseBytes.ReadFrom(resp.Body)
		resp.Body.Close()
		if readErr != nil {
			errs = append(errs, fmt.Sprintf("%s => 读取响应失败: %v", displayURL, readErr))
			continue
		}

		if resp.StatusCode != http.StatusOK {
			errs = append(errs, fmt.Sprintf("%s => HTTP响应错误: %d %s", displayURL, resp.StatusCode, resp.Status))
			continue
		}

		responseText := strings.TrimSpace(responseBytes.String())
		if responseText == "ok" {
			if i > 0 {
				fmt.Printf("↪️ HTTP上报已自动回退到: %s\n", displayURL)
			}
			storePreferredURL(preferred, targetURL)
			return true, nil
		}

		errs = append(errs, fmt.Sprintf("%s => 服务器响应: %s (期望: ok)", displayURL, responseText))
	}

	return false, fmt.Errorf("发送HTTP请求失败: %s", strings.Join(errs, " | "))
}
