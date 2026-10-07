package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// dbView is the part of a database the CLI prints.
type dbView struct {
	Name        string `json:"name"`
	Project     string `json:"project"`
	Environment string `json:"environment"`
	Engine      string `json:"engine"`
	Version     string `json:"version"`
	Health      string `json:"health"`
	Status      string `json:"status"`
	Host        string `json:"host"`
	ReadHost    string `json:"readHost"`
	Port        int    `json:"port"`
	Network     struct {
		Access []string `json:"access"`
		Public struct {
			Enabled bool     `json:"enabled"`
			Allow   []string `json:"allow"`
		} `json:"public"`
	} `json:"network"`
	Public struct {
		Enabled   bool   `json:"enabled"`
		Available bool   `json:"available"`
		Reason    string `json:"reason"`
		Host      string `json:"host"`
		ReadHost  string `json:"readHost"`
		Port      int    `json:"port"`
	} `json:"public"`
	Spec struct {
		Memory         struct{ Min, Max int } `json:"memory"`
		Replicas       struct{ Min, Max int } `json:"replicas"`
		Persistence    string                 `json:"persistence"`
		EvictionPolicy string                 `json:"evictionPolicy"`
		Nodes          []string               `json:"nodes"`
	} `json:"spec"`
	State struct {
		MemoryMiB int `json:"memoryMiB"`
		Replicas  int `json:"replicas"`
		Primary   int `json:"primary"`
	} `json:"state"`
	Members []struct {
		Name     string  `json:"name"`
		Node     string  `json:"node"`
		State    string  `json:"state"`
		Role     string  `json:"role"`
		LagBytes int64   `json:"lagBytes"`
		Memory   int64   `json:"usedMemoryBytes"`
		Ops      float64 `json:"opsPerSec"`
	} `json:"members"`
	Usage struct {
		UsedMemoryBytes int64   `json:"usedMemoryBytes"`
		Keys            int64   `json:"keys"`
		OpsPerSec       float64 `json:"opsPerSec"`
		Clients         int64   `json:"clients"`
		HitRate         float64 `json:"hitRate"`
	} `json:"usage"`
}

// dbList is a project environment's databases.
func (s scope) dbList() string {
	return "/api/v1/projects/" + url.PathEscape(s.project) + "/environments/" + url.PathEscape(s.env) + "/databases"
}

// dbItem is one database (names are unique in the cluster).
func dbItem(name string) string { return "/api/v1/databases/" + url.PathEscape(name) }

// owner is where a database lives.
func (d dbView) owner() string {
	if d.Project == "" {
		return "standalone"
	}
	return d.Project + "/" + d.Environment
}

// dbSpecFlags are the flags shared by create and update.
type dbSpecFlags struct {
	memory, maxMemory, replicas, maxReplicas int
	cpu, cpuTarget                           float64
	persistence, eviction                    string
	nodes                                    []string
}

func (f *dbSpecFlags) add(cmd *cobra.Command) {
	cmd.Flags().IntVar(&f.memory, "memory", 0, "memory in MiB (the minimum when autoscaling; default 256)")
	cmd.Flags().IntVar(&f.maxMemory, "max-memory", 0, "autoscale memory up to this many MiB (default: --memory)")
	cmd.Flags().IntVar(&f.replicas, "replicas", 0, "read replicas (the minimum when autoscaling)")
	cmd.Flags().IntVar(&f.maxReplicas, "max-replicas", -1, "autoscale read replicas up to this many (default: --replicas)")
	cmd.Flags().Float64Var(&f.cpu, "cpu", 0, "CPU reserved per member, in cores (default 0.1)")
	cmd.Flags().Float64Var(&f.cpuTarget, "cpu-target", 0, "replica autoscaling target: read CPU % of one core (default 60)")
	cmd.Flags().StringVar(&f.persistence, "persistence", "", "aof (default), rdb or none")
	cmd.Flags().StringVar(&f.eviction, "eviction", "", "maxmemory-policy, e.g. allkeys-lru (default noeviction)")
	cmd.Flags().StringSliceVar(&f.nodes, "nodes", nil, "run only on these nodes")
}

