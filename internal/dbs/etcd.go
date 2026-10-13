package dbs

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ridoysheikh/syncloud/internal/agentgw"
	"github.com/ridoysheikh/syncloud/internal/auth"
	agentv1 "github.com/ridoysheikh/syncloud/internal/gen/syncloud/agent/v1"
	"github.com/ridoysheikh/syncloud/internal/store"
	"github.com/ridoysheikh/syncloud/internal/workload"
)

// The platform etcd (Phase 13) is Patroni's coordination store for every
// PostgreSQL cluster. It runs on the nodes, so leader elections and
// failovers keep working while the controller is down. Three members on
// distinct nodes (one while the cluster has fewer nodes); members join as
// learners and get their vote once caught up, and a member whose node is
// lost for good is removed and replaced, one change at a time.

const (
	EtcdPrefix = "dbe_"
	// EtcdServiceID labels etcd containers for security groups.
	EtcdServiceID  = "etcd"
	etcdZone       = "etcd." + Zone
	etcdClientPort = 2379
	etcdPeerPort   = 2380
	etcdSize       = 3
	etcdLostAfter  = 10 * time.Minute
	// etcdLearnerTimeout is how long a new member may take to start and
	// catch up (its node pulls the PostgreSQL image first).
	etcdLearnerTimeout = 15 * time.Minute
	etcdRootKey        = "etcd.root_password" // sealed, in settings
	etcdAuthKey        = "etcd.auth_enabled"
)

func etcdName(ord int) string { return fmt.Sprintf("e%d", ord) }

// EtcdHost is a member's DNS name.
func EtcdHost(ord int) string { return etcdName(ord) + "." + etcdZone }

func etcdPeerURL(ord int) string { return fmt.Sprintf("http://%s:%d", EtcdHost(ord), etcdPeerPort) }

func etcdVolume(ord int) string { return "syncloud-etcd-" + etcdName(ord) }

// etcdTaskSpec runs one etcd member from the PostgreSQL image.
func etcdTaskSpec(image string, mb store.EtcdMember, dns, search []string) *agentv1.TaskSpec {
	name := etcdName(mb.Ordinal)
	state := "existing"
	if mb.NewCluster {
		state = "new"
	}
	return &agentv1.TaskSpec{
		TaskId: mb.ID, Name: "syncloud-etcd-" + name, Image: image, NetworkMode: workload.Network,
		Restart: agentv1.RestartPolicy_RESTART_POLICY_UNLESS_STOPPED,
		Mounts:  []*agentv1.Mount{{Type: agentv1.Mount_TYPE_VOLUME, Source: etcdVolume(mb.Ordinal), Target: "/data"}},
		Command: []string{"etcd",
			"--name=" + name, "--data-dir=/data/etcd",
			fmt.Sprintf("--listen-client-urls=http://0.0.0.0:%d", etcdClientPort),
			fmt.Sprintf("--advertise-client-urls=http://%s:%d", EtcdHost(mb.Ordinal), etcdClientPort),
			fmt.Sprintf("--listen-peer-urls=http://0.0.0.0:%d", etcdPeerPort),
			"--initial-advertise-peer-urls=" + etcdPeerURL(mb.Ordinal),
			"--initial-cluster=" + mb.Cluster, "--initial-cluster-state=" + state,
			"--initial-cluster-token=syncloud-etcd",
			"--auto-compaction-retention=1h", "--quota-backend-bytes=1073741824",
		},
		Env:              map[string]string{"ETCD_SELF": EtcdHost(mb.Ordinal)},
		NetworkAliases:   []string{EtcdHost(mb.Ordinal)},
		MemoryLimitBytes: 256 << 20,
		DnsServers:       dns, DnsSearch: search,
		Labels: map[string]string{"syncloud.service": "etcd", "syncloud.service_id": EtcdServiceID, "syncloud.etcd_member": name},
		System: true,
	}
}

