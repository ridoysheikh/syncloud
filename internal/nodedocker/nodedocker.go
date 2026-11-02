// Package nodedocker asks agents to measure and clean the Docker host of
// their node (node management), over the agent stream.
package nodedocker

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/ridoysheikh/syncloud/internal/agentgw"
	"github.com/ridoysheikh/syncloud/internal/auth"
	agentv1 "github.com/ridoysheikh/syncloud/internal/gen/syncloud/agent/v1"
	"github.com/ridoysheikh/syncloud/internal/store"
)

// ErrOffline means the node is not connected.
var ErrOffline = errors.New("the node is not connected")

// ErrOutdated means the node's agent predates this feature.
var ErrOutdated = errors.New("the node's agent does not support this yet: upgrade it under Settings → Updates")

type call struct {
	node string
	ch   chan *agentv1.DockerReply
}

type Relay struct {
	gw      *agentgw.Gateway
	mu      sync.Mutex
	pending map[string]*call
}

func New(gw *agentgw.Gateway) *Relay { return &Relay{gw: gw, pending: map[string]*call{}} }

func (r *Relay) Hooks() agentgw.Hooks {
	return agentgw.Hooks{OnDockerReply: r.reply, OnDisconnect: r.gone}
}

func (r *Relay) reply(_ store.Node, rep *agentv1.DockerReply) {
	r.mu.Lock()
	c := r.pending[rep.GetRequestId()]
	delete(r.pending, rep.GetRequestId())
	r.mu.Unlock()
	if c != nil {
		c.ch <- rep
	}
}

// gone fails the calls waiting on a node that disconnected.
func (r *Relay) gone(n store.Node) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, c := range r.pending {
		if c.node == n.ID {
			delete(r.pending, id)
			c.ch <- &agentv1.DockerReply{RequestId: id, Error: ErrOffline.Error()}
		}
	}
}

// Call runs op on the node's agent with params (JSON-encoded) and decodes
// the answer into out.
func (r *Relay) Call(ctx context.Context, nodeID, op string, params, out any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	id := auth.NewID("dkr_")
	c := &call{node: nodeID, ch: make(chan *agentv1.DockerReply, 1)}
	r.mu.Lock()
	r.pending[id] = c
	r.mu.Unlock()
	drop := func() {
		r.mu.Lock()
		delete(r.pending, id)
		r.mu.Unlock()
	}
	err = r.gw.Send(nodeID, &agentv1.ConnectResponse{Msg: &agentv1.ConnectResponse_DockerRequest{DockerRequest: &agentv1.DockerRequest{RequestId: id, Op: op, Params: raw}}})
	if err != nil {
		drop()
		if errors.Is(err, agentgw.ErrNotConnected) {
			return ErrOffline
		}
		return err
	}
	select {
	case rep := <-c.ch:
		if rep.GetError() != "" {
			return errors.New(rep.GetError())
		}
		return json.Unmarshal(rep.GetPayload(), out)
	case <-ctx.Done():
		drop()
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return errors.New("the node did not answer in time: its agent may be busy, disconnected, or older than 0.3.2 (upgrade it under Settings → Updates)")
		}
		return ctx.Err()
	}
}
