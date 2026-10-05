package mesh

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"syncloud/internal/store"
)

func TestAddresses(t *testing.T) {
	if MeshAddr(1).String() != "10.90.0.1" || MeshAddr(258).String() != "10.90.1.2" || Subnet(7).String() != "10.91.7.0/24" {
		t.Fatal(MeshAddr(1), MeshAddr(258), Subnet(7))
	}
}

func TestAllocationAndConfig(t *testing.T) {
	ctx := context.Background()
	st, err := store.Open(ctx, filepath.Join(t.TempDir(), "t.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Now() // the release trigger stamps rows with the real clock
	add := func(id, name string) store.NodeNetwork {
		t.Helper()
		if _, err := st.W.ExecContext(ctx, `INSERT INTO nodes (id, name, status, cert_serial, created_at, status_at) VALUES (?, ?, 'pending', 'x', 0, 0)`, id, name); err != nil {
			t.Fatal(err)
		}
		nn, err := st.AllocateNodeNetwork(ctx, id, meshPool(name), subnetPool, Cooldown, now)
		if err != nil {
			t.Fatal(err)
		}
		return nn
	}
	w1 := add("n1", "w-1") // a worker joining first must not take 10.90.0.1
	ctl := add("n0", "ctl-0")
	if ctl.MeshIndex != 1 || w1.MeshIndex != 2 || w1.SubnetIndex != 1 || ctl.SubnetIndex != 2 {
		t.Fatalf("ctl=%+v w1=%+v", ctl, w1)
	}
	again, _ := st.AllocateNodeNetwork(ctx, "n1", meshPool("w-1"), subnetPool, Cooldown, now)
	if again.MeshIndex != 2 {
		t.Fatal("allocation not stable")
	}

	// Released addresses are not reused during the cool-down.
	if err := st.DeleteNode(ctx, "n1"); err != nil {
		t.Fatal(err)
	}
	w2 := add("n2", "w-2")
	if w2.MeshIndex == 2 || w2.SubnetIndex == 1 {
		t.Fatalf("reused during cool-down: %+v", w2)
	}

	st.SetNodeWireGuard(ctx, "n0", "KEY0", "203.0.113.1:51820", now)
	st.SetNodeWireGuard(ctx, "n2", "KEY2", "198.51.100.2:51820", now)
	if changed, _ := st.SetNodeWireGuard(ctx, "n2", "KEY2", "198.51.100.2:51820", now); changed {
		t.Fatal("unchanged update reported as change")
	}
	all, _ := st.ListNodeNetworks(ctx)
	self, _ := st.NodeNetwork(ctx, "n2")
	cfg := ConfigFor(self, all, 7)
	if cfg.NodeAddress != MeshAddr(w2.MeshIndex).String()+"/16" || len(cfg.Peers) != 1 {
		t.Fatalf("config: %+v", cfg)
	}
	p := cfg.Peers[0]
	if p.Name != "ctl-0" || p.Endpoint != "203.0.113.1:51820" || p.AllowedIps[0] != "10.90.0.1/32" || p.AllowedIps[1] != "10.91.2.0/24" {
		t.Fatalf("peer: %+v", p)
	}
}
