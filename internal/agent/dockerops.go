package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	agentv1 "github.com/ridoysheikh/syncloud/internal/gen/syncloud/agent/v1"

	"github.com/ridoysheikh/syncloud/internal/dockerusage"
)

// dockerRequest answers the controller's node-management requests: measure
// what Docker holds, or clean some of it.
func (a *agentLink) dockerRequest(ctx context.Context, req *agentv1.DockerRequest) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Minute)
	defer cancel()
	reply := &agentv1.DockerReply{RequestId: req.GetRequestId()}
	var out any
	var err error
	switch req.GetOp() {
	case dockerusage.OpUsage:
		out, err = a.runner.docker.Usage(ctx)
	case dockerusage.OpClean:
		var cr dockerusage.CleanRequest
		if err = json.Unmarshal(req.GetParams(), &cr); err == nil {
			var res dockerusage.CleanResult
			res, err = a.runner.docker.Clean(ctx, cr)
			out = res
			if err != nil && (res.Removed > 0 || res.ReclaimedBytes > 0) {
				// Report what was done together with the error.
				res.Errors = append(res.Errors, err.Error())
				out, err = res, nil
			}
		}
	default:
		err = fmt.Errorf("unknown docker operation %q", req.GetOp())
	}
	if err != nil {
		reply.Error = err.Error()
	} else if reply.Payload, err = json.Marshal(out); err != nil {
		reply.Error = err.Error()
	}
	a.report(&agentv1.ConnectRequest{Msg: &agentv1.ConnectRequest_DockerReply{DockerReply: reply}})
}
