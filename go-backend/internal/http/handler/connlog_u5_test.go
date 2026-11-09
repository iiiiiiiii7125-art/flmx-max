package handler

import (
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// U5: connLogEnabled 在入站保存 API 的解码口径——在场才是指针非空
// （更新时省略＝保持存量，见 inboundSave 的指针惯例），显式
// true/false 都须原样到达落库赋值点。
func TestDecodeInboundRequestConnLogEnabled(t *testing.T) {
	decode := func(body string) (inboundRequest, map[string]bool) {
		var req inboundRequest
		present, err := decodeInboundRequest(io.NopCloser(strings.NewReader(body)), &req)
		if err != nil {
			t.Fatalf("decode %s: %v", body, err)
		}
		return req, present
	}

	req, present := decode(`{"name":"a","connLogEnabled":true}`)
	if req.ConnLogEnabled == nil || !*req.ConnLogEnabled {
		t.Fatalf("explicit true must decode to a non-nil true pointer, got %v", req.ConnLogEnabled)
	}
	if !present["connLogEnabled"] {
		t.Fatal("presence map must carry connLogEnabled")
	}

	req, _ = decode(`{"name":"a","connLogEnabled":false}`)
	if req.ConnLogEnabled == nil || *req.ConnLogEnabled {
		t.Fatalf("explicit false must decode to a non-nil false pointer, got %v", req.ConnLogEnabled)
	}

	req, present = decode(`{"name":"a"}`)
	if req.ConnLogEnabled != nil {
		t.Fatal("omitted connLogEnabled must stay nil so updates keep the stored switch")
	}
	if present["connLogEnabled"] {
		t.Fatal("presence map must not carry an omitted connLogEnabled")
	}

	// 落库赋值口径与 inboundSave 同形：指针非空才覆盖。
	row := 0
	if req.ConnLogEnabled != nil {
		row = boolToInt(*req.ConnLogEnabled)
	}
	if row != 0 {
		t.Fatal("omitted switch must not touch the stored value")
	}
}

// seedU5Inbound 直接落一行入站（applyInboundToModel 不映射
// ConnLogEnabled，故在建行前显式赋值），供 payload 名单断言用。
func seedU5Inbound(t *testing.T, r *repo.Repository, k *model.NodeKernel, name string, port int, enabled, connLog bool, companionOf int64) *model.NodeInbound {
	t.Helper()
	in := kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: port,
		Security: kernel.Security{Type: kernel.SecurityNone}}
	if err := kernel.Normalize(k.KernelType, &in); err != nil {
		t.Fatal(err)
	}
	row := model.NodeInbound{KernelID: k.ID, NodeID: k.NodeID, Name: name,
		CompanionOf: companionOf, CreatedTime: time.Now().UnixMilli()}
	if enabled {
		row.Enabled = 1
	}
	if connLog {
		row.ConnLogEnabled = 1
	}
	if err := applyInboundToModel(in, &row); err != nil {
		t.Fatal(err)
	}
	if err := r.CreateNodeInbound(&row); err != nil {
		t.Fatal(err)
	}
	return &row
}

// U5↔U4 契约：KernelApply 载荷的 connLogInbounds 恰为渲染集中开启
// 采集的非伴生入站——未开启、已停用、伴生三类都不得入列；无入选
// 时键整体缺席（agent 解码为空集、采集器停用）。
func TestBuildKernelApplyPayloadConnLogInbounds(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/connlog-u5.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")

	node := seedRelayTargetNode(t, r, "u5-node", "198.51.100.30")
	k := seedChainKernel(t, r, node, kernel.TypeXray)

	optedIn := seedU5Inbound(t, r, k, "opted-in", 8443, true, true, 0)
	seedU5Inbound(t, r, k, "plain", 8444, true, false, 0)
	seedU5Inbound(t, r, k, "disabled-opted-in", 8445, false, true, 0)
	seedU5Inbound(t, r, k, "companion", 8446, true, true, optedIn.ID)

	payload, _, err := h.buildKernelApplyPayload(k)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := payload["connLogInbounds"].([]int64)
	if !ok {
		t.Fatalf("connLogInbounds missing or wrong type: %T %v", payload["connLogInbounds"], payload["connLogInbounds"])
	}
	if want := []int64{optedIn.ID}; !reflect.DeepEqual(got, want) {
		t.Fatalf("connLogInbounds = %v, want %v", got, want)
	}

	k2 := seedChainKernel(t, r, node, kernel.TypeSingbox)
	seedU5Inbound(t, r, k2, "plain-sb", 9443, true, false, 0)
	payload2, _, err := h.buildKernelApplyPayload(k2)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := payload2["connLogInbounds"]; ok {
		t.Fatalf("connLogInbounds must be absent when no inbound opted in, got %v", payload2["connLogInbounds"])
	}
}
