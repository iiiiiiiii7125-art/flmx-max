package contract_test

import (
	"strings"
	"testing"

	"go-backend/internal/auth"
)

// Imported-node identity is address + protocol + credentials, not bare
// host:port. Residential gateways (IPRoyal style) serve many distinct
// exits from one host:port, told apart only by the username's session
// id; before this fix every entry after the first was rejected as
// "已经导入过".
func TestExternalImportCredentialIdentityContract(t *testing.T) {
	secret := "credential-identity-secret"
	router, _ := setupContractRouter(t, secret)
	admin, _ := auth.GenerateToken(1, "admin@user", 0, secret)

	link1 := "socks5://client-a_session-111:pw@198.51.100.60:1001#ip1"
	link2 := "socks5://client-a_session-222:pw@198.51.100.60:1001#ip2"
	out := kernelContractPost(t, router, admin, "/api/v1/inbound/import", `{"links":"`+link1+`\n`+link2+`"}`)
	if out.Code != 0 {
		t.Fatalf("same gateway, different sessions must both import: %+v", out)
	}
	data := out.Data.(map[string]interface{})
	if len(data["created"].([]interface{})) != 2 {
		t.Fatalf("both nodes must be created: %v", data)
	}
	for _, e := range data["errors"].([]interface{}) {
		if strings.Contains(e.(string), "已经导入过") {
			t.Fatalf("no duplicate rejection expected, got %v", data["errors"])
		}
	}

	// The exact same link again is a true duplicate.
	dup := kernelContractPost(t, router, admin, "/api/v1/inbound/import", `{"links":"`+link1+`"}`)
	if dup.Code == 0 || !strings.Contains(dup.Msg, "已经导入过") {
		t.Fatalf("re-importing the identical link must be skipped: %+v", dup)
	}

	// Same address and username, different password: a different node.
	link3 := "socks5://client-a_session-111:pw2@198.51.100.60:1001#ip3"
	out = kernelContractPost(t, router, admin, "/api/v1/inbound/import", `{"links":"`+link3+`"}`)
	if out.Code != 0 || len(out.Data.(map[string]interface{})["created"].([]interface{})) != 1 {
		t.Fatalf("different password must import as its own node: %+v", out)
	}
}

// Two unnamed nodes on one gateway with different credentials both get
// the parser's default name (host:port); the import must disambiguate
// the stored names with a " (2)" suffix instead of storing twins.
func TestExternalImportUnnamedDisambiguationContract(t *testing.T) {
	secret := "credential-identity-secret"
	router, _ := setupContractRouter(t, secret)
	admin, _ := auth.GenerateToken(1, "admin@user", 0, secret)

	link1 := "socks5://client-a_session-111:pw@198.51.100.61:1001"
	link2 := "socks5://client-a_session-222:pw@198.51.100.61:1001"
	out := kernelContractPost(t, router, admin, "/api/v1/inbound/import", `{"links":"`+link1+`\n`+link2+`"}`)
	if out.Code != 0 || len(out.Data.(map[string]interface{})["created"].([]interface{})) != 2 {
		t.Fatalf("both unnamed nodes must import: %+v", out)
	}
	names := map[string]bool{}
	for _, it := range kernelContractPost(t, router, admin, "/api/v1/inbound/list", `{}`).Data.([]interface{}) {
		names[it.(map[string]interface{})["name"].(string)] = true
	}
	if len(names) != 2 {
		t.Fatalf("stored names must differ, got %v", names)
	}
	if !names["198.51.100.61:1001"] || !names["198.51.100.61:1001 (2)"] {
		t.Fatalf("second node must carry the (2) suffix, got %v", names)
	}
}

// Editing one of two same-address, different-credential rows must not
// trip the dedupe check that guards the edit path.
func TestExternalEditCredentialSiblingsContract(t *testing.T) {
	secret := "credential-identity-secret"
	router, _ := setupContractRouter(t, secret)
	admin, _ := auth.GenerateToken(1, "admin@user", 0, secret)

	link1 := "socks5://client-a_session-111:pw@198.51.100.62:1001#ip1"
	link2 := "socks5://client-a_session-222:pw@198.51.100.62:1001#ip2"
	out := kernelContractPost(t, router, admin, "/api/v1/inbound/import", `{"links":"`+link1+`\n`+link2+`"}`)
	if out.Code != 0 {
		t.Fatalf("import: %+v", out)
	}
	created := out.Data.(map[string]interface{})["created"].([]interface{})
	id1 := int64(created[0].(float64))

	upd := kernelContractPost(t, router, admin, "/api/v1/inbound/external/update",
		`{"id":`+jsonInt64(id1)+`,"name":"ip1-renamed","host":"198.51.100.62","port":1001}`)
	if upd.Code != 0 || upd.Msg != "操作成功" {
		t.Fatalf("renaming one credential sibling must succeed: %+v", upd)
	}
	// Editing it onto the sibling's exact identity (same credentials via
	// a replacement link) must still be refused.
	clash := kernelContractPost(t, router, admin, "/api/v1/inbound/external/update",
		`{"id":`+jsonInt64(id1)+`,"name":"ip1-renamed","host":"198.51.100.62","port":1001,"link":"`+link2+`"}`)
	if clash.Code == 0 || !strings.Contains(clash.Msg, "凭据完全相同") {
		t.Fatalf("editing onto a sibling's identity must be refused: %+v", clash)
	}
}
