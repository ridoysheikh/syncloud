package dbs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/ridoysheikh/syncloud/internal/store"
)

// ErrUnavailable means no member can answer right now.
var ErrUnavailable = errors.New("the database's primary is not reachable")

// readLimit caps how much of a value the explorer returns.
const (
	maxItems       = 500
	maxStringBytes = 1 << 20
)

// primaryClient connects to the current primary as the admin user.
func (m *Manager) primaryClient(ctx context.Context, d store.Database) (*redis.Client, error) {
	sec, err := m.secrets(d)
	if err != nil {
		return nil, err
	}
	st := parseState(d.State)
	members, err := m.st.DatabaseMembers(ctx, d.ID)
	if err != nil {
		return nil, err
	}
	for _, mb := range members {
		if mb.Kind == KindData && mb.Ordinal == st.Primary && mb.IP != "" && mb.State == store.TaskRunning {
			return m.cl.get(addr(mb.IP, Port), adminUser, sec.AdminPassword), nil
		}
	}
	return nil, ErrUnavailable
}

// KeyInfo is one key in a listing.
type KeyInfo struct {
	Key   string `json:"key"`
	Type  string `json:"type"`
	TTL   int64  `json:"ttlMs"` // -1 = no expiry
	Bytes int64  `json:"bytes"` // MEMORY USAGE (approximate)
}

// KeyPage is a page of a SCAN.
type KeyPage struct {
	Cursor uint64    `json:"cursor"` // 0 = done
	Keys   []KeyInfo `json:"keys"`
	Total  int64     `json:"total"` // keys in the database (DBSIZE)
}

// Scan lists keys matching pattern (and type), one SCAN step at a time.
func (m *Manager) Scan(ctx context.Context, d store.Database, pattern, typ string, cursor uint64, count int) (KeyPage, error) {
	cl, err := m.primaryClient(ctx, d)
	if err != nil {
		return KeyPage{}, err
	}
	if pattern == "" {
		pattern = "*"
	}
	count = min(max(count, 10), 1000)
	page := KeyPage{Keys: []KeyInfo{}}
	// A sparse pattern can return empty steps: keep scanning to fill a page.
	deadline := time.Now().Add(2 * time.Second)
	for {
		var keys []string
		var next uint64
		if typ != "" {
			keys, next, err = cl.ScanType(ctx, cursor, pattern, int64(count), typ).Result()
		} else {
			keys, next, err = cl.Scan(ctx, cursor, pattern, int64(count)).Result()
		}
		if err != nil {
			return KeyPage{}, err
		}
		for _, k := range keys {
			page.Keys = append(page.Keys, KeyInfo{Key: k})
		}
		cursor = next
		if cursor == 0 || len(page.Keys) >= count || time.Now().After(deadline) {
			break
		}
	}
	page.Cursor = cursor
	pipe := cl.Pipeline()
	types := make([]*redis.StatusCmd, len(page.Keys))
	ttls := make([]*redis.DurationCmd, len(page.Keys))
	sizes := make([]*redis.IntCmd, len(page.Keys))
	for i, k := range page.Keys {
		types[i] = pipe.Type(ctx, k.Key)
		ttls[i] = pipe.PTTL(ctx, k.Key)
		sizes[i] = pipe.MemoryUsage(ctx, k.Key)
	}
	total := pipe.DBSize(ctx)
	_, _ = pipe.Exec(ctx)
	for i := range page.Keys {
		page.Keys[i].Type = types[i].Val()
		page.Keys[i].TTL = ttlMs(ttls[i].Val())
		page.Keys[i].Bytes = sizes[i].Val()
	}
	page.Total = total.Val()
	return page, nil
}

func ttlMs(d time.Duration) int64 {
	if d < 0 {
		return -1
	}
	return d.Milliseconds()
}

// Value is one key with its value, shaped by type.
type Value struct {
	Key       string            `json:"key"`
	Type      string            `json:"type"`
	TTL       int64             `json:"ttlMs"`
	Bytes     int64             `json:"bytes"`
	Length    int64             `json:"length"`    // items, fields or bytes
	Truncated bool              `json:"truncated"` // only the first items are shown
	String    *string           `json:"string,omitempty"`
	Hash      map[string]string `json:"hash,omitempty"`
	List      []string          `json:"list,omitempty"`
	Set       []string          `json:"set,omitempty"`
	ZSet      []ZMember         `json:"zset,omitempty"`
	Stream    []StreamEntry     `json:"stream,omitempty"`
}

type ZMember struct {
	Member string  `json:"member"`
	Score  float64 `json:"score"`
}

type StreamEntry struct {
	ID     string            `json:"id"`
	Fields map[string]string `json:"fields"`
}

// ErrNoKey means the key does not exist.
var ErrNoKey = errors.New("no such key")

