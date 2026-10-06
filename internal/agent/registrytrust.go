package agent

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// DockerCertsDir is where the Docker daemon looks for per-registry CAs; it
// reads them on every connection, so no restart is needed.
var DockerCertsDir = "/etc/docker/certs.d"

// trustRegistry makes Docker trust caPEM for the registry of image: the
// platform registry has a self-signed certificate on private networks,
// where ACME cannot issue one (§5.9).
func trustRegistry(log *slog.Logger, image, caPEM string) {
	if caPEM == "" {
		return
	}
	host, _, ok := strings.Cut(image, "/")
	if !ok || !strings.ContainsAny(host, ".:") || strings.ContainsAny(host, `/\ `) || strings.Contains(host, "..") {
		return
	}
	dir := filepath.Join(DockerCertsDir, host)
	path := filepath.Join(dir, "ca.crt")
	if cur, err := os.ReadFile(path); err == nil && bytes.Equal(cur, []byte(caPEM)) {
		return
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		log.Warn("trust registry certificate", "registry", host, "err", err)
		return
	}
	if err := os.WriteFile(path, []byte(caPEM), 0o644); err != nil {
		log.Warn("trust registry certificate", "registry", host, "err", err)
		return
	}
	log.Info("trusting the platform registry's self-signed certificate", "registry", host)
}
