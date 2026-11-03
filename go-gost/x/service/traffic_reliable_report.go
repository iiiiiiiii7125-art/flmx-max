package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// 面板对 v2 上报的三路拒绝（与回执 JSON 的 error 字段对应）。
const (
	trafficRejectionResetStream      = trafficReportRejection("reset_stream")
	trafficRejectionIdentityConflict = trafficReportRejection("identity_conflict")
	trafficRejectionInvalidBatch     = trafficReportRejection("invalid_batch")
)

// trafficReportRejection 表示面板明确拒收了当前待确认批次（区别于
// 网络失败/回执丢失——后者原样重试，前者走 recoverRejected 自愈）。
type trafficReportRejection string

// reliableReportURL 把 v1 上报候选 URL 改写为 v2 端点，只替换尾段
// 路径（审计 A-T-07）。前提订正（R3-C3-O1）：候选 URL 的唯一生产
// 者 buildReportURLCandidates 先经 normalizeReportAddress 归一
// 化——只取 host 形态、路径整体丢弃，再拼上 /flow/upload，故生产
// 候选恒为无路径前缀的 host 形态，「带反代路径前缀的候选」在现
// 行链路下不会出现。只替换尾段是防御性写法：候选形态若将来带上
// 前缀，v2 沿用同一前缀，不被整段覆盖丢掉；尾段不是 /flow/upload
// 的未知形态保持整段覆盖的旧行为兜底。
func reliableReportURL(parsed *url.URL) {
	if strings.HasSuffix(parsed.Path, "/flow/upload") {
		parsed.Path = strings.TrimSuffix(parsed.Path, "/flow/upload") + "/flow/upload/v2"
		return
	}
	parsed.Path = "/flow/upload/v2"
}

func (e trafficReportRejection) Error() string { return "traffic batch rejected: " + string(e) }

// sendReliableTrafficReport 把一个 spool 批次发往 /flow/upload/v2。
// 返回的 int64 是确认回执带的面板游标位置（C-C2-01）：新面板在
// 确认回执里附加 cursor 字段（判定后该 stream 的游标序号），旧
// 面板无此字段时为 0。error 为 nil 仅当面板回执的 stream+sequence
// 与所发批次完全一致且无 error；三路拒绝以 trafficReportRejection
// 返回；其余失败（网络、回执不匹配、旧面板无此端点）返回普通
// 错误，批次保留下个周期原样重发。加密沿用 F-089 fail-closed：
// 加密器缺失直接报错不发明文。
func sendReliableTrafficReport(ctx context.Context, batch trafficEnvelope) (int64, error) {
	if httpReportURL == "" {
		return 0, fmt.Errorf("流量上报URL未设置")
	}
	jsonData, err := json.Marshal(batch)
	if err != nil {
		return 0, err
	}
	requestBody, err := encryptReportPayload(jsonData)
	if err != nil {
		return 0, err
	}

	urls := prioritizeURLs(strings.Split(httpReportURL, ","), loadPreferredURL(&preferredUploadURL))
	var errs []string
	for _, target := range urls {
		parsed, err := url.Parse(target)
		if err != nil || parsed.Host == "" {
			continue
		}
		reliableReportURL(parsed)
		targetURL := parsed.String()
		displayURL := sanitizeReportURL(targetURL)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(requestBody))
		if err != nil {
			return 0, err
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("User-Agent", "GOST-Traffic-Reporter/2.0")

		resp, err := reportDo(ctx, req, 5*time.Second)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s => 请求失败: %v", displayURL, redactReportError(err, targetURL, displayURL)))
			continue
		}
		var ack struct {
			Stream   string `json:"stream"`
			Sequence int64  `json:"sequence"`
			Cursor   int64  `json:"cursor"`
			Error    string `json:"error"`
		}
		decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&ack)
		_ = resp.Body.Close()
		if decodeErr == nil && ack.Stream == batch.Stream && ack.Sequence == batch.Sequence {
			rejection := trafficReportRejection(ack.Error)
			switch {
			case resp.StatusCode == http.StatusConflict && (rejection == trafficRejectionResetStream || rejection == trafficRejectionIdentityConflict):
				return 0, rejection
			case resp.StatusCode == http.StatusBadRequest && rejection == trafficRejectionInvalidBatch:
				return 0, rejection
			case resp.StatusCode == http.StatusOK && ack.Error == "":
				storePreferredURL(&preferredUploadURL, target)
				return ack.Cursor, nil
			}
		}
		errs = append(errs, fmt.Sprintf("%s => HTTP %d, 回执与批次不匹配或面板未确认", displayURL, resp.StatusCode))
	}
	return 0, fmt.Errorf("流量批次未获面板确认，保留待重试: %s", strings.Join(errs, " | "))
}