// Get reads a key.
func (m *Manager) Get(ctx context.Context, d store.Database, key string) (Value, error) {
	cl, err := m.primaryClient(ctx, d)
	if err != nil {
		return Value{}, err
	}
	typ, err := cl.Type(ctx, key).Result()
	if err != nil {
		return Value{}, err
	}
	if typ == "none" {
		return Value{}, ErrNoKey
	}
	v := Value{Key: key, Type: typ}
	ttl, _ := cl.PTTL(ctx, key).Result()
	v.TTL = ttlMs(ttl)
	v.Bytes, _ = cl.MemoryUsage(ctx, key).Result()
	switch typ {
	case "string":
		v.Length, _ = cl.StrLen(ctx, key).Result()
		s, err := cl.GetRange(ctx, key, 0, maxStringBytes-1).Result()
		if err != nil {
			return Value{}, err
		}
		v.String, v.Truncated = &s, v.Length > maxStringBytes
	case "hash":
		v.Length, _ = cl.HLen(ctx, key).Result()
		v.Hash = map[string]string{}
		var cur uint64
		for len(v.Hash) < maxItems {
			kv, next, err := cl.HScan(ctx, key, cur, "*", 200).Result()
			if err != nil {
				return Value{}, err
			}
			for i := 0; i+1 < len(kv); i += 2 {
				v.Hash[kv[i]] = kv[i+1]
			}
			if cur = next; cur == 0 {
				break
			}
		}
		v.Truncated = int64(len(v.Hash)) < v.Length
	case "list":
		v.Length, _ = cl.LLen(ctx, key).Result()
		v.List, err = cl.LRange(ctx, key, 0, maxItems-1).Result()
		v.Truncated = v.Length > maxItems
	case "set":
		v.Length, _ = cl.SCard(ctx, key).Result()
		var cur uint64
		for len(v.Set) < maxItems {
			ms, next, err := cl.SScan(ctx, key, cur, "*", 200).Result()
			if err != nil {
				return Value{}, err
			}
			v.Set = append(v.Set, ms...)
			if cur = next; cur == 0 {
				break
			}
		}
		v.Truncated = int64(len(v.Set)) < v.Length
	case "zset":
		v.Length, _ = cl.ZCard(ctx, key).Result()
		zs, err := cl.ZRangeWithScores(ctx, key, 0, maxItems-1).Result()
		if err != nil {
			return Value{}, err
		}
		for _, z := range zs {
			v.ZSet = append(v.ZSet, ZMember{Member: fmt.Sprint(z.Member), Score: z.Score})
		}
		v.Truncated = v.Length > maxItems
	case "stream":
		v.Length, _ = cl.XLen(ctx, key).Result()
		xs, err := cl.XRevRangeN(ctx, key, "+", "-", 100).Result()
		if err != nil {
			return Value{}, err
		}
		for _, x := range xs {
			e := StreamEntry{ID: x.ID, Fields: map[string]string{}}
			for k, val := range x.Values {
				e.Fields[k] = fmt.Sprint(val)
			}
			v.Stream = append(v.Stream, e)
		}
		v.Truncated = v.Length > 100
	}
	return v, err
}

// Write sets a key. Replace deletes it first; otherwise fields, items and
// members are added to what is there.
type Write struct {
	Type    string            `json:"type"` // string | hash | list | set | zset
	String  string            `json:"string"`
	Hash    map[string]string `json:"hash"`
	List    []string          `json:"list"`
	Set     []string          `json:"set"`
	ZSet    []ZMember         `json:"zset"`
	TTL     int64             `json:"ttlSeconds"` // 0 = keep / none
	Replace bool              `json:"replace"`
}

// Set writes a key in one transaction.
func (m *Manager) Set(ctx context.Context, d store.Database, key string, w Write) error {
	if key == "" {
		return ErrInvalid{errors.New("key must not be empty")}
	}
	cl, err := m.primaryClient(ctx, d)
	if err != nil {
		return err
	}
	_, err = cl.TxPipelined(ctx, func(p redis.Pipeliner) error {
		if w.Replace {
			p.Del(ctx, key)
		}
		switch w.Type {
		case "string":
			p.Set(ctx, key, w.String, 0)
		case "hash":
			if len(w.Hash) == 0 {
				return ErrInvalid{errors.New("hash: at least one field")}
			}
			p.HSet(ctx, key, w.Hash)
		case "list":
			if len(w.List) == 0 {
				return ErrInvalid{errors.New("list: at least one item")}
			}
			p.RPush(ctx, key, toAny(w.List)...)
		case "set":
			if len(w.Set) == 0 {
				return ErrInvalid{errors.New("set: at least one member")}
			}
			p.SAdd(ctx, key, toAny(w.Set)...)
		case "zset":
			if len(w.ZSet) == 0 {
				return ErrInvalid{errors.New("zset: at least one member")}
			}
			zs := make([]redis.Z, len(w.ZSet))
			for i, z := range w.ZSet {
				zs[i] = redis.Z{Score: z.Score, Member: z.Member}
			}
			p.ZAdd(ctx, key, zs...)
		default:
			return ErrInvalid{errors.New("type must be string, hash, list, set or zset")}
		}
		if w.TTL > 0 {
			p.Expire(ctx, key, time.Duration(w.TTL)*time.Second)
		}
		return nil
	})
	return err
}