// EtcdHosts are the client addresses Patroni starts from: every name a
// member can have, so members are not restarted as etcd grows or a member
// moves. Patroni skips the names that do not answer and learns the
// cluster's members from the first that does.
func EtcdHosts() []string {
	var out []string
	for i := range etcdSize {
		out = append(out, fmt.Sprintf("%s:%d", EtcdHost(i), etcdClientPort))
	}
	return out
}

func (m *Manager) needsEtcd(ctx context.Context) bool {
	dbs, _ := m.st.ListDatabases(ctx)
	for _, d := range dbs {
		if d.Engine == EnginePostgres && !d.Deleting {
			return true
		}
	}
	return false
}

// reconcileEtcd keeps the platform etcd at its size. It starts with one
// member and grows one learner at a time: a learner has no vote, so a member
// that is slow to pull its image, or never starts, cannot cost the cluster
// its quorum. It gets its vote once it has caught up with the leader.
func (m *Manager) reconcileEtcd(ctx context.Context) {
	members, err := m.st.EtcdMembers(ctx)
	if err != nil {
		return
	}
	if len(members) == 0 && !m.needsEtcd(ctx) {
		return
	}
	// etcd runs from the default PostgreSQL image.
	if err := m.imageReady(ctx, m.pgImage(pgDefaultVersion)); err != nil {
		return
	}
	now := m.now().UTC()
	// What etcd lists (it needs a quorum to answer): nil while it does not.
	list, lerr := m.etcdList(ctx)
	m.mu.Lock()
	m.etcdUp = lerr == nil
	m.mu.Unlock()
	if list != nil {
		// Authentication only needs etcd to answer: PostgreSQL clusters
		// wait for it, so it never waits for etcd to grow.
		if err := m.etcdEnableAuth(ctx); err != nil {
			m.log.Warn("etcd auth", "err", err)
		}
		m.etcdLearnIDs(ctx, members, list)
		if m.etcdTidy(ctx, members, list, now) {
			return // one change at a time
		}
		m.etcdPromote(ctx, members, list)
	}

	// Replace a member whose node is gone for good, if the rest keep quorum.
	for _, mb := range members {
		n, ok := m.nodes.Get(mb.NodeID)
		gone := !ok || (n.Status == store.NodeNotReady && now.Sub(n.StatusAt) > etcdLostAfter)
		if !gone || len(members) == 1 {
			continue
		}
		if mb.MemberID != "" {
			id, _ := strconv.ParseUint(mb.MemberID, 16, 64)
			if err := m.etcdCall(ctx, "cluster/member/remove", map[string]string{"ID": strconv.FormatUint(id, 10)}, nil); err != nil {
				m.log.Warn("remove a lost etcd member", "member", etcdName(mb.Ordinal), "err", err)
				return
			}
		}
		m.log.Warn("replacing an etcd member on a lost node", "member", etcdName(mb.Ordinal))
		m.etcdDrop(ctx, mb)
		return // one change at a time
	}

	want := 1
	if m.readyNodes() >= etcdSize {
		want = etcdSize
	}
	want = max(want, len(members)) // never shrink
	var used []string
	for _, x := range members {
		used = append(used, m.nodeName(x.NodeID))
	}
	placeNode := func() string {
		ws := workload.Spec{Resources: workload.Resources{CPU: 0.05, Memory: 128}, Placement: workload.Placement{Strategy: "spread"}}.Avoiding(used)
		nodeID, why := m.wl.PlaceSpec(ctx, ws)
		if nodeID == "" {
			m.etcdNote("cannot place an etcd member: " + why)
		}
		return nodeID
	}
	create := func(ord int, nodeID, cluster string, isNew bool, memberID string) bool {
		mb := store.EtcdMember{ID: auth.NewID(EtcdPrefix), Ordinal: ord, NodeID: nodeID, State: store.TaskPending,
			Cluster: cluster, NewCluster: isNew, MemberID: memberID, CreatedAt: now.Truncate(time.Second)}
		if err := m.st.CreateEtcdMember(ctx, mb); err != nil {
			m.log.Error("create etcd member", "err", err)
			return false
		}
		m.etcdNote("")
		m.etcdSend(ctx, &mb)
		m.changed()
		return true
	}

	switch {
	case len(members) == 0:
		// Bootstrap a one-member cluster; the others join as learners.
		if nodeID := placeNode(); nodeID != "" {
			create(0, nodeID, etcdName(0)+"="+etcdPeerURL(0), true, "")
		}
		return
	case len(members) < want && list != nil && m.etcdAllRunning(members) && !hasLearner(list):
		ord := 0
		for slicesContainsOrd(members, ord) {
			ord++
		}
		// The node first: etcd only hears of the member once it has one.
		nodeID := placeNode()
		if nodeID == "" {
			return
		}
		var resp struct {
			Member  etcdListed   `json:"member"`
			Members []etcdListed `json:"members"`
		}
		if err := m.etcdCall(ctx, "cluster/member/add", map[string]any{"peerURLs": []string{etcdPeerURL(ord)}, "isLearner": true}, &resp); err != nil {
			m.log.Warn("add an etcd member", "member", etcdName(ord), "err", err)
			return
		}
		var parts []string
		for _, x := range resp.Members {
			name := x.Name
			if name == "" && slices.Contains(x.PeerURLs, etcdPeerURL(ord)) {
				name = etcdName(ord) // not started yet: etcd knows no name
			}
			if name == "" {
				continue
			}
			for _, u := range x.PeerURLs {
				parts = append(parts, name+"="+u)
			}
		}
		m.log.Info("adding an etcd member as a learner", "member", etcdName(ord), "node", m.nodeName(nodeID))
		if !create(ord, nodeID, strings.Join(parts, ","), false, hexID(resp.Member.ID)) {
			_ = m.etcdRemove(ctx, resp.Member.ID)
		}
		return
	}

	for _, mb := range members {
		if mb.SpecHash == "" || (mb.State == store.TaskPending && now.Sub(mb.UpdatedAt) > pendingResend) {
			m.etcdSend(ctx, &mb)
		}
	}
	// A changed spec (image, aliases, DNS) rolls out one member at a time,
	// while the rest are running, so the cluster keeps its quorum. The
	// member keeps its data volume.
	for _, mb := range members {
		if mb.SpecHash == "" || mb.SpecHash == specHash(m.etcdSpec(mb)) {
			continue
		}
		others := true
		for _, x := range members {
			// Settled: the last one updated has rejoined before the next goes.
			if (x.ID != mb.ID && x.State != store.TaskRunning) || now.Sub(x.UpdatedAt) < 15*time.Second {
				others = false
			}
		}
		if n, ok := m.nodes.Get(mb.NodeID); others && ok && n.Connected {
			m.log.Info("updating an etcd member", "member", etcdName(mb.Ordinal))
			m.etcdSend(ctx, &mb)
		}
		break
	}
}

