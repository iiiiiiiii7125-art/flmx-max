package handler

import (
	"io"
	"strings"
	"testing"
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
