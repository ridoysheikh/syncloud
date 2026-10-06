package store

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestRejoinExistingNode(t *testing.T) {
	s := openTest(t)
	ctx := context.Background()
	now := time.Now().Truncate(time.Second)
	tok := func(hash, name string) {
		t.Helper()
		if err := s.CreateJoinToken(ctx, JoinToken{ID: "jt_" + hash, TokenHash: hash, CreatedAt: now, ExpiresAt: now.Add(time.Hour), SingleUse: true, NodeName: name}); err != nil {
			t.Fatal(err)
		}
	}
	tok("h1", "")
	if err := s.JoinNode(ctx, "h1", Node{ID: "node_a", Name: "w1", CertSerial: "s1", CreatedAt: now}, now); err != nil {
		t.Fatal(err)
	}
	// A plain token cannot take an existing name.
	tok("h2", "")
	if err := s.JoinNode(ctx, "h2", Node{ID: "node_b", Name: "w1", CertSerial: "s2", CreatedAt: now}, now); !errors.Is(err, ErrNameTaken) {
		t.Fatalf("plain token on existing name: %v", err)
	}
	// A token bound to w1 re-joins it: same ID, new certificate.
	tok("h3", "w1")
	target, ok, err := s.RejoinTarget(ctx, "h3", now)
	if err != nil || !ok || target.ID != "node_a" {
		t.Fatalf("RejoinTarget = %v %v %v", target, ok, err)
	}
	if err := s.JoinNode(ctx, "h3", Node{ID: target.ID, Name: "w1", CertSerial: "s3", CreatedAt: now}, now); err != nil {
		t.Fatal(err)
	}
	cur, prev, err := s.NodeCertSerials(ctx, "node_a")
	if err != nil || cur != "s3" || prev != "" {
		t.Fatalf("serials = %q %q %v", cur, prev, err)
	}
	// Single use.
	if _, ok, _ := s.RejoinTarget(ctx, "h3", now); ok {
		t.Fatal("token usable twice")
	}
}
