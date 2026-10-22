package handler

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go-backend/internal/http/response"
	"go-backend/internal/kernel"
	"go-backend/internal/store/model"
	"go-backend/internal/store/repo"
)

// R2X-030：仅改名 + 清各子字段，保存后各值不变。
func TestR2B6InboundUpdatePreservesCredentialFields(t *testing.T) {
	setup := func(t *testing.T, db string) (*repo.Repository, *Handler, int64, *model.NodeKernel) {
		r, err := repo.Open(t.TempDir() + "/" + db)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = r.Close() })
		h := New(r, "unit-test-secret")
		nodeID := seedRelayTargetNode(t, r, "b6-node", "198.51.100.40")
		k := seedB8Kernel(t, r, nodeID, kernel.TypeXray, "latest")
		return r, h, nodeID, k
	}

	t.Run("encryptionSecret", func(t *testing.T) {
		r, h, _, k := setup(t, "b6-pres-enc.db")
		entry := seedB6ENCInbound(t, r, k, "pres-enc", 24443)
		before := b6ReloadInbound(t, r, entry.ID)
		if before.Settings.EncryptionSecret == "" {
			t.Fatal("seed must carry an encryption secret")
		}
		out := b6SaveUpdate(t, h, entry, func(req *inboundRequest) {
			req.Name = "pres-enc-renamed"
			req.Settings.EncryptionSecret = ""
		})
		if out.Code != 0 {
			t.Fatalf("update: %+v", out)
		}
		if after := b6ReloadInbound(t, r, entry.ID); after.Settings.EncryptionSecret != before.Settings.EncryptionSecret {
			t.Fatalf("cleared encryptionSecret must be preserved, before %q after %q", before.Settings.EncryptionSecret, after.Settings.EncryptionSecret)
		}
	})

	t.Run("realityKeyAndShortIDs", func(t *testing.T) {
		r, h, _, k := setup(t, "b6-pres-reality.db")
		entry := seedChainInbound(t, r, k, "pres-reality", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 24443,
			Security: kernel.Security{Type: kernel.SecurityReality}}, 0)
		before := b6ReloadInbound(t, r, entry.ID)
		if before.Security.RealityPrivateKey == "" || len(before.Security.RealityShortIDs) == 0 {
			t.Fatal("seed must carry reality key and short ids")
		}
		out := b6SaveUpdate(t, h, entry, func(req *inboundRequest) {
			req.Name = "pres-reality-renamed"
			req.Security.RealityPrivateKey = ""
			req.Security.RealityPublicKey = ""
			req.Security.RealityShortIDs = nil
		})
		if out.Code != 0 {
			t.Fatalf("update: %+v", out)
		}
		after := b6ReloadInbound(t, r, entry.ID)
		if after.Security.RealityPrivateKey != before.Security.RealityPrivateKey || after.Security.RealityPublicKey != before.Security.RealityPublicKey {
			t.Fatalf("cleared reality keys must be preserved, before %q/%q after %q/%q",
				before.Security.RealityPrivateKey, before.Security.RealityPublicKey, after.Security.RealityPrivateKey, after.Security.RealityPublicKey)
		}
		if strings.Join(after.Security.RealityShortIDs, ",") != strings.Join(before.Security.RealityShortIDs, ",") {
			t.Fatalf("cleared reality short ids must be preserved, before %v after %v", before.Security.RealityShortIDs, after.Security.RealityShortIDs)
		}
	})

	t.Run("listenAndUUID", func(t *testing.T) {
		r, h, _, k := setup(t, "b6-pres-listen.db")
		entry := seedChainInbound(t, r, k, "pres-listen", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Listen: "0.0.0.0", Port: 24443,
			Security: kernel.Security{Type: kernel.SecurityNone}}, 0)
		before := b6ReloadInbound(t, r, entry.ID)
		if before.Listen != "0.0.0.0" || before.Settings.UUID == "" {
			t.Fatalf("seed listen/uuid: %+v", before)
		}
		out := b6SaveUpdate(t, h, entry, func(req *inboundRequest) {
			req.Name = "pres-listen-renamed"
			req.Listen = ""
			req.Settings.UUID = ""
		})
		if out.Code != 0 {
			t.Fatalf("update: %+v", out)
		}
		after := b6ReloadInbound(t, r, entry.ID)
		if after.Listen != before.Listen {
			t.Fatalf("cleared listen must be preserved, before %q after %q", before.Listen, after.Listen)
		}
		if after.Settings.UUID != before.Settings.UUID {
			t.Fatalf("cleared uuid must be preserved, before %q after %q", before.Settings.UUID, after.Settings.UUID)
		}
	})

	t.Run("password", func(t *testing.T) {
		r, h, _, k := setup(t, "b6-pres-password.db")
		entry := seedChainInbound(t, r, k, "pres-password", kernel.Inbound{Protocol: kernel.ProtocolTrojan, Port: 24443,
			Security: kernel.Security{Type: kernel.SecurityTLS, SNI: "p.example.com"}}, 0)
		before := b6ReloadInbound(t, r, entry.ID)
		if before.Settings.Password == "" {
			t.Fatal("seed must carry a password")
		}
		out := b6SaveUpdate(t, h, entry, func(req *inboundRequest) {
			req.Name = "pres-password-renamed"
			req.Settings.Password = ""
		})
		if out.Code != 0 {
			t.Fatalf("update: %+v", out)
		}
		if after := b6ReloadInbound(t, r, entry.ID); after.Settings.Password != before.Settings.Password {
			t.Fatalf("cleared password must be preserved, before %q after %q", before.Settings.Password, after.Settings.Password)
		}
	})
}

