package api

import (
	_ "embed"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

//go:embed assets/join.sh
var joinScript string

// handleJoinScript serves the worker installer (§6.1) with this controller's
// public URL filled in.
func (s *Server) handleJoinScript(w http.ResponseWriter, r *http.Request) {
	scheme := "http"
	if r.TLS != nil || isHTTPS(r) {
		scheme = "https"
	}
	url := scheme + "://" + r.Host
	w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(strings.ReplaceAll(joinScript, "{{URL}}", url)))
}

var downloadName = regexp.MustCompile(`^((syncloud-agent|synctl)-linux-(amd64|arm64)|SHA256SUMS)$`)

// handleDownload serves agent and CLI binaries from the downloads directory
// (filled by install.sh). Integrity is checked against SHA256SUMS by join.sh.
func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	if s.downloadsDir == "" || !downloadName.MatchString(name) {
		http.NotFound(w, r)
		return
	}
	path := filepath.Join(s.downloadsDir, name)
	if _, err := os.Stat(path); err != nil {
		http.Error(w, name+" is not available on this controller; reinstall with install.sh to populate "+s.downloadsDir, http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeFile(w, r, path)
}