func toAny(xs []string) []any {
	out := make([]any, len(xs))
	for i, x := range xs {
		out[i] = x
	}
	return out
}

// Delete removes keys.
func (m *Manager) DeleteKeys(ctx context.Context, d store.Database, keys ...string) (int64, error) {
	cl, err := m.primaryClient(ctx, d)
	if err != nil {
		return 0, err
	}
	return cl.Unlink(ctx, keys...).Result()
}

// Expire sets a key's TTL in seconds (-1 removes it).
func (m *Manager) Expire(ctx context.Context, d store.Database, key string, seconds int64) error {
	cl, err := m.primaryClient(ctx, d)
	if err != nil {
		return err
	}
	var ok bool
	if seconds < 0 {
		ok, err = cl.Persist(ctx, key).Result()
		if err == nil && !ok {
			if n, _ := cl.Exists(ctx, key).Result(); n == 1 {
				return nil // had no TTL
			}
		}
	} else {
		ok, err = cl.Expire(ctx, key, time.Duration(seconds)*time.Second).Result()
	}
	if err == nil && !ok {
		return ErrNoKey
	}
	return err
}

// denied are commands the console refuses: administration the platform
// owns, and commands that block or stream.
var denied = map[string]string{
	"CONFIG": "managed by SynCloud (change the database's settings instead)", "DEBUG": "administration", "SHUTDOWN": "administration",
	"MODULE": "administration", "REPLICAOF": "replication is managed by Sentinel", "SLAVEOF": "replication is managed by Sentinel",
	"FAILOVER": "use the Failover button", "ACL": "users are managed by SynCloud", "SYNC": "replication", "PSYNC": "replication",
	"MONITOR": "streams forever", "SUBSCRIBE": "streams forever", "PSUBSCRIBE": "streams forever", "SSUBSCRIBE": "streams forever",
	"CLUSTER": "cluster mode is not used", "SAVE": "blocks the server (persistence is managed)", "BGREWRITEAOF": "persistence is managed",
	"MIGRATE": "administration", "HELLO": "connection state", "AUTH": "connection state", "SELECT": "the console uses database 0", "QUIT": "connection state",
	"RESET": "connection state", "CLIENT": "connection state",
}

// Command runs one console command on the primary.
func (m *Manager) Command(ctx context.Context, d store.Database, args []string) (any, error) {
	if len(args) == 0 {
		return nil, ErrInvalid{errors.New("empty command")}
	}
	if why, bad := denied[strings.ToUpper(args[0])]; bad {
		return nil, ErrInvalid{fmt.Errorf("%s is not allowed here: %s", strings.ToUpper(args[0]), why)}
	}
	cl, err := m.primaryClient(ctx, d)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	res, err := cl.Do(ctx, toAny(args)...).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		var re redis.Error
		if errors.As(err, &re) {
			return nil, ErrInvalid{err} // the server's own error, e.g. WRONGTYPE
		}
		return nil, err
	}
	return plain(res), nil
}

// plain turns a RESP reply into JSON-friendly values.
func plain(v any) any {
	switch x := v.(type) {
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = plain(x[i])
		}
		return out
	case map[any]any:
		out := map[string]any{}
		for k, val := range x {
			out[fmt.Sprint(k)] = plain(val)
		}
		return out
	}
	return v
}

// Sections is INFO grouped by section.
type Sections map[string]map[string]string

// InfoSections returns the primary's INFO, by section.
func (m *Manager) InfoSections(ctx context.Context, d store.Database) (Sections, error) {
	cl, err := m.primaryClient(ctx, d)
	if err != nil {
		return nil, err
	}
	raw, err := cl.Info(ctx, "everything").Result()
	if err != nil {
		return nil, err
	}
	out := Sections{}
	sec := "other"
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if s, ok := strings.CutPrefix(line, "# "); ok {
			sec = strings.ToLower(s)
			continue
		}
		if k, v, ok := strings.Cut(line, ":"); ok {
			if out[sec] == nil {
				out[sec] = map[string]string{}
			}
			out[sec][k] = v
		}
	}
	return out, nil
}

// SlowEntry is one slow log entry.
type SlowEntry struct {
	ID       int64     `json:"id"`
	At       time.Time `json:"at"`
	Duration int64     `json:"durationMicros"`
	Args     []string  `json:"args"`
	Client   string    `json:"client"`
}

// SlowLog returns the primary's slowest recent commands.
func (m *Manager) SlowLog(ctx context.Context, d store.Database) ([]SlowEntry, error) {
	cl, err := m.primaryClient(ctx, d)
	if err != nil {
		return nil, err
	}
	logs, err := cl.SlowLogGet(ctx, 50).Result()
	if err != nil {
		return nil, err
	}
	out := make([]SlowEntry, 0, len(logs))
	for _, l := range logs {
		out = append(out, SlowEntry{ID: l.ID, At: l.Time.UTC(), Duration: l.Duration.Microseconds(), Args: l.Args, Client: l.ClientAddr})
	}
	return out, nil
}
