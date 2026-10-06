// Command gitserver serves bare repositories over Git's smart HTTP protocol
// (git http-backend) for the builds end-to-end test.
//
//	gitserver -root /srv/git -listen :3000
package main

import (
	"flag"
	"log"
	"net/http"
	"net/http/cgi"
	"os/exec"
)

func main() {
	root := flag.String("root", "/srv/git", "directory with bare repositories")
	listen := flag.String("listen", ":3000", "listen address")
	flag.Parse()
	git, err := exec.LookPath("git")
	if err != nil {
		log.Fatal(err)
	}
	h := &cgi.Handler{
		Path: git,
		Args: []string{"http-backend"},
		Env:  []string{"GIT_PROJECT_ROOT=" + *root, "GIT_HTTP_EXPORT_ALL=1"},
	}
	log.Fatal(http.ListenAndServe(*listen, h))
}
