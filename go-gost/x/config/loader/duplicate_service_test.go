package loader_test

import (
	"fmt"
	"net"
	"strings"
	"testing"

	"github.com/go-gost/x/config"
	"github.com/go-gost/x/config/loader"
	_ "github.com/go-gost/x/handler/auto"
	_ "github.com/go-gost/x/listener/tcp"
	"github.com/go-gost/x/registry"
)

func loadDuplicateServices(t *testing.T, addr string) error {
	t.Helper()
	unlock := config.LockMutation()
	defer unlock()
	original := config.Global()
	defer config.Set(original)
	defer func() {
		for name := range registry.ServiceRegistry().GetAll() {
			registry.ServiceRegistry().Unregister(name)
		}
	}()

	svc := func(name string) *config.ServiceConfig {
		return &config.ServiceConfig{
			Name:     name,
			Addr:     addr,
			Handler:  &config.HandlerConfig{Type: "auto"},
			Listener: &config.ListenerConfig{Type: "tcp"},
		}
	}
	cfg := &config.Config{Services: []*config.ServiceConfig{
		svc("6_1_0_tcp"),
		svc("6_1_0_tcp"),
	}}
	return loader.Load(cfg)
}

// R17-C (R17-X-01): a poisoned gost.json with a duplicated service name
// must fail startup with an error that names the service — a bare
// "registry: duplicate object" gives forensics nothing to match against
// the on-disk config. With Addr :0 both listeners bind (ephemeral ports)
// so the failure surfaces at registration.
func TestLoadDuplicateServiceNamesError(t *testing.T) {
	err := loadDuplicateServices(t, "127.0.0.1:0")
	if err == nil {
		t.Fatal("expected duplicate service name to fail Load, got nil error")
	}
	if !strings.Contains(err.Error(), `register service "6_1_0_tcp"`) {
		t.Fatalf("error should name the duplicated service, got: %v", err)
	}
	if !strings.Contains(err.Error(), "registry: duplicate object") {
		t.Fatalf("error should keep the registry cause, got: %v", err)
	}
}

// R17-C: the production poisoned file fails even earlier — ParseService
// binds the listener eagerly, so the second 6_1_0_tcp dies on the same
// port with a bare "address already in use". That error must also name
// the service, or the log only shows a port number.
func TestLoadDuplicateServiceBindErrorNamesService(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()

	err = loadDuplicateServices(t, fmt.Sprintf("127.0.0.1:%d", port))
	if err == nil {
		t.Fatal("expected duplicate service on a fixed port to fail Load, got nil error")
	}
	if !strings.Contains(err.Error(), `parse service "6_1_0_tcp"`) {
		t.Fatalf("error should name the duplicated service, got: %v", err)
	}
	if !strings.Contains(err.Error(), "address already in use") {
		t.Fatalf("error should keep the bind cause, got: %v", err)
	}
}
