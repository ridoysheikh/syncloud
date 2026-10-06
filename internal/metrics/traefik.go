package metrics

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// ServiceNames maps a SynCloud service ID to its project, environment and
// name, for labelling Traefik's metrics.
type ServiceNames func(ctx context.Context) map[string][3]string

// traefikService matches Traefik's service label: "svc-<id>-<port>@http" for
// user services, "syncloud-<component>@http" for the platform.
var traefikService = regexp.MustCompile(`service="(?:svc-(svc_[a-z0-9]+)-[^"@]*|syncloud-([a-z-]+))@[a-z]+"`)

// ScrapeTraefik copies Traefik's request metrics (§5.7, §9.1) into
// VictoriaMetrics every interval until ctx ends. Each series gets the
// SynCloud labels service_id, project, environment and app, and edge (the
// node Traefik runs on).
func (s *Store) ScrapeTraefik(ctx context.Context, metricsURL, edge string, names ServiceNames, interval time.Duration) {
	if s.url == "" || metricsURL == "" {
		return
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	var lastErr string
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		err := s.scrapeOnce(ctx, metricsURL, edge, names)
		if msg := fmt.Sprint(err); err != nil && msg != lastErr {
			s.log.Warn("scrape Traefik metrics", "err", err) // logged once per distinct error
			lastErr = msg
		} else if err == nil {
			lastErr = ""
		}
	}
}

// ScrapeTraefikOnce imports one scrape (edge nodes are scraped by their manager).
func (s *Store) ScrapeTraefikOnce(ctx context.Context, metricsURL, edge string, names ServiceNames) error {
	if s.url == "" {
		return ErrDisabled
	}
	return s.scrapeOnce(ctx, metricsURL, edge, names)
}

func (s *Store) scrapeOnce(ctx context.Context, metricsURL, edge string, names ServiceNames) error {
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, metricsURL, nil)
	if err != nil {
		return err
	}
	resp, err := s.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d from %s", resp.StatusCode, metricsURL)
	}
	body, err := Relabel(io.LimitReader(resp.Body, 32<<20), names(ctx))
	if err != nil {
		return err
	}
	if len(body) == 0 {
		return nil
	}
	q := url.Values{"extra_label": {"edge=" + edge}}
	ireq, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url+"/api/v1/import/prometheus?"+q.Encode(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	iresp, err := s.http.Do(ireq)
	if err != nil {
		return err
	}
	iresp.Body.Close()
	if iresp.StatusCode >= 300 {
		return fmt.Errorf("import into VictoriaMetrics: HTTP %d", iresp.StatusCode)
	}
	return nil
}

// Relabel keeps Traefik's service, router and entrypoint series and adds
// SynCloud labels to service series.
func Relabel(r io.Reader, names map[string][3]string) ([]byte, error) {
	var out bytes.Buffer
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "traefik_service_") && !strings.HasPrefix(line, "traefik_router_") &&
			!strings.HasPrefix(line, "traefik_entrypoint_") && !strings.HasPrefix(line, "traefik_open_connections") {
			continue
		}
		brace := strings.IndexByte(line, '{')
		if m := traefikService.FindStringSubmatch(line); m != nil && brace > 0 {
			var extra string
			if id := m[1]; id != "" {
				n, ok := names[id]
				if !ok {
					continue // a deleted service
				}
				extra = fmt.Sprintf(`service_id=%q,project=%q,environment=%q,app=%q,`, id, n[0], n[1], n[2])
			} else {
				extra = fmt.Sprintf(`project="syncloud",environment="system",app=%q,`, m[2])
			}
			line = line[:brace+1] + extra + line[brace+1:]
		}
		out.WriteString(line)
		out.WriteByte('\n')
	}
	return out.Bytes(), sc.Err()
}