// 防过修：/inbound/regenerate 仍全轮换；创建留空仍自动生成。
func TestR2B6RegenerateStillRotatesAndCreateStillGenerates(t *testing.T) {
	r, err := repo.Open(t.TempDir() + "/b6-regen.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = r.Close() })
	h := New(r, "unit-test-secret")
	nodeID := seedRelayTargetNode(t, r, "b6-node", "198.51.100.41")
	k := seedB8Kernel(t, r, nodeID, kernel.TypeXray, "latest")

	entry := seedChainInbound(t, r, k, "regen-in", kernel.Inbound{Protocol: kernel.ProtocolVLESS, Port: 24443,
		Security: kernel.Security{Type: kernel.SecurityReality}}, 0)
	before := b6ReloadInbound(t, r, entry.ID)

	rec := httptest.NewRecorder()
	h.inboundRegenerate(rec, httptest.NewRequest(http.MethodPost, "/api/v1/inbound/regenerate", strings.NewReader(`{"id":`+jsonID(entry.ID)+`}`)))
	var out response.R
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.Code != 0 {
		t.Fatalf("regenerate: %+v", out)
	}
	after := b6ReloadInbound(t, r, entry.ID)
	if after.Settings.UUID == before.Settings.UUID {
		t.Fatal("regenerate must still rotate the UUID")
	}
	if after.Security.RealityPrivateKey == before.Security.RealityPrivateKey {
		t.Fatal("regenerate must still rotate the reality private key")
	}

	createBody := inboundRequest{
		NodeID: nodeID, KernelType: kernel.TypeXray, Name: "created-auto",
		Protocol: kernel.ProtocolVLESS, Port: 24444,
		Security: kernel.Security{Type: kernel.SecurityReality},
	}
	raw, err := json.Marshal(createBody)
	if err != nil {
		t.Fatal(err)
	}
	if out := callInboundSave(t, h, false, string(raw)); out.Code != 0 {
		t.Fatalf("create: %+v", out)
	}
	rows, err := r.ListNodeInbounds()
	if err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		if row.Name != "created-auto" {
			continue
		}
		in := b6ReloadInbound(t, r, row.ID)
		if in.Settings.UUID == "" || in.Security.RealityPrivateKey == "" || len(in.Security.RealityShortIDs) == 0 {
			t.Fatalf("create with empty credentials must still auto-generate: %+v", in)
		}
		return
	}
	t.Fatal("created inbound not found")
}
