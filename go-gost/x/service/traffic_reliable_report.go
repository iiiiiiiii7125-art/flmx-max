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

func (e trafficReportRejection) Error() string { return "traffic batch rejected: " + string(e) }

// sendReliableTrafficReport 把一个 spool 批次发往 /flow/upload/v2。
// 返回 nil 仅当面板回执的 stream+sequence 与所发批次完全一致且无
// error；三路拒绝以 trafficReportRejection 返回；其余失败（网络、
// 回执不匹配、旧面板无此端点）返回普通错误，批次保留下个周期原样
// 重发。加密沿用 F-089 fail-closed：加密器缺失直接报错不发明文。
func sendReliableTrafficReport(ctx context.Context, batch trafficEnvelope) error {
	if httpReportURL == "" {
		return fmt.Errorf("流量上报URL未设置")
	}
	jsonData, err := json.Marshal(batch)
	if err != nil {
		return err
	}
	requestBody, err := encryptReportPayload(jsonData)
	if err != nil {
		return err
	}

	urls := prioritizeURLs(strings.Split(httpReportURL, ","), loadPreferredURL(&preferredUploadURL))
	var errs []string
	for _, target := range urls {
		parsed, err := url.Parse(target)
		if err != nil || parsed.Host == "" {
			continue
		}
		parsed.Path = "/flow/upload/v2"
		targetURL := parsed.String()
		displayURL := sanitizeReportURL(targetURL)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, targetURL, bytes.NewReader(requestBody))
		if err != nil {
			return err
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
			Error    string `json:"error"`
		}
		decodeErr := json.NewDecoder(io.LimitReader(resp.Body, 4096)).Decode(&ack)
		_ = resp.Body.Close()
		if decodeErr == nil && ack.Stream == batch.Stream && ack.Sequence == batch.Sequence {
			rejection := trafficReportRejection(ack.Error)
			switch {
			case resp.StatusCode == http.StatusConflict && (rejection == trafficRejectionResetStream || rejection == trafficRejectionIdentityConflict):
				return rejection
			case resp.StatusCode == http.StatusBadRequest && rejection == trafficRejectionInvalidBatch:
				return rejection
			case resp.StatusCode == http.StatusOK && ack.Error == "":
				storePreferredURL(&preferredUploadURL, target)
				return nil
			}
		}
		errs = append(errs, fmt.Sprintf("%s => HTTP %d, 回执与批次不匹配或面板未确认", displayURL, resp.StatusCode))
	}
	return fmt.Errorf("流量批次未获面板确认，保留待重试: %s", strings.Join(errs, " | "))
}
