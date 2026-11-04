package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"go-backend/internal/http/response"
)

type diagnosisStreamEvent struct {
	Type string      `json:"type"`
	Data interface{} `json:"data,omitempty"`
	TS   int64       `json:"ts"`
}

func prepareDiagnosisStreamResponse(w http.ResponseWriter) (http.Flusher, error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, errors.New("当前服务不支持流式响应")
	}
	w.Header().Set("Content-Type", "application/x-ndjson; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	return flusher, nil
}

func writeDiagnosisStreamEvent(encoder *json.Encoder, flusher http.Flusher, eventType string, data interface{}) error {
	if encoder == nil || flusher == nil {
		return errors.New("流式响应写入器未初始化")
	}
	event := diagnosisStreamEvent{Type: eventType, Data: data, TS: time.Now().UnixMilli()}
	if err := encoder.Encode(event); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func summarizeDiagnosisProgress(results []map[string]interface{}) diagnosisProgress {
	progress := diagnosisProgress{Total: len(results)}
	for _, item := range results {
		countDiagnosisResult(&progress, item)
	}
	return progress
}

func shouldIgnoreDiagnosisStreamError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return true
	}
	msg := strings.ToLower(strings.TrimSpace(err.Error()))
	if strings.Contains(msg, "broken pipe") || strings.Contains(msg, "connection reset by peer") {
		return true
	}
	if strings.Contains(msg, "stream already closed") {
		return true
	}
	return false
}

func (h *Handler) streamDiagnosisRuntime(ctx context.Context, cancel context.CancelFunc, w http.ResponseWriter, startPayload map[string]interface{}, workItems []diagnosisWorkItem) error {
	flusher, err := prepareDiagnosisStreamResponse(w)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(w)

	payload := map[string]interface{}{
		"total":     len(workItems),
		"timestamp": time.Now().UnixMilli(),
		"items":     h.buildDiagnosisStreamStartItems(workItems),
	}
	for key, value := range startPayload {
		payload[key] = value
	}
	if err := writeDiagnosisStreamEvent(encoder, flusher, "start", payload); err != nil {
		return err
	}

	streamBroken := false
	emitter := func(index int, item map[string]interface{}, progress diagnosisProgress) {
		if streamBroken {
			return
		}
		itemPayload := map[string]interface{}{
			"index":    index,
			"result":   item,
			"progress": progress,
		}
		if err := writeDiagnosisStreamEvent(encoder, flusher, "item", itemPayload); err != nil {
			streamBroken = true
			if cancel != nil {
				cancel()
			}
		}
	}

	results := h.runDiagnosisWorkItems(ctx, workItems, emitter)
	if streamBroken {
		return context.Canceled
	}

	progress := summarizeDiagnosisProgress(results)
	donePayload := map[string]interface{}{
		"progress": progress,
		"timedOut": errors.Is(ctx.Err(), context.DeadlineExceeded),
	}
	return writeDiagnosisStreamEvent(encoder, flusher, "done", donePayload)
}

func (h *Handler) tunnelDiagnoseStream(w http.ResponseWriter, r *http.Request) {
	// R8-T-05: the stream's done event must remain writable when a
	// diagnosis runs its full 120s ctx budget.
	extendWriteDeadline(w, diagnosisWriteDeadline)
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	id := asInt64FromBodyKey(r, w, "tunnelId")
	if id <= 0 {
		return
	}

	tunnelName, tunnelType, workItems, err := h.prepareTunnelDiagnosis(id)
	if err != nil {
		if strings.Contains(err.Error(), "不存在") || strings.Contains(err.Error(), "不完整") {
			response.WriteJSON(w, response.ErrDefault(err.Error()))
			return
		}
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), diagnosisRequestTimeout)
	defer cancel()

	startPayload := map[string]interface{}{
		"tunnelName": tunnelName,
		"tunnelType": tunnelType,
	}
	if err := h.streamDiagnosisRuntime(ctx, cancel, w, startPayload, workItems); err != nil {
		if shouldIgnoreDiagnosisStreamError(err) {
			return
		}
		if strings.Contains(err.Error(), "不支持流式响应") {
			response.WriteJSON(w, response.ErrDefault(err.Error()))
			return
		}
		return
	}
}