// etcdListed is a member as etcd lists it.
type etcdListed struct {
	ID        string   `json:"ID"` // decimal
	Name      string   `json:"name"`
	PeerURLs  []string `json:"peerURLs"`
	IsLearner bool     `json:"isLearner"`
}

func hexID(dec string) string {
	id, err := strconv.ParseUint(dec, 10, 64)
	if err != nil || id == 0 {
		return ""
	}
	return strconv.FormatUint(id, 16)
}

func hasLearner(list []etcdListed) bool {
	return slices.ContainsFunc(list, func(x etcdListed) bool { return x.IsLearner })
}

// etcdList is etcd's member list, or nil while no member answers with a
// quorum behind it.
func (m *Manager) etcdList(ctx context.Context) ([]etcdListed, error) {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	var resp struct {
		Members []etcdListed `json:"members"`
	}
	// Linearizable: a member that lost its quorum does not answer.
	if err := m.etcdCall(ctx, "cluster/member/list", map[string]any{"linearizable": true}, &resp); err != nil {
		return nil, err
	}
	return resp.Members, nil
}

func (m *Manager) etcdRemove(ctx context.Context, decID string) error {
	return m.etcdCall(ctx, "cluster/member/remove", map[string]string{"ID": decID}, nil)
}

// etcdTidy removes what cannot become a working member: members etcd lists
// that SynCloud does not run (left over from an interrupted change), members
// SynCloud runs that etcd no longer lists, and learners that never caught
// up. It reports whether it changed anything.
func (m *Manager) etcdTidy(ctx context.Context, members []store.EtcdMember, list []etcdListed, now time.Time) bool {
	ours := func(x etcdListed) (store.EtcdMember, bool) {
		for _, mb := range members {
			if x.Name == etcdName(mb.Ordinal) || slices.Contains(x.PeerURLs, etcdPeerURL(mb.Ordinal)) {
				return mb, true
			}
		}
		return store.EtcdMember{}, false
	}
	for _, x := range list {
		mb, ok := ours(x)
		switch {
		case !ok:
			m.log.Warn("removing an etcd member SynCloud does not run", "name", x.Name, "peers", strings.Join(x.PeerURLs, ","))
			if err := m.etcdRemove(ctx, x.ID); err != nil {
				m.log.Warn("remove an etcd member", "err", err)
			}
			return true
		case x.IsLearner && now.Sub(mb.CreatedAt) > etcdLearnerTimeout:
			m.log.Warn("an etcd learner did not catch up; trying again", "member", etcdName(mb.Ordinal), "node", m.nodeName(mb.NodeID), "state", mb.State, "error", mb.Error)
			if err := m.etcdRemove(ctx, x.ID); err != nil {
				m.log.Warn("remove an etcd learner", "err", err)
				return true
			}
			m.etcdDrop(ctx, mb)
			return true
		}
	}
	for _, mb := range members {
		listed := slices.ContainsFunc(list, func(x etcdListed) bool {
			return x.Name == etcdName(mb.Ordinal) || slices.Contains(x.PeerURLs, etcdPeerURL(mb.Ordinal))
		})
		if !listed {
			// Removed from the cluster: it can never rejoin with its data.
			m.log.Warn("an etcd member is no longer in the cluster; replacing it", "member", etcdName(mb.Ordinal))
			m.etcdDrop(ctx, mb)
			return true
		}
	}
	return false
}

