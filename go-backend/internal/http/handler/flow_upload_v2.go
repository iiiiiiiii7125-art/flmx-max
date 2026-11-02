package handler

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"strings"
	"time"

	"go-backend/internal/store/repo"
)

// 可靠上报 v2（/flow/upload/v2）：与 v1 的 /flow/upload 并存。
// v1 的 (node_id, seq) 幂等只防重复不防丢失，且面板游标回退时
// agent 无自愈路径（同 seq 被判重放回 ok、agent 清零，静默漏计）。
// v2 以 stream 身份＋序号＋内容摘要记游标（repo.TrafficStreamCursor）：
// agent 先落盘 spool 才发送，面板入账与游标推进同事务，回执带
// stream+sequence 供 agent 核对；异常按三路回执自愈——
//   - 409 reset_stream：序号缺口（面板备份恢复致游标回退/丢失），
//     agent 换新 stream、原字节重报；
//   - 409 identity_conflict：同序号不同内容，agent 隔离留证不重放；
//   - 400 invalid_batch：批次本身非法，agent 二分定位坏项。
// 旧 agent 继续走 v1，语义不变。

type flowUploadV2Envelope struct {
	Stream   string     `json:"stream"`
	Sequence int64      `json:"sequence"`
	Items    []flowItem `json:"items"`
}

func (h *Handler) flowUploadV2(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST required", http.StatusMethodNotAllowed)
		return
	}
	secret := r.URL.Query().Get("secret")
	node, err := h.repo.GetNodeBySecret(secret)
	if err != nil {
		log.Printf("flow upload v2 node lookup failed err=%v", err)
		http.Error(w, "database unavailable", http.StatusServiceUnavailable)
		return
	}
	if node == nil {
		http.Error(w, "invalid node", http.StatusUnauthorized)
		return
	}
	raw, err := readAndDecryptFlowBody(r.Body, secret)
	if err != nil {
		http.Error(w, "invalid traffic body", http.StatusBadRequest)
		return
	}
	var envelope flowUploadV2Envelope
	if json.Unmarshal([]byte(raw), &envelope) != nil || envelope.Items == nil ||
		envelope.Sequence <= 0 || !validFlowStreamID(envelope.Stream) {
		http.Error(w, "invalid traffic upload", http.StatusBadRequest)
		return
	}
	reject := func(status int, reason string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"stream": envelope.Stream, "sequence": envelope.Sequence, "error": reason,
		})
	}

	now := time.Now()
	metas, err := h.repo.GetFlowUploadForwardMetas(collectFlowUploadForwardIDs(envelope.Items))
	if err != nil {
		log.Printf("flow upload v2 metadata lookup failed node_id=%d err=%v", node.ID, err)
		http.Error(w, "traffic upload not committed", http.StatusServiceUnavailable)
		return
	}
	if err := validateFlowUploadV2Items(envelope.Items, metas); err != nil {
		log.Printf("flow upload v2 invalid batch node_id=%d err=%v", node.ID, err)
		reject(http.StatusBadRequest, "invalid_batch")
		return
	}
	batch := h.buildNodeFlowUploadBatch(node.ID, envelope.Items, metas)
	itemsJSON, _ := json.Marshal(envelope.Items)
	digest := fmt.Sprintf("%x", sha256.Sum256(itemsJSON))
	scope := fmt.Sprintf("flow:%d:%s", node.ID, envelope.Stream)
	quotaViews, applied, err := h.repo.ApplyTrafficStreamAccounting(
		batch.flowDeltas, batch.quotaUsage, now, scope, envelope.Sequence, digest)
	if err != nil {
		var gap *repo.TrafficStreamGap
		switch {
		case errors.As(err, &gap):
			reject(http.StatusConflict, "reset_stream")
		case errors.Is(err, repo.ErrTrafficStreamConflict):
			reject(http.StatusConflict, "identity_conflict")
		default:
			log.Printf("flow upload v2 failed node_id=%d err=%v", node.ID, err)
			http.Error(w, "traffic upload not committed", http.StatusServiceUnavailable)
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(struct {
		Stream   string `json:"stream"`
		Sequence int64  `json:"sequence"`
	}{envelope.Stream, envelope.Sequence})
	// 确认回执先落到 agent，入账后副作用（可能触发慢速节点控制
	// RPC）不阻塞确认——agent 超时未收到确认会重发，重放由游标
	// 摘要判定、不重复入账。
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	if applied {
		h.runFlowUploadSideEffects(node.ID, batch, quotaViews)
		h.recordTunnelMetricsFromForwardBatch(node.ID, batch.forwardTraffic, metas, now.UnixMilli())
	}
}

func validFlowStreamID(stream string) bool {
	decoded, err := hex.DecodeString(stream)
	return err == nil && len(decoded) == 16
}

// validateFlowUploadV2Items 是整批合法性门（任一项非法整批拒收、
// 回 invalid_batch 供 agent 二分定位）：负计数、批次总量溢出、
// 按计费方向缩放溢出。存储侧计费配置本身非法（倍率为负/NaN）
// 不是上报错误——跳过其缩放校验，由构建路径按既有守卫丢弃该
// 转发，与 v1 口径一致。
func validateFlowUploadV2Items(items []flowItem, metas map[int64]repo.FlowUploadForwardMeta) error {
	var rawTotal int64
	for i, item := range items {
		for _, value := range []int64{item.U, item.D} {
			if value < 0 || value > math.MaxInt64-rawTotal {
				return fmt.Errorf("item %d: invalid traffic counter or overflowing batch", i)
			}
			rawTotal += value
		}
		forwardID, _, _, ok := parseFlowServiceIDs(strings.TrimSpace(item.N))
		if !ok {
			continue
		}
		meta, exists := metas[forwardID]
		if !exists || meta.TrafficRatio < 0 || math.IsNaN(meta.TrafficRatio) {
			continue
		}
		if _, ok := scaleTrafficBytes(item.D, meta.TrafficRatio); !ok {
			return fmt.Errorf("item %d: scaled traffic exceeds int64", i)
		}
		if meta.TunnelFlow != 1 {
			if _, ok := scaleTrafficBytes(item.U, meta.TrafficRatio); !ok {
				return fmt.Errorf("item %d: scaled traffic exceeds int64", i)
			}
		}
	}
	return nil
}