// apply sets the changed flags on a spec (a JSON object).
func (f *dbSpecFlags) apply(cmd *cobra.Command, spec map[string]any) {
	sub := func(k string) map[string]any {
		m, _ := spec[k].(map[string]any)
		if m == nil {
			m = map[string]any{}
			spec[k] = m
		}
		return m
	}
	ch := cmd.Flags().Changed
	if ch("memory") {
		sub("memory")["min"] = f.memory
		if !ch("max-memory") {
			if mx, _ := sub("memory")["max"].(float64); int(mx) < f.memory {
				sub("memory")["max"] = f.memory
			}
		}
	}
	if ch("max-memory") {
		sub("memory")["max"] = f.maxMemory
	}
	if ch("replicas") {
		sub("replicas")["min"] = f.replicas
		if !ch("max-replicas") {
			if mx, _ := sub("replicas")["max"].(float64); int(mx) < f.replicas {
				sub("replicas")["max"] = f.replicas
			}
		}
	}
	if ch("max-replicas") {
		sub("replicas")["max"] = f.maxReplicas
	}
	if ch("cpu") {
		spec["cpu"] = f.cpu
	}
	if ch("cpu-target") {
		sub("autoscaling")["cpuTarget"] = f.cpuTarget
	}
	if ch("persistence") {
		spec["persistence"] = f.persistence
	}
	if ch("eviction") {
		spec["evictionPolicy"] = f.eviction
	}
	if ch("nodes") {
		spec["nodes"] = f.nodes
	}
}