// etcdPromote gives running learners their vote; etcd refuses until a
// learner has caught up with the leader, and the next tick tries again.
func (m *Manager) etcdPromote(ctx context.Context, members []store.EtcdMember, list []etcdListed) {
	for _, x := range list {
		if !x.IsLearner {
			continue
		}
		for _, mb := range members {
			if x.Name != etcdName(mb.Ordinal) || mb.State != store.TaskRunning {
				continue
			}
			if err := m.etcdCall(ctx, "cluster/member/promote", map[string]string{"ID": x.ID}, nil); err != nil {
				m.log.Debug("promote an etcd learner", "member", x.Name, "err", err)
				continue
			}
			m.log.Info("etcd member promoted to a voter", "member", x.Name)
			m.changed()
		}
	}
}

// etcdDrop forgets a member and removes its container and data, which are
// of no use once etcd no longer lists it: a member started on them again
// would be refused as removed. A node that cannot be told now removes them
// when it reports in (the orphan record).
func (m *Manager) etcdDrop(ctx context.Context, mb store.EtcdMember) {
	vol := etcdVolume(mb.Ordinal)
	if err := m.st.AddDatabaseOrphan(ctx, store.DatabaseOrphan{TaskID: mb.ID, NodeID: mb.NodeID, Volume: vol, CreatedAt: m.now().UTC()}); err != nil {
		m.log.Warn("record etcd member volume", "member", mb.ID, "err", err)
	}
	_ = m.st.DeleteEtcdMember(ctx, mb.ID)
	var volumes []string
	if m.etcdVolumeFree(ctx, mb.NodeID, vol) {
		volumes = []string{vol}
	}
	err := m.gw.Send(mb.NodeID, &agentv1.ConnectResponse{Msg: &agentv1.ConnectResponse_StopTask{StopTask: &agentv1.StopTask{
		TaskId: mb.ID, TimeoutSeconds: 10, Remove: true, RemoveVolumes: volumes}}})
	if err != nil && !errors.Is(err, agentgw.ErrNotConnected) {
		m.log.Warn("stop etcd member", "member", mb.ID, "err", err)
	}
	m.changed()
}