func (h *Handler) forwardDiagnoseStream(w http.ResponseWriter, r *http.Request) {
	// R8-T-05: see tunnelDiagnoseStream.
	extendWriteDeadline(w, diagnosisWriteDeadline)
	if r.Method != http.MethodPost {
		response.WriteJSON(w, response.ErrDefault("请求失败"))
		return
	}
	id := asInt64FromBodyKey(r, w, "forwardId")
	if id <= 0 {
		return
	}

	forward, _, _, err := h.resolveForwardAccess(r, id)
	if err != nil {
		if errors.Is(err, errForwardNotFound) {
			response.WriteJSON(w, response.ErrDefault("转发不存在"))
			return
		}
		if respondInternalIfRepoError(w, "forwardDiagnoseStream: resolveForwardAccess failed", err) {
			return
		}
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}

	// RV3-01：与非流式面共用 prepareForwardDiagnosisPlan——nft
	// 转发先出规则状态项、applied 后走真实探测，不再直走
	// prepareForwardDiagnosis 绕过 nft 分支（规则缺失时流式面
	// 曾只剩与规则无关的直拨探测、与非流式结论矛盾）。
	plan, err := h.prepareForwardDiagnosisPlan(forward)
	if err != nil {
		if strings.Contains(err.Error(), "不存在") || strings.Contains(err.Error(), "不能为空") || strings.Contains(err.Error(), "错误") {
			response.WriteJSON(w, response.ErrDefault(err.Error()))
			return
		}
		if respondInternalIfRepoError(w, "forwardDiagnoseStream: prepareForwardDiagnosisPlan failed", err) {
			return
		}
		response.WriteJSON(w, response.Err(-2, err.Error()))
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), diagnosisRequestTimeout)
	defer cancel()

	startPayload := map[string]interface{}{
		"forwardName": plan.name,
	}
	if err := h.streamForwardDiagnosisRuntime(ctx, cancel, w, startPayload, plan); err != nil {
		if shouldIgnoreDiagnosisStreamError(err) {
			return
		}
		if strings.Contains(err.Error(), "不支持流式响应") {
			response.WriteJSON(w, response.ErrDefault(err.Error()))
			return
		}
		return
	}
}

// diagnosisStreamStartItemFromResult 把既成结果项转成流式 start
// 占位项：标识字段（description/nodeId/targetIp/targetPort）与
// 结果项逐字一致——前端按这四元组把后续 item 事件对位替换；
// 其余字段先随占位下发，item 事件到达后以结果项为准。
func diagnosisStreamStartItemFromResult(item map[string]interface{}) map[string]interface{} {
	placeholder := make(map[string]interface{}, len(item)+1)
	for key, value := range item {
		placeholder[key] = value
	}
	placeholder["diagnosing"] = true
	placeholder["message"] = "诊断中..."
	return placeholder
}

// streamForwardDiagnosisRuntime 流式呈现 forwardDiagnosisPlan
// （RV3-01）：statusItems（nft 规则状态项）是既成结果，随 start
// 下发占位后立即以 item 事件给出；workItems 走与隧道共用的并发
// 执行器。进度以 statusItems+workItems 为 total，两类项在同一
// index 空间编号（statusItems 在前），结果集顺序与非流式
// diagnoseForwardRuntime 恒一致。
func (h *Handler) streamForwardDiagnosisRuntime(ctx context.Context, cancel context.CancelFunc, w http.ResponseWriter, startPayload map[string]interface{}, plan *forwardDiagnosisPlan) error {
	flusher, err := prepareDiagnosisStreamResponse(w)
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(w)

	startItems := make([]map[string]interface{}, 0, len(plan.statusItems)+len(plan.workItems))
	for _, item := range plan.statusItems {
		startItems = append(startItems, diagnosisStreamStartItemFromResult(item))
	}
	startItems = append(startItems, h.buildDiagnosisStreamStartItems(plan.workItems)...)
	payload := map[string]interface{}{
		"total":     len(startItems),
		"timestamp": time.Now().UnixMilli(),
		"items":     startItems,
	}
	for key, value := range startPayload {
		payload[key] = value
	}
	if err := writeDiagnosisStreamEvent(encoder, flusher, "start", payload); err != nil {
		return err
	}

	total := len(plan.statusItems) + len(plan.workItems)
	streamBroken := false
	writeItem := func(index int, item map[string]interface{}, progress diagnosisProgress) {
		if streamBroken {
			return
		}
		itemPayload := map[string]interface{}{
			"index":    index,
			"result":   item,
			"progress": progress,
		}
		if err := writeDiagnosisStreamEvent(encoder, flusher, "item", itemPayload); err != nil {
			streamBroken = true
			if cancel != nil {
				cancel()
			}
		}
	}

	progress := diagnosisProgress{Total: total}
	for i, item := range plan.statusItems {
		countDiagnosisResult(&progress, item)
		writeItem(i, item, progress)
	}
	base := progress
	offset := len(plan.statusItems)
	emitter := func(index int, item map[string]interface{}, probeProgress diagnosisProgress) {
		writeItem(offset+index, item, diagnosisProgress{
			Total:      total,
			Completed:  base.Completed + probeProgress.Completed,
			Success:    base.Success + probeProgress.Success,
			Failed:     base.Failed + probeProgress.Failed,
			Unverified: base.Unverified + probeProgress.Unverified,
		})
	}
	probeResults := h.runDiagnosisWorkItems(ctx, plan.workItems, emitter)
	if streamBroken {
		return context.Canceled
	}

	results := make([]map[string]interface{}, 0, total)
	results = append(results, plan.statusItems...)
	results = append(results, probeResults...)
	donePayload := map[string]interface{}{
		"progress": summarizeDiagnosisProgress(results),
		"timedOut": errors.Is(ctx.Err(), context.DeadlineExceeded),
	}
	return writeDiagnosisStreamEvent(encoder, flusher, "done", donePayload)
}
