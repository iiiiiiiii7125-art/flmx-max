package service

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func TestGlobalTrafficManagerRemoveServicesDropsCachedEntries(t *testing.T) {
	m := &GlobalTrafficManager{serviceTraffic: map[string]*ServiceTraffic{
		"svc-a": {ServiceName: "svc-a"},
		"svc-b": {ServiceName: "svc-b"},
	}}

	m.RemoveServices("svc-a")

	if _, ok := m.serviceTraffic["svc-a"]; ok {
		t.Fatalf("expected svc-a traffic entry to be removed")
	}
	if _, ok := m.serviceTraffic["svc-b"]; !ok {
		t.Fatalf("expected svc-b traffic entry to remain")
	}
}

func TestGlobalTrafficManagerRetainServicesDropsStaleEntries(t *testing.T) {
	m := &GlobalTrafficManager{serviceTraffic: map[string]*ServiceTraffic{
		"svc-a": {ServiceName: "svc-a"},
		"svc-b": {ServiceName: "svc-b"},
	}}

	m.RetainServices(map[string]struct{}{"svc-b": {}})

	if _, ok := m.serviceTraffic["svc-a"]; ok {
		t.Fatalf("expected stale svc-a traffic entry to be removed")
	}
	if _, ok := m.serviceTraffic["svc-b"]; !ok {
		t.Fatalf("expected active svc-b traffic entry to remain")
	}
}

func TestGlobalTrafficManagerAddTrafficIgnoresUnregisteredService(t *testing.T) {
	m := &GlobalTrafficManager{serviceTraffic: make(map[string]*ServiceTraffic)}

	m.AddTraffic("deleted-service", 10, 20)

	if _, ok := m.serviceTraffic["deleted-service"]; ok {
		t.Fatalf("expected unregistered service traffic to be ignored")
	}
}

func TestGlobalTrafficManagerCollectAndReportDropsStaleEntriesAfterReporting(t *testing.T) {
	origReportDo := reportDo
	origReportURL := httpReportURL
	origAESCrypto := httpAESCrypto
	defer func() {
		reportDo = origReportDo
		httpReportURL = origReportURL
		httpAESCrypto = origAESCrypto
	}()

	// F-089 后加密器缺失即拒绝发送，桩装真实加密器并解密后断言。
	captured := stubV2Panel(t, "abc", func(env trafficEnvelope) (*http.Response, error) {
		return v2AckResponse(env)
	})

	spool, err := openTrafficSpool("")
	if err != nil {
		t.Fatal(err)
	}
	m := &GlobalTrafficManager{
		serviceTraffic: map[string]*ServiceTraffic{
			"stale-service": {ServiceName: "stale-service", UpBytes: 10, DownBytes: 20},
		},
		spool: spool,
		ctx:   context.Background(),
	}

	m.collectAndReport()

	if len(*captured) != 1 || !strings.Contains((*captured)[0].raw, "stale-service") {
		t.Fatalf("expected pending stale traffic to be reported first, got %+v", *captured)
	}
	if _, ok := m.serviceTraffic["stale-service"]; ok {
		t.Fatalf("expected stale traffic entry to be removed after report collection")
	}
}