// etcdVolumeFree reports whether no current member uses a volume on a node
// (volumes are named after the member's name, which a replacement reuses).
func (m *Manager) etcdVolumeFree(ctx context.Context, nodeID, vol string) bool {
	ms, err := m.st.EtcdMembers(ctx)
	if err != nil {
		return false
	}
	return !slices.ContainsFunc(ms, func(x store.EtcdMember) bool { return x.NodeID == nodeID && etcdVolume(x.Ordinal) == vol })
}

// etcdNote logs why etcd cannot grow, once per reason.
func (m *Manager) etcdNote(why string) {
	m.mu.Lock()
	same := m.etcdWhy == why
	m.etcdWhy = why
	m.mu.Unlock()
	if !same && why != "" {
		m.log.Warn(why)
	}
}

// etcdWaiting says why PostgreSQL clusters wait for etcd: its members'
// states while none answers.
func (m *Manager) etcdWaiting(ctx context.Context) string {
	ms, _ := m.st.EtcdMembers(ctx)
	if len(ms) == 0 {
		m.mu.Lock()
		why := m.etcdWhy
		m.mu.Unlock()
		if why != "" {
			return why
		}
		return "no member is placed yet"
	}
	var parts []string
	for _, mb := range ms {
		p := fmt.Sprintf("%s on %s %s", etcdName(mb.Ordinal), m.nodeName(mb.NodeID), mb.State)
		if mb.Error != "" {
			p += " (" + mb.Error + ")"
		} else if mb.State == store.TaskRunning && mb.IP == "" {
			p += " without an address"
		}
		parts = append(parts, p)
	}
	return "no member answers yet: " + strings.Join(parts, ", ") + " (see the platform's etcd logs)"
}

// etcdHealthy reports whether etcd answered the last reconcile.
func (m *Manager) etcdHealthy() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.etcdUp
}

func slicesContainsOrd(ms []store.EtcdMember, ord int) bool {
	for _, x := range ms {
		if x.Ordinal == ord {
			return true
		}
	}
	return false
}

func (m *Manager) nodeName(id string) string {
	if n, ok := m.nodes.Get(id); ok {
		return n.Name
	}
	return ""
}

func (m *Manager) readyNodes() int {
	n := 0
	for _, x := range m.nodes.List() {
		if x.Status == store.NodeReady && x.Connected {
			n++
		}
	}
	return n
}

func (m *Manager) etcdAllRunning(ms []store.EtcdMember) bool {
	for _, x := range ms {
		if x.State != store.TaskRunning || x.IP == "" {
			return false
		}
	}
	return len(ms) > 0
}

// etcdSpec is what a member should run now.
func (m *Manager) etcdSpec(mb store.EtcdMember) *agentv1.TaskSpec {
	var dns, search []string
	if m.DNS != nil {
		dns, search = m.DNS(mb.NodeID, "", "")
	}
	return etcdTaskSpec(m.pgImage(pgDefaultVersion), mb, dns, search)
}

func (m *Manager) etcdSend(ctx context.Context, mb *store.EtcdMember) {
	ts := m.etcdSpec(*mb)
	mb.SpecHash = specHash(ts)
	m.resolve(ts)
	_ = m.st.UpdateEtcdMember(ctx, *mb, m.now().UTC())
	err := m.gw.Send(mb.NodeID, &agentv1.ConnectResponse{Msg: &agentv1.ConnectResponse_RunTask{RunTask: &agentv1.RunTask{Spec: ts}}})
	if err != nil && !errors.Is(err, agentgw.ErrNotConnected) {
		m.log.Warn("send etcd member", "member", mb.ID, "err", err)
	}
}

