package contract_test

import (
	"encoding/base64"
	"strings"
	"testing"

	"go-backend/internal/auth"
)

// F-003 variant entry 3 (edit path): two imported nodes on different hosts
// may share a port. Importing A(:443) and B(:8443), then editing B's port
// to 443 must succeed — before the fix the node_inbound(node_id, port)
// full unique index rejected the save because imported rows all carry
// node_id=0. (Entries 1 and 2 — manual import and subscription-source
// sync — are covered by TestExternalImportFromSubscriptionURLContract
// and TestRemoteSourceContract.)
func TestExternalEditToSharedPortContract(t *testing.T) {
	secret := "f003-secret"
	router, _ := setupContractRouter(t, secret)
	admin, _ := auth.GenerateToken(1, "admin@user", 0, secret)

	enc := base64.RawURLEncoding.EncodeToString([]byte("aes-256-gcm:secret"))
	linkA := "ss://" + enc + "@198.51.100.40:443#nodeA"
	linkB := "ss://" + enc + "@198.51.100.41:8443#nodeB"
	out := kernelContractPost(t, router, admin, "/api/v1/inbound/import", `{"links":"`+linkA+`\n`+linkB+`"}`)
	if out.Code != 0 {
		t.Fatalf("import: %+v", out)
	}
	created := out.Data.(map[string]interface{})["created"].([]interface{})
	if len(created) != 2 {
		t.Fatalf("both nodes must import: %+v", out.Data)
	}
	idB := int64(created[1].(float64))

	upd := kernelContractPost(t, router, admin, "/api/v1/inbound/external/update",
		`{"id":`+jsonInt64(idB)+`,"name":"nodeB","host":"198.51.100.41","port":443,"method":"aes-256-gcm","password":"secret"}`)
	if upd.Code != 0 || upd.Msg != "操作成功" {
		t.Fatalf("editing imported node B to port 443 (shared with A on another host) must succeed: %+v", upd)
	}

	list := kernelContractPost(t, router, admin, "/api/v1/inbound/list", `{}`).Data.([]interface{})
	ports := map[string]int{}
	for _, it := range list {
		m := it.(map[string]interface{})
		ports[m["name"].(string)] = valueAsInt(m["port"])
	}
	if ports["nodeA"] != 443 || ports["nodeB"] != 443 {
		t.Fatalf("both imported nodes must sit on 443 afterwards: %v", ports)
	}
	if !strings.Contains(linkA, "198.51.100.40") {
		t.Fatal("unreachable guard")
	}
}
