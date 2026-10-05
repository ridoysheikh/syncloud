package store

import (
	"context"
	"encoding/json"
	"time"
)

type AuditEvent struct {
	At        time.Time
	ActorID   string // empty for unauthenticated actions (e.g. failed login)
	Action    string // e.g. "auth:Login", "setup:CreateRoot"
	Resource  string // e.g. "srn:syncloud:user/<id>"
	IP        string
	UserAgent string
	Detail    map[string]any
}

func (s *Store) WriteAudit(ctx context.Context, e AuditEvent) error {
	detail := []byte("{}")
	if len(e.Detail) > 0 {
		var err error
		if detail, err = json.Marshal(e.Detail); err != nil {
			return err
		}
	}
	var actor any
	if e.ActorID != "" {
		actor = e.ActorID
	}
	_, err := s.W.ExecContext(ctx,
		`INSERT INTO audit_events (at, actor_id, action, resource, ip, user_agent, detail) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		e.At.UnixMilli(), actor, e.Action, e.Resource, e.IP, e.UserAgent, string(detail))
	return err
}
