package dbs

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// clients caches one admin connection pool per member address.
type clients struct {
	mu sync.Mutex
	m  map[string]*redis.Client // "addr|password" -> client
}

func (c *clients) get(addr, user, pass string) *redis.Client {
	key := addr + "|" + user + "|" + pass
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.m == nil {
		c.m = map[string]*redis.Client{}
	}
	if cl, ok := c.m[key]; ok {
		return cl
	}
	cl := redis.NewClient(&redis.Options{
		Addr: addr, Username: user, Password: pass,
		DialTimeout: 2 * time.Second, ReadTimeout: 3 * time.Second, WriteTimeout: 3 * time.Second,
		PoolSize: 4, MaxRetries: 0, Protocol: 2, DisableIdentity: true,
	})
	c.m[key] = cl
	return cl
}

// forget closes clients of an address (a member that is gone).
func (c *clients) forget(addr string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, cl := range c.m {
		if strings.HasPrefix(k, addr+"|") {
			_ = cl.Close()
			delete(c.m, k)
		}
	}
}

// Info is a member's parsed INFO.
type Info map[string]string

func (i Info) Int(k string) int64 {
	v, _ := strconv.ParseInt(i[k], 10, 64)
	return v
}

func (i Info) Float(k string) float64 {
	v, _ := strconv.ParseFloat(i[k], 64)
	return v
}

// parseInfo reads INFO output ("key:value" lines; "# Section" headers).
func parseInfo(s string) Info {
	out := Info{}
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' {
			continue
		}
		if k, v, ok := strings.Cut(line, ":"); ok {
			out[k] = v
		}
	}
	return out
}

// Keys is the total key count over every logical database (db0=keys=…).
func (i Info) Keys() int64 {
	var n int64
	for k, v := range i {
		if !strings.HasPrefix(k, "db") {
			continue
		}
		for _, f := range strings.Split(v, ",") {
			if c, ok := strings.CutPrefix(f, "keys="); ok {
				x, _ := strconv.ParseInt(c, 10, 64)
				n += x
			}
		}
	}
	return n
}

// sentinelView is what one sentinel knows of the database.
type sentinelView struct {
	Primary  string // host
	Replicas int    // replicas it knows
	Peers    int    // other sentinels it knows
	Flags    string
}

// sentinelState asks a sentinel for the primary and what it knows about it.
func sentinelState(ctx context.Context, cl *redis.Client, name string) (sentinelView, error) {
	res, err := cl.Do(ctx, "SENTINEL", "MASTER", name).StringSlice()
	if err != nil {
		return sentinelView{}, err
	}
	kv := map[string]string{}
	for i := 0; i+1 < len(res); i += 2 {
		kv[res[i]] = res[i+1]
	}
	if kv["ip"] == "" {
		return sentinelView{}, fmt.Errorf("sentinel does not know %s", name)
	}
	r, _ := strconv.Atoi(kv["num-slaves"])
	p, _ := strconv.Atoi(kv["num-other-sentinels"])
	return sentinelView{Primary: kv["ip"], Replicas: r, Peers: p, Flags: kv["flags"]}, nil
}

func addr(ip string, port int) string { return net.JoinHostPort(ip, strconv.Itoa(port)) }