func (a *app) databasesCmd() *cobra.Command {
	var s scope
	db := &cobra.Command{Use: "db", Aliases: []string{"databases", "database", "valkey"}, Short: "Managed databases (Valkey, Redis-compatible), standalone or in a project"}
	a.scopeFlags(db, &s)

	list := &cobra.Command{
		Use: "list", Aliases: []string{"ls"}, Short: "List databases (all of them, or one project environment's with -p)", Args: cobra.NoArgs,
		Annotations: op("listDatabases", "listAllDatabases"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			path := "/api/v1/databases"
			if cmd.Flags().Changed("project") {
				path = s.dbList()
			}
			var out struct {
				Items []dbView `json:"items"`
			}
			if err := a.do(cmd, "GET", path, nil, &out); err != nil {
				return err
			}
			rows := make([][]string, 0, len(out.Items))
			for _, d := range out.Items {
				public := "-"
				if d.Public.Enabled {
					public = d.Public.Host
				}
				rows = append(rows, []string{d.Name, d.owner(), d.Engine + " " + d.Version, d.Health,
					fmt.Sprintf("%d MiB (%d–%d)", d.State.MemoryMiB, d.Spec.Memory.Min, d.Spec.Memory.Max),
					fmt.Sprintf("%d (%d–%d)", d.State.Replicas, d.Spec.Replicas.Min, d.Spec.Replicas.Max),
					fmt.Sprint(d.Usage.Keys), fmt.Sprintf("%.0f", d.Usage.OpsPerSec), public})
			}
			return a.printer().table(out.Items, []string{"DATABASE", "OWNER", "ENGINE", "HEALTH", "MEMORY", "REPLICAS", "KEYS", "OPS/S", "PUBLIC"}, rows)
		},
	}

	var cf dbSpecFlags
	var version, engine string
	var standalone, public bool
	var allow, access []string
	create := &cobra.Command{
		Use: "create NAME", Short: "Create a database: standalone, or in a project environment with -p", Args: cobra.ExactArgs(1),
		Example: `  synctl db create sessions --memory 256 --max-memory 2048 --access project:shop --public
  synctl db create cache -p shop --replicas 1 --max-replicas 3 --eviction allkeys-lru`,
		Annotations: op("createDatabase"),
		RunE: func(cmd *cobra.Command, args []string) error {
			spec := map[string]any{}
			cf.apply(cmd, spec)
			body := map[string]any{"name": args[0], "engine": engine, "version": version, "spec": spec}
			if s.project != "" && !standalone {
				body["project"], body["environment"] = s.project, s.env
			}
			network := map[string]any{"public": map[string]any{"enabled": public, "allow": allow}}
			if cmd.Flags().Changed("access") {
				network["access"] = access
			}
			body["network"] = network
			var v dbView
			if err := a.do(cmd, "POST", "/api/v1/databases", body, &v); err != nil {
				return err
			}
			if a.output == "json" {
				return a.printer().json(v)
			}
			fmt.Fprintf(a.out, "Created database %s (%s): %s:%d, read-only %s\n", v.Name, v.owner(), v.Host, v.Port, v.ReadHost)
			if v.Public.Enabled && v.Public.Available {
				fmt.Fprintf(a.out, "Public endpoint (TLS): %s:%d, read-only %s\n", v.Public.Host, v.Public.Port, v.Public.ReadHost)
			} else if v.Public.Enabled {
				fmt.Fprintf(a.out, "Public endpoint unavailable: %s\n", v.Public.Reason)
			}
			fmt.Fprintf(a.out, "Get the password with: synctl db credentials %s\n", v.Name)
			return nil
		},
	}
	cf.add(create)
	create.Flags().StringVar(&engine, "engine", "valkey", "database engine (synctl db engines)")
	create.Flags().StringVar(&version, "version", "", "engine version (default: the newest)")
	create.Flags().BoolVar(&standalone, "standalone", false, "create outside any project even when $SYNCLOUD_PROJECT is set")
	create.Flags().BoolVar(&public, "public", false, "serve a TLS endpoint <name>.db.<base-domain> outside the cluster")
	create.Flags().StringSliceVar(&allow, "allow", nil, "client addresses allowed on the public endpoint (IP or CIDR; default anywhere)")
	create.Flags().StringSliceVar(&access, "access", nil, "internal peers allowed in: project:P, environment:P/E, service:P/E/S, a CIDR or cluster (default: a project database's own environment)")

	var netPublic string
	var netAllow, netAccess, addAccess, removeAccess []string
	network := &cobra.Command{
		Use: "network NAME", Short: "Show or change who may connect: the internal access list and the public endpoint", Args: cobra.ExactArgs(1),
		Example: `  synctl db network sessions --public on --allow 203.0.113.0/24
  synctl db network sessions --add-access environment:billing/production
  synctl db network sessions --public off`,
		Annotations: op("setDatabaseNetwork"),
		RunE: func(cmd *cobra.Command, args []string) error {
			var v dbView
			if err := a.do(cmd, "GET", dbItem(args[0]), nil, &v); err != nil {
				return err
			}
			ch := cmd.Flags().Changed
			if ch("public") || ch("allow") || ch("access") || ch("add-access") || ch("remove-access") {
				n := v.Network
				switch netPublic {
				case "":
				case "on", "true", "yes":
					n.Public.Enabled = true
				case "off", "false", "no":
					n.Public.Enabled = false
				default:
					return errors.New("--public is on or off")
				}
				if ch("allow") {
					n.Public.Allow = netAllow
				}
				if ch("access") {
					n.Access = netAccess
				}
				n.Access = append(n.Access, addAccess...)
				for _, r := range removeAccess {
					kept := n.Access[:0]
					for _, x := range n.Access {
						if x != r {
							kept = append(kept, x)
						}
					}
					n.Access = kept
				}
				if n.Access == nil {
					n.Access = []string{}
				}
				if err := a.do(cmd, "PUT", dbItem(args[0])+"/network", n, &v); err != nil {
					return err
				}
			}
			if a.output == "json" {
				return a.printer().json(map[string]any{"network": v.Network, "public": v.Public})
			}
			fmt.Fprintf(a.out, "internal:   %s:%d, read-only %s\n", v.Host, v.Port, v.ReadHost)
			fmt.Fprintf(a.out, "access:     %s\n", orDash(strings.Join(v.Network.Access, ", ")))
			switch {
			case !v.Public.Enabled:
				fmt.Fprintln(a.out, "public:     off")
			case !v.Public.Available:
				fmt.Fprintf(a.out, "public:     on, unavailable: %s\n", v.Public.Reason)
			default:
				fmt.Fprintf(a.out, "public:     %s:%d (TLS), read-only %s\n", v.Public.Host, v.Public.Port, v.Public.ReadHost)
				fmt.Fprintf(a.out, "allowed:    %s\n", strings.Join(v.Network.Public.Allow, ", "))
			}
			return nil
		},
	}
	network.Flags().StringVar(&netPublic, "public", "", "on or off")
	network.Flags().StringSliceVar(&netAllow, "allow", nil, "replace the public allow-list (IP or CIDR)")
	network.Flags().StringSliceVar(&netAccess, "access", nil, "replace the internal access list")
	network.Flags().StringSliceVar(&addAccess, "add-access", nil, "add internal peers")
	network.Flags().StringSliceVar(&removeAccess, "remove-access", nil, "remove internal peers")

	engines := &cobra.Command{
		Use: "engines", Short: "Database engines, their versions and features", Args: cobra.NoArgs,
		Annotations: op("listDatabaseEngines"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			var out struct {
				Items []struct {
					Name, Title, Description string
					Available                bool
					Versions                 []string
					Port                     int
				} `json:"items"`
			}
			if err := a.do(cmd, "GET", "/api/v1/databases/engines", nil, &out); err != nil {
				return err
			}
			rows := [][]string{}
			for _, e := range out.Items {
				status := "available"
				if !e.Available {
					status = "planned"
				}
				rows = append(rows, []string{e.Name, e.Title, status, orDash(strings.Join(e.Versions, ", ")), fmt.Sprint(e.Port), e.Description})
			}
			return a.printer().table(out.Items, []string{"ENGINE", "TITLE", "STATUS", "VERSIONS", "PORT", "DESCRIPTION"}, rows)
		},
	}

	get := &cobra.Command{
		Use: "get NAME", Short: "A database: endpoints, members and usage", Args: cobra.ExactArgs(1),
		Annotations: op("getDatabase"),
		RunE: func(cmd *cobra.Command, args []string) error {
			var v dbView
			if err := a.do(cmd, "GET", dbItem(args[0]), nil, &v); err != nil {
				return err
			}
			if a.output == "json" {
				return a.printer().json(v)
			}
			fmt.Fprintf(a.out, "%s  (%s)  %s  %s %s\n", v.Name, v.owner(), v.Health, v.Engine, v.Version)
			if v.Status != "" {
				fmt.Fprintf(a.out, "status:     %s\n", v.Status)
			}
			fmt.Fprintf(a.out, "read-write: %s:%d\nread-only:  %s:%d\n", v.Host, v.Port, v.ReadHost, v.Port)
			if v.Public.Enabled && v.Public.Available {
				fmt.Fprintf(a.out, "public:     %s:%d (TLS), read-only %s\n", v.Public.Host, v.Public.Port, v.Public.ReadHost)
			}
			fmt.Fprintf(a.out, "access:     %s\n", orDash(strings.Join(v.Network.Access, ", ")))
			fmt.Fprintf(a.out, "memory:     %d MiB now (%d–%d), %s used\nreplicas:   %d now (%d–%d)\n", v.State.MemoryMiB, v.Spec.Memory.Min, v.Spec.Memory.Max,
				bytesIEC(uint64(v.Usage.UsedMemoryBytes)), v.State.Replicas, v.Spec.Replicas.Min, v.Spec.Replicas.Max)
			fmt.Fprintf(a.out, "usage:      %d keys, %.0f ops/s, %d clients\n\n", v.Usage.Keys, v.Usage.OpsPerSec, v.Usage.Clients)
			rows := [][]string{}
			for _, m := range v.Members {
				lag := "-"
				if m.Role == "replica" {
					lag = bytesIEC(uint64(m.LagBytes))
				}
				rows = append(rows, []string{m.Name, orDash(m.Role), m.Node, m.State, bytesIEC(uint64(m.Memory)), fmt.Sprintf("%.0f", m.Ops), lag})
			}
			return a.printer().table(v.Members, []string{"MEMBER", "ROLE", "NODE", "STATE", "MEMORY", "OPS/S", "LAG"}, rows)
		},
	}

	var uf dbSpecFlags
	update := &cobra.Command{
		Use: "update NAME", Aliases: []string{"set"}, Short: "Change memory, replicas, persistence, eviction or nodes", Args: cobra.ExactArgs(1),
		Example:     `  synctl db update cache --max-memory 4096 --max-replicas 2`,
		Annotations: op("updateDatabase"),
		RunE: func(cmd *cobra.Command, args []string) error {
			var cur struct {
				Spec map[string]any `json:"spec"`
			}
			if err := a.do(cmd, "GET", dbItem(args[0]), nil, &cur); err != nil {
				return err
			}
			uf.apply(cmd, cur.Spec)
			var v dbView
			if err := a.do(cmd, "PUT", dbItem(args[0]), map[string]any{"spec": cur.Spec}, &v); err != nil {
				return err
			}
			fmt.Fprintf(a.out, "Updated %s: memory %d–%d MiB, replicas %d–%d\n", v.Name, v.Spec.Memory.Min, v.Spec.Memory.Max, v.Spec.Replicas.Min, v.Spec.Replicas.Max)
			return nil
		},
	}
	uf.add(update)

	simple := func(use, short, method, suffix, opID, done string) *cobra.Command {
		return &cobra.Command{
			Use: use + " NAME", Short: short, Args: cobra.ExactArgs(1), Annotations: op(opID),
			RunE: func(cmd *cobra.Command, args []string) error {
				if err := a.do(cmd, method, dbItem(args[0])+suffix, nil, nil); err != nil {
					return err
				}
				fmt.Fprintf(a.out, done+"\n", args[0])
				return nil
			},
		}
	}

	creds := &cobra.Command{
		Use: "credentials NAME", Aliases: []string{"creds"}, Short: "Connection details with the password (audited)", Args: cobra.ExactArgs(1),
		Annotations: op("getDatabaseCredentials"),
		RunE: func(cmd *cobra.Command, args []string) error {
			var c map[string]any
			if err := a.do(cmd, "GET", dbItem(args[0])+"/credentials", nil, &c); err != nil {
				return err
			}
			if a.output == "json" {
				return a.printer().json(c)
			}
			for _, k := range []string{"url", "readUrl", "haUrl", "publicUrl", "publicReadUrl", "host", "readHost", "port", "database", "username", "password"} {
				if v, ok := c[k]; ok {
					fmt.Fprintf(a.out, "%-14s %v\n", k+":", v)
				}
			}
			return nil
		},
	}

	var rng string
	metricsCmd := &cobra.Command{
		Use: "metrics NAME", Short: "History: latest, average and peak of operations, memory, clients, keys, hit rate, lag…", Args: cobra.ExactArgs(1),
		Annotations: op("getDatabaseMetrics"),
		RunE: func(cmd *cobra.Command, args []string) error {
			var m struct {
				Charts map[string][]struct {
					Key    string       `json:"key"`
					Points [][2]float64 `json:"points"`
				} `json:"charts"`
			}
			if err := a.do(cmd, "GET", dbItem(args[0])+"/metrics?range="+url.QueryEscape(rng), nil, &m); err != nil {
				return err
			}
			var rows [][]string
			for _, chart := range []string{"ops", "memory", "maxmemory", "clients", "keys", "hitRate", "evictions", "lag", "cpu", "network"} {
				for _, sr := range m.Charts[chart] {
					last, sum, peak, n := 0.0, 0.0, 0.0, 0
					for _, p := range sr.Points {
						last, sum, peak, n = p[1], sum+p[1], max(peak, p[1]), n+1
					}
					if n > 0 {
						rows = append(rows, []string{chart, sr.Key, strconv.FormatFloat(last, 'f', 1, 64), strconv.FormatFloat(sum/float64(n), 'f', 1, 64), strconv.FormatFloat(peak, 'f', 1, 64)})
					}
				}
			}
			return a.printer().table(m, []string{"CHART", "SERIES", "LATEST", "AVERAGE", "PEAK"}, rows)
		},
	}
	metricsCmd.Flags().StringVar(&rng, "range", "1h", "time range: 15m, 1h, 6h, 24h or 7d")

	events := &cobra.Command{
		Use: "events NAME", Short: "Scaling, failover and member events", Args: cobra.ExactArgs(1),
		Annotations: op("listDatabaseEvents"),
		RunE: func(cmd *cobra.Command, args []string) error {
			var out struct {
				Items []struct {
					At                            time.Time `json:"at"`
					Kind, From, To, Reason, Actor string
				} `json:"items"`
			}
			if err := a.do(cmd, "GET", dbItem(args[0])+"/events", nil, &out); err != nil {
				return err
			}
			rows := [][]string{}
			for _, e := range out.Items {
				rows = append(rows, []string{e.At.Local().Format("2006-01-02 15:04:05"), e.Kind, orDash(e.From), orDash(e.To), orDash(e.Reason), e.Actor})
			}
			return a.printer().table(out.Items, []string{"AT", "KIND", "FROM", "TO", "REASON", "BY"}, rows)
		},
	}

	var pattern, typ string
	var count int
	keys := &cobra.Command{
		Use: "keys NAME [PATTERN]", Short: "List keys matching a pattern (SCAN), with type and TTL", Args: cobra.RangeArgs(1, 2),
		Annotations: op("scanDatabaseKeys"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 2 {
				pattern = args[1]
			}
			var page struct {
				Cursor uint64 `json:"cursor"`
				Total  int64  `json:"total"`
				Keys   []struct {
					Key   string `json:"key"`
					Type  string `json:"type"`
					TTL   int64  `json:"ttlMs"`
					Bytes int64  `json:"bytes"`
				} `json:"keys"`
			}
			q := url.Values{"pattern": {pattern}, "type": {typ}, "count": {strconv.Itoa(count)}}
			if err := a.do(cmd, "GET", dbItem(args[0])+"/keys?"+q.Encode(), nil, &page); err != nil {
				return err
			}
			rows := [][]string{}
			for _, k := range page.Keys {
				ttl := "-"
				if k.TTL >= 0 {
					ttl = (time.Duration(k.TTL) * time.Millisecond).Round(time.Second).String()
				}
				rows = append(rows, []string{k.Key, k.Type, ttl, bytesIEC(uint64(k.Bytes))})
			}
			if err := a.printer().table(page, []string{"KEY", "TYPE", "TTL", "SIZE"}, rows); err != nil {
				return err
			}
			if a.output != "json" && page.Cursor != 0 {
				fmt.Fprintf(a.errOut, "(more keys: raise --count; %d keys in the database)\n", page.Total)
			}
			return nil
		},
	}
	keys.Flags().StringVar(&typ, "type", "", "only keys of this type (string, hash, list, set, zset, stream)")
	keys.Flags().IntVar(&count, "count", 100, "keys to return (up to 1000)")

	keyCmd := &cobra.Command{Use: "key", Short: "Read, write, expire or delete one key"}
	keyGet := &cobra.Command{
		Use: "get NAME KEY", Short: "A key's value", Args: cobra.ExactArgs(2), Annotations: op("getDatabaseKey"),
		RunE: func(cmd *cobra.Command, args []string) error {
			var v map[string]any
			if err := a.do(cmd, "GET", dbItem(args[0])+"/key?key="+url.QueryEscape(args[1]), nil, &v); err != nil {
				return err
			}
			if str, ok := v["string"].(string); ok && a.output != "json" {
				fmt.Fprintln(a.out, str)
				return nil
			}
			return a.printer().json(v)
		},
	}
	var ttl int64
	var kind string
	keySet := &cobra.Command{
		Use: "set NAME KEY VALUE...", Short: "Write a key: a string, or hash fields (f=v), list items, set or zset (score=member) members",
		Example: `  synctl db key set cache greeting hello --ttl 3600
  synctl db key set cache user:1 --type hash name=Ada role=admin`,
		Args: cobra.MinimumNArgs(3), Annotations: op("setDatabaseKey"),
		RunE: func(cmd *cobra.Command, args []string) error {
			vals := args[2:]
			w := map[string]any{"type": kind, "ttlSeconds": ttl, "replace": true}
			switch kind {
			case "string":
				w["string"] = strings.Join(vals, " ")
			case "hash":
				h := map[string]string{}
				for _, kv := range vals {
					k, v, ok := strings.Cut(kv, "=")
					if !ok {
						return fmt.Errorf("hash fields are field=value, got %q", kv)
					}
					h[k] = v
				}
				w["hash"] = h
			case "list", "set":
				w[kind] = vals
			case "zset":
				var zs []map[string]any
				for _, sm := range vals {
					sc, m, ok := strings.Cut(sm, "=")
					f, err := strconv.ParseFloat(sc, 64)
					if !ok || err != nil {
						return fmt.Errorf("zset members are score=member, got %q", sm)
					}
					zs = append(zs, map[string]any{"score": f, "member": m})
				}
				w["zset"] = zs
			default:
				return errors.New("--type must be string, hash, list, set or zset")
			}
			if err := a.do(cmd, "PUT", dbItem(args[0])+"/key?key="+url.QueryEscape(args[1]), w, nil); err != nil {
				return err
			}
			fmt.Fprintf(a.out, "Set %s\n", args[1])
			return nil
		},
	}
	keySet.Flags().StringVar(&kind, "type", "string", "string, hash, list, set or zset")
	keySet.Flags().Int64Var(&ttl, "ttl", 0, "expire after this many seconds")
	keyDel := &cobra.Command{
		Use: "delete NAME KEY...", Aliases: []string{"rm", "del"}, Short: "Delete keys", Args: cobra.MinimumNArgs(2), Annotations: op("deleteDatabaseKey"),
		RunE: func(cmd *cobra.Command, args []string) error {
			q := url.Values{"key": args[1:]}
			var out struct {
				Deleted int `json:"deleted"`
			}
			if err := a.do(cmd, "DELETE", dbItem(args[0])+"/key?"+q.Encode(), nil, &out); err != nil {
				return err
			}
			fmt.Fprintf(a.out, "Deleted %d key(s)\n", out.Deleted)
			return nil
		},
	}
	keyTTL := &cobra.Command{
		Use: "expire NAME KEY SECONDS", Short: "Set a key's TTL (-1 removes it)", Args: cobra.ExactArgs(3), Annotations: op("expireDatabaseKey"),
		RunE: func(cmd *cobra.Command, args []string) error {
			sec, err := strconv.ParseInt(args[2], 10, 64)
			if err != nil {
				return errors.New("SECONDS must be a number (-1 removes the TTL)")
			}
			return a.do(cmd, "PUT", dbItem(args[0])+"/key/ttl?key="+url.QueryEscape(args[1]), map[string]any{"ttlSeconds": sec}, nil)
		},
	}
	keyCmd.AddCommand(keyGet, keySet, keyDel, keyTTL)

	run := &cobra.Command{
		Use: "cmd NAME COMMAND [ARG...]", Aliases: []string{"exec", "cli"}, Short: "Run one command on the primary (administration commands are refused)",
		Example: `  synctl db cmd cache INCR visits
  synctl db cmd cache HGETALL user:1`,
		Args: cobra.MinimumNArgs(2), Annotations: op("runDatabaseCommand"),
		RunE: func(cmd *cobra.Command, args []string) error {
			var out struct {
				Result any `json:"result"`
			}
			if err := a.do(cmd, "POST", dbItem(args[0])+"/command", map[string]any{"args": args[1:]}, &out); err != nil {
				return err
			}
			if a.output == "json" {
				return a.printer().json(out)
			}
			printReply(a, out.Result, "")
			return nil
		},
	}

	info := &cobra.Command{
		Use: "info NAME [SECTION]", Short: "The primary's INFO", Args: cobra.RangeArgs(1, 2), Annotations: op("getDatabaseInfo"),
		RunE: func(cmd *cobra.Command, args []string) error {
			var sec map[string]map[string]string
			if err := a.do(cmd, "GET", dbItem(args[0])+"/info", nil, &sec); err != nil {
				return err
			}
			if len(args) == 2 {
				sec = map[string]map[string]string{args[1]: sec[strings.ToLower(args[1])]}
			}
			return a.printer().json(sec)
		},
	}
	slow := &cobra.Command{
		Use: "slowlog NAME", Short: "The primary's slowest recent commands", Args: cobra.ExactArgs(1), Annotations: op("getDatabaseSlowlog"),
		RunE: func(cmd *cobra.Command, args []string) error {
			var out struct {
				Items []struct {
					At       time.Time `json:"at"`
					Duration int64     `json:"durationMicros"`
					Args     []string  `json:"args"`
				} `json:"items"`
			}
			if err := a.do(cmd, "GET", dbItem(args[0])+"/slowlog", nil, &out); err != nil {
				return err
			}
			rows := [][]string{}
			for _, e := range out.Items {
				rows = append(rows, []string{e.At.Local().Format("15:04:05"), fmt.Sprintf("%.1f ms", float64(e.Duration)/1000), strings.Join(e.Args, " ")})
			}
			return a.printer().table(out.Items, []string{"AT", "DURATION", "COMMAND"}, rows)
		},
	}

	db.AddCommand(list, create, get, update, network, engines,
		simple("delete", "Delete a database and its data", "DELETE", "", "deleteDatabase", "Deleting database %s"),
		creds,
		simple("failover", "Promote a replica to primary", "POST", "/failover", "failoverDatabase", "Failover of %s requested"),
		metricsCmd, events, keys, keyCmd, run, info, slow)
	return db
}

// printReply prints a command reply like valkey-cli.
func printReply(a *app, v any, indent string) {
	switch x := v.(type) {
	case nil:
		fmt.Fprintln(a.out, indent+"(nil)")
	case []any:
		if len(x) == 0 {
			fmt.Fprintln(a.out, indent+"(empty array)")
		}
		for i, e := range x {
			if inner, ok := e.([]any); ok {
				fmt.Fprintf(a.out, "%s%d)\n", indent, i+1)
				printReply(a, inner, indent+"   ")
				continue
			}
			b, _ := json.Marshal(e)
			fmt.Fprintf(a.out, "%s%d) %s\n", indent, i+1, b)
		}
	case string:
		fmt.Fprintf(a.out, "%s%q\n", indent, x)
	case float64:
		fmt.Fprintf(a.out, "%s(integer) %s\n", indent, strconv.FormatFloat(x, 'f', -1, 64))
	default:
		b, _ := json.Marshal(x)
		fmt.Fprintln(a.out, indent+string(b))
	}
}
