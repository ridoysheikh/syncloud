package domain

import (
	"context"
	"net"
	"sync"

	"syncloud/internal/store"
)

// Endpoints are the public addresses derived from the base domain.
type Endpoints struct {
	BaseDomain   string `json:"baseDomain"`
	DashboardURL string `json:"dashboardUrl"`
	RegistryHost string `json:"registryHost"`
}

// Service owns the base domain setting and tells listeners when it changes.
type Service struct {
	st *store.Store
	// HTTPSPort is appended to URLs when it is not 443 (development).
	HTTPSPort string
	// DevURL is the dashboard URL before a base domain exists.
	DevURL string
	// DevRegistryHost is the registry hostname before a base domain exists.
	DevRegistryHost string

	mu        sync.Mutex
	base      string
	listeners []func(Endpoints)
}

func NewService(st *store.Store, httpsPort, devURL, devRegistryHost string) *Service {
	if httpsPort == "443" {
		httpsPort = ""
	}
	return &Service{st: st, HTTPSPort: httpsPort, DevURL: devURL, DevRegistryHost: devRegistryHost}
}

func (s *Service) Load(ctx context.Context) error {
	base, _, err := s.st.GetSetting(ctx, store.SettingBaseDomain)
	s.mu.Lock()
	s.base = base
	s.mu.Unlock()
	return err
}

// Base returns the base domain ("" until set).
func (s *Service) Base() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.base
}

func (s *Service) Endpoints() Endpoints {
	base := s.Base()
	if base == "" {
		return Endpoints{DashboardURL: s.DevURL, RegistryHost: s.DevRegistryHost}
	}
	u := "https://" + base
	reg := RegistryHost(base)
	if s.HTTPSPort != "" {
		u += ":" + s.HTTPSPort
		reg += ":" + s.HTTPSPort
	}
	return Endpoints{BaseDomain: base, DashboardURL: u, RegistryHost: reg}
}

// OnChange registers fn, called after every successful Set.
func (s *Service) OnChange(fn func(Endpoints)) {
	s.mu.Lock()
	s.listeners = append(s.listeners, fn)
	s.mu.Unlock()
}

// Set validates, stores and announces a new base domain.
func (s *Service) Set(ctx context.Context, base string) (Endpoints, error) {
	base, err := Normalize(base)
	if err != nil {
		return Endpoints{}, err
	}
	if err := s.st.SetSetting(ctx, store.SettingBaseDomain, base); err != nil {
		return Endpoints{}, err
	}
	s.mu.Lock()
	changed := s.base != base
	s.base = base
	ls := append([]func(Endpoints){}, s.listeners...)
	s.mu.Unlock()
	ep := s.Endpoints()
	if changed {
		for _, fn := range ls {
			fn(ep)
		}
	}
	return ep, nil
}

// Resolves reports whether host resolves to ip (for warnings on custom domains).
func Resolves(ctx context.Context, host, ip string) (bool, []string) {
	addrs, err := net.DefaultResolver.LookupHost(ctx, host)
	if err != nil {
		return false, nil
	}
	for _, a := range addrs {
		if a == ip {
			return true, addrs
		}
	}
	return false, addrs
}