// onEtcdStatus follows an etcd container's state.
func (m *Manager) onEtcdStatus(node store.Node, s *agentv1.TaskStatus) {
	ctx := context.Background()
	mb, err := m.st.EtcdMemberByID(ctx, s.GetTaskId())
	if errors.Is(err, store.ErrNotFound) {
		if s.GetState() != agentv1.TaskState_TASK_STATE_REMOVED {
			// Left over from a replaced member: remove it, with its data when
			// its removal recorded the volume (etcd no longer lists it).
			var volumes []string
			if o, err := m.st.DatabaseOrphanByTask(ctx, s.GetTaskId()); err == nil && m.etcdVolumeFree(ctx, node.ID, o.Volume) {
				volumes = []string{o.Volume}
			}
			_ = m.gw.Send(node.ID, &agentv1.ConnectResponse{Msg: &agentv1.ConnectResponse_StopTask{StopTask: &agentv1.StopTask{
				TaskId: s.GetTaskId(), TimeoutSeconds: 10, Remove: true, RemoveVolumes: volumes}}})
		} else if _, err := m.st.DatabaseOrphanByTask(ctx, s.GetTaskId()); err == nil {
			_ = m.st.DeleteDatabaseOrphan(ctx, s.GetTaskId())
		}
		return
	} else if err != nil {
		return
	}
	state, ip := stateOf(s.GetState()), s.GetIp()
	if state != store.TaskRunning {
		ip = ""
	}
	if mb.State == state && mb.IP == ip && mb.Error == s.GetError() {
		return
	}
	mb.State, mb.IP, mb.Error = state, ip, s.GetError()
	_ = m.st.UpdateEtcdMember(ctx, mb, m.now().UTC())
	m.changed() // DNS names follow addresses
}

// ── etcd API (JSON gateway) ─────────────────────────────────────────────────

var etcdHTTP = &http.Client{Timeout: 5 * time.Second}

// etcdCall posts to /v3/<path> on the first running member that answers,
// authenticating as root once auth is on. A member that lost its quorum
// does not answer writes or linearizable reads, and the next is tried.
func (m *Manager) etcdCall(ctx context.Context, path string, body, out any) error {
	members, err := m.st.EtcdMembers(ctx)
	if err != nil {
		return err
	}
	authOn, _, _ := m.st.GetSetting(ctx, etcdAuthKey)
	last := errors.New("no etcd member is running")
	for _, mb := range members {
		if mb.IP == "" || mb.State != store.TaskRunning {
			continue
		}
		if ctx.Err() != nil {
			return last
		}
		base := fmt.Sprintf("http://%s/v3/", addr(mb.IP, etcdClientPort))
		// A simple token is only valid on the member that issued it.
		token := ""
		if authOn == "1" {
			pw, err := m.etcdRootPassword(ctx)
			if err != nil {
				return err
			}
			var a struct {
				Token string `json:"token"`
			}
			if last = etcdPost(ctx, base+"auth/authenticate", "", map[string]string{"name": "root", "password": pw}, &a); last != nil {
				continue
			}
			token = a.Token
		}
		if last = etcdPost(ctx, base+path, token, body, out); last == nil {
			return nil
		}
	}
	return last
}

func etcdPost(ctx context.Context, url, token string, body, out any) error {
	b, _ := json.Marshal(body)
	req, _ := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(b))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", token)
	}
	res, err := etcdHTTP.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if res.StatusCode != http.StatusOK {
		var e struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(raw, &e)
		if e.Message == "" {
			e.Message = strings.TrimSpace(string(raw))
		}
		return fmt.Errorf("etcd %s: %s", url[strings.Index(url, "/v3/")+4:], e.Message)
	}
	if out != nil {
		return json.Unmarshal(raw, out)
	}
	return nil
}

func (m *Manager) etcdRootPassword(ctx context.Context) (string, error) {
	v, ok, err := m.st.GetSetting(ctx, etcdRootKey)
	if err != nil {
		return "", err
	}
	if !ok {
		pw := randomPassword()
		if err := m.st.SetSetting(ctx, etcdRootKey, base64.StdEncoding.EncodeToString(m.box.Seal([]byte(pw), []byte(etcdRootKey)))); err != nil {
			return "", err
		}
		return pw, nil
	}
	raw, err := base64.StdEncoding.DecodeString(v)
	if err != nil {
		return "", err
	}
	pw, err := m.box.Open(raw, []byte(etcdRootKey))
	return string(pw), err
}

