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
	"sort"
	"strconv"
	"strings"
	"time"

	"syncloud/internal/agentgw"
	"syncloud/internal/auth"
	agentv1 "syncloud/internal/gen/syncloud/agent/v1"
	"syncloud/internal/store"
	"syncloud/internal/workload"
)

// The platform etcd (Phase 13) is Patroni's coordination store for every
// PostgreSQL cluster. It runs on the nodes, so leader elections and
// failovers keep working while the controller is down. Three members on
// distinct nodes (one while the cluster has fewer nodes); a member whose
// node is lost for good is removed and replaced, one change at a time.

const (
	EtcdPrefix = "dbe_"
	// EtcdServiceID labels etcd containers for security groups.
	EtcdServiceID  = "etcd"
	etcdZone       = "etcd." + Zone
	etcdClientPort = 2379
	etcdPeerPort   = 2380
	etcdSize       = 3
	etcdLostAfter  = 10 * time.Minute
	etcdRootKey    = "etcd.root_password" // sealed, in settings
	etcdAuthKey    = "etcd.auth_enabled"
)

func etcdName(ord int) string { return fmt.Sprintf("e%d", ord) }

// EtcdHost is a member's DNS name.
func EtcdHost(ord int) string { return etcdName(ord) + "." + etcdZone }

func etcdPeerURL(ord int) string { return fmt.Sprintf("http://%s:%d", EtcdHost(ord), etcdPeerPort) }

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
		Mounts:  []*agentv1.Mount{{Type: agentv1.Mount_TYPE_VOLUME, Source: "syncloud-etcd-" + name, Target: "/data"}},
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

// EtcdHosts are the client addresses Patroni uses.
func (m *Manager) EtcdHosts(ctx context.Context) []string {
	ms, _ := m.st.EtcdMembers(ctx)
	var out []string
	for _, mb := range ms {
		out = append(out, fmt.Sprintf("%s:%d", EtcdHost(mb.Ordinal), etcdClientPort))
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

// reconcileEtcd keeps the platform etcd at its size.
func (m *Manager) reconcileEtcd(ctx context.Context) {
	members, err := m.st.EtcdMembers(ctx)
	if err != nil {
		return
	}
	if len(members) == 0 && !m.needsEtcd(ctx) {
		return
	}
	now := m.now().UTC()
	m.etcdLearnIDs(ctx, members)

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
		_ = m.st.DeleteEtcdMember(ctx, mb.ID)
		return // one change at a time
	}

	want := 1
	if m.readyNodes() >= etcdSize {
		want = etcdSize
	}
	want = max(want, len(members)) // never shrink
	placeOne := func(ord int, used []string, cluster string, isNew bool) (store.EtcdMember, bool) {
		ws := workload.Spec{Resources: workload.Resources{CPU: 0.05, Memory: 128}, Placement: workload.Placement{Strategy: "spread"}}.Avoiding(used)
		nodeID, why := m.wl.PlaceSpec(ctx, ws)
		if nodeID == "" {
			m.log.Warn("cannot place an etcd member", "why", why)
			return store.EtcdMember{}, false
		}
		mb := store.EtcdMember{ID: auth.NewID(EtcdPrefix), Ordinal: ord, NodeID: nodeID, State: store.TaskPending,
			Cluster: cluster, NewCluster: isNew, CreatedAt: now.Truncate(time.Second)}
		if err := m.st.CreateEtcdMember(ctx, mb); err != nil {
			m.log.Error("create etcd member", "err", err)
			return store.EtcdMember{}, false
		}
		return mb, true
	}

	switch {
	case len(members) == 0:
		// Bootstrap: every member at once, with the full initial cluster.
		var parts []string
		for i := 0; i < want; i++ {
			parts = append(parts, etcdName(i)+"="+etcdPeerURL(i))
		}
		var used []string
		for i := 0; i < want; i++ {
			mb, ok := placeOne(i, used, strings.Join(parts, ","), true)
			if !ok {
				break
			}
			used = append(used, m.nodeName(mb.NodeID))
			m.etcdSend(ctx, &mb)
		}
		m.changed()
		return
	case len(members) < want && m.etcdAllRunning(members):
		// Grow by one: announce the member, then start it with the
		// cluster as etcd now sees it.
		ord := 0
		for slicesContainsOrd(members, ord) {
			ord++
		}
		var resp struct {
			Members []struct {
				Name     string   `json:"name"`
				PeerURLs []string `json:"peerURLs"`
			} `json:"members"`
		}
		if err := m.etcdCall(ctx, "cluster/member/add", map[string]any{"peerURLs": []string{etcdPeerURL(ord)}}, &resp); err != nil {
			m.log.Warn("add an etcd member", "err", err)
			return
		}
		var parts []string
		for _, x := range resp.Members {
			name := x.Name
			if name == "" {
				name = etcdName(ord)
			}
			for _, u := range x.PeerURLs {
				parts = append(parts, name+"="+u)
			}
		}
		var used []string
		for _, x := range members {
			used = append(used, m.nodeName(x.NodeID))
		}
		if mb, ok := placeOne(ord, used, strings.Join(parts, ","), false); ok {
			m.etcdSend(ctx, &mb)
			m.changed()
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
	if m.etcdAllRunning(members) {
		if err := m.etcdEnableAuth(ctx); err != nil {
			m.log.Warn("etcd auth", "err", err)
		}
	}
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
			// Left over from a replaced member: remove it (its volume stays).
			_ = m.gw.Send(node.ID, &agentv1.ConnectResponse{Msg: &agentv1.ConnectResponse_StopTask{StopTask: &agentv1.StopTask{
				TaskId: s.GetTaskId(), TimeoutSeconds: 10, Remove: true}}})
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
// authenticating as root once auth is on.
func (m *Manager) etcdCall(ctx context.Context, path string, body, out any) error {
	members, err := m.st.EtcdMembers(ctx)
	if err != nil {
		return err
	}
	token := ""
	authOn, _, _ := m.st.GetSetting(ctx, etcdAuthKey)
	last := errors.New("no etcd member is running")
	for _, mb := range members {
		if mb.IP == "" || mb.State != store.TaskRunning {
			continue
		}
		base := fmt.Sprintf("http://%s/v3/", addr(mb.IP, etcdClientPort))
		if authOn == "1" && token == "" {
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
		return errors.New("the platform etcd is starting")
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
func (m *Manager) etcdLearnIDs(ctx context.Context, members []store.EtcdMember) {
	missing := false
	for _, mb := range members {
		if mb.MemberID == "" && mb.State == store.TaskRunning {
			missing = true
		}
	}
	if !missing {
		return
	}
	var resp struct {
		Members []struct {
			ID   string `json:"ID"`
			Name string `json:"name"`
		} `json:"members"`
	}
	if err := m.etcdCall(ctx, "cluster/member/list", map[string]any{}, &resp); err != nil {
		return
	}
	for _, mb := range members {
		for _, x := range resp.Members {
			if x.Name == etcdName(mb.Ordinal) && mb.MemberID == "" {
				id, _ := strconv.ParseUint(x.ID, 10, 64)
				mb.MemberID = strconv.FormatUint(id, 16)
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
