//go:build !linux

package netcfg

import (
	"context"
	"errors"

	agentv1 "syncloud/internal/gen/syncloud/agent/v1"
)

type platform struct{}

func (platform) close() {}

func (m *Manager) apply(context.Context, *agentv1.NetworkConfig) (string, error) {
	return "", errors.New("private networking requires Linux")
}

func (m *Manager) peerStatus() []*agentv1.PeerStatus { return nil }
