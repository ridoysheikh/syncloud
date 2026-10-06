// Command hooksink records the requests it receives, one line each
// ("<path> <body>"), for the alerts end-to-end test.
//
//	hooksink -listen 127.0.0.1:9999 -out /tmp/hooks.log
package main

import (
	"bytes"
	"flag"
	"io"
	"log"
	"net/http"
	"os"
	"sync"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:9999", "listen address")
	out := flag.String("out", "/tmp/hooks.log", "file to append requests to")
	flag.Parse()
	f, err := os.OpenFile(*out, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		log.Fatal(err)
	}
	var mu sync.Mutex
	log.Fatal(http.ListenAndServe(*listen, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		mu.Lock()
		defer mu.Unlock()
		_, _ = f.WriteString(r.URL.Path + " " + string(bytes.ReplaceAll(b, []byte("\n"), []byte(`\n`))) + "\n")
	})))
}
