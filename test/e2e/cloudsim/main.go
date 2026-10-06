// Command cloudsim is a fake cloud for end-to-end tests of provider-backed
// node pools (§6.5). It implements SynCloud's webhook provider: "create"
// starts a Docker-in-Docker node on the e2e network and runs what the
// server's cloud-init would (the agent joins with the token from the user
// data); "delete" removes it; "list" lists them.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

type serverSpec struct {
	Name     string            `json:"name"`
	UserData string            `json:"userData"`
	Labels   map[string]string `json:"labels"`
}

type server struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

var (
	network    = flag.String("network", "sc-e2e", "Docker network of the nodes")
	bin        = flag.String("bin", "", "directory with the agent binary and busybox.tar (mounted at /opt/sc)")
	controller = flag.String("controller", "", "controller URL as nodes reach it (replaces the user data's)")
	listen     = flag.String("listen", ":7099", "listen address")
	tokenRE    = regexp.MustCompile(`--token (\S+)`)
	mu         sync.Mutex
	servers    = map[string]server{}
)

func run(args ...string) (string, error) {
	out, err := exec.Command("docker", args...).CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

func create(s serverSpec) (server, error) {
	m := tokenRE.FindStringSubmatch(s.UserData)
	if m == nil {
		return server{}, fmt.Errorf("no join token in the user data")
	}
	name := "sc-e2e-" + s.Name
	id, err := run("run", "-d", "--privileged", "--name", name, "--hostname", s.Name, "--network", *network, "-v", *bin+":/opt/sc:ro",
		"--label", "cloudsim.pool="+s.Labels["syncloud-pool"], "syncloud-e2e-node", "dockerd", "-H", "unix:///var/run/docker.sock")
	if err != nil {
		return server{}, fmt.Errorf("docker run: %v: %s", err, id)
	}
	srv := server{ID: name, Name: s.Name, Status: "initializing"}
	mu.Lock()
	servers[name] = srv
	mu.Unlock()
	// Boot like cloud-init would: Docker up, then join and start the agent.
	go func() {
		for range 60 {
			if _, err := run("exec", name, "docker", "info"); err == nil {
				break
			}
			time.Sleep(time.Second)
		}
		_, _ = run("exec", name, "docker", "load", "-q", "-i", "/opt/sc/busybox.tar")
		if out, err := run("exec", name, "/opt/sc/syncloud-agent", "join", "--controller", *controller, "--token", m[1], "--name", s.Name, "--data-dir", "/agent"); err != nil {
			log.Printf("join %s: %v: %s", s.Name, err, out)
			return
		}
		_, _ = run("exec", "-d", name, "sh", "-c", "/opt/sc/syncloud-agent run --data-dir /agent --network on > /var/log/agent.log 2>&1")
		mu.Lock()
		srv.Status = "running"
		servers[name] = srv
		mu.Unlock()
		log.Printf("server %s joined", s.Name)
	}()
	return srv, nil
}

func main() {
	flag.Parse()
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Action string     `json:"action"`
			Server serverSpec `json:"server"`
			ID     string     `json:"id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		log.Printf("%s %s %s", req.Action, req.Server.Name, req.ID)
		switch req.Action {
		case "create":
			s, err := create(req.Server)
			if err != nil {
				http.Error(w, err.Error(), 500)
				return
			}
			_ = json.NewEncoder(w).Encode(s)
		case "delete":
			_, _ = run("rm", "-f", req.ID)
			mu.Lock()
			delete(servers, req.ID)
			mu.Unlock()
			w.WriteHeader(204)
		case "list":
			mu.Lock()
			out := []server{}
			for _, s := range servers {
				out = append(out, s)
			}
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(out)
		default:
			http.Error(w, "unknown action", 400)
		}
	})
	log.Fatal(http.ListenAndServe(*listen, nil))
}