// etcdEnableAuth creates root and turns authentication on (once).
func (m *Manager) etcdEnableAuth(ctx context.Context) error {
	if v, _, _ := m.st.GetSetting(ctx, etcdAuthKey); v == "1" {
		return nil
	}
	pw, err := m.etcdRootPassword(ctx)
	if err != nil {
		return err
	}
	if err := m.etcdCall(ctx, "auth/user/add", map[string]string{"name": "root", "password": pw}, nil); err != nil && !strings.Contains(err.Error(), "already exists") {
		return err
	}
	if err := m.etcdCall(ctx, "auth/user/grant", map[string]string{"user": "root", "role": "root"}, nil); err != nil {
		return err
	}
	if err := m.etcdCall(ctx, "auth/enable", map[string]string{}, nil); err != nil {
		return err
	}
	m.log.Info("etcd authentication enabled")
	return m.st.SetSetting(ctx, etcdAuthKey, "1")
}

// ensureEtcdUser gives a PostgreSQL cluster its own etcd user, allowed only
// under its Patroni prefix.
func (m *Manager) ensureEtcdUser(ctx context.Context, d store.Database, sec Secrets) error {
	if v, _, _ := m.st.GetSetting(ctx, etcdAuthKey); v != "1" {
		return errors.New("turning on authentication")
	}
	user := etcdUser(d)
	prefix := pgNamespace + d.ID + "/"
	end := prefix[:len(prefix)-1] + string(prefix[len(prefix)-1]+1)
	b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	steps := []struct {
		path string
		body any
	}{
		{"auth/role/add", map[string]string{"name": user}},
		{"auth/role/grant", map[string]any{"name": user, "perm": map[string]string{"permType": "READWRITE", "key": b64(prefix), "range_end": b64(end)}}},
		{"auth/user/add", map[string]string{"name": user, "password": sec.EtcdPassword}},
		{"auth/user/grant", map[string]string{"user": user, "role": user}},
	}
	for _, s := range steps {
		if err := m.etcdCall(ctx, s.path, s.body, nil); err != nil && !strings.Contains(err.Error(), "already exists") {
			return err
		}
	}
	return nil
}

// dropEtcdUser removes a deleted cluster's user, role and keys.
func (m *Manager) dropEtcdUser(ctx context.Context, d store.Database) {
	user := etcdUser(d)
	prefix := pgNamespace + d.ID + "/"
	end := prefix[:len(prefix)-1] + string(prefix[len(prefix)-1]+1)
	b64 := func(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }
	_ = m.etcdCall(ctx, "kv/deleterange", map[string]string{"key": b64(prefix), "range_end": b64(end)}, nil)
	_ = m.etcdCall(ctx, "auth/user/delete", map[string]string{"name": user}, nil)
	_ = m.etcdCall(ctx, "auth/role/delete", map[string]string{"role": user}, nil)
}

// etcdLearnIDs records etcd's member IDs (needed to remove members).
func (m *Manager) etcdLearnIDs(ctx context.Context, members []store.EtcdMember, list []etcdListed) {
	for _, mb := range members {
		if mb.MemberID != "" {
			continue
		}
		for _, x := range list {
			if x.Name == etcdName(mb.Ordinal) {
				mb.MemberID = hexID(x.ID)
				_ = m.st.UpdateEtcdMember(ctx, mb, m.now().UTC())
			}
		}
	}
}

// EtcdView is the platform etcd as the API shows it.
type EtcdView struct {
	Name  string `json:"name"`
	Node  string `json:"node"`
	State string `json:"state"`
	IP    string `json:"ip"`
	Error string `json:"error,omitempty"`
}

// Etcd lists the platform etcd members.
func (m *Manager) Etcd(ctx context.Context) []EtcdView {
	ms, _ := m.st.EtcdMembers(ctx)
	out := []EtcdView{}
	for _, mb := range ms {
		out = append(out, EtcdView{Name: etcdName(mb.Ordinal), Node: m.nodeName(mb.NodeID), State: mb.State, IP: mb.IP, Error: mb.Error})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
