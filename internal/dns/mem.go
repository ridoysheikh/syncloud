package dns

import (
	"context"
	"fmt"
	"strings"
	"sync"
)

// Mem is an in-memory provider with one zone, for tests of everything that
// uses a Provider (the real ones are exercised against fake HTTP servers).
type Mem struct {
	mu      sync.Mutex
	Zone    ZoneInfo
	records []Record
	next    int
	Calls   []string
	// Fail, when set, makes the named call ("create", "delete", "update", "records") fail.
	Fail map[string]error
}

func NewMem(zone string, records ...Record) *Mem {
	m := &Mem{Zone: ZoneInfo{ID: "z-" + zone, Name: zone, Status: "active", NameServers: []string{"ns1." + zone, "ns2." + zone}}, Fail: map[string]error{}}
	for _, r := range records {
		m.add(r)
	}
	return m
}

func (m *Mem) add(r Record) Record {
	m.next++
	r.ID = fmt.Sprintf("r%d", m.next)
	r.Type, r.Name = normType(r.Type), Fqdn(r.Name)
	if r.TTL == 0 {
		r.TTL = 1
	}
	m.records = append(m.records, r)
	return r
}

func (m *Mem) Verify(context.Context) error { return m.Fail["verify"] }

func (m *Mem) Zones(context.Context) ([]ZoneInfo, error) { return []ZoneInfo{m.Zone}, nil }

func (m *Mem) Records(_ context.Context, _ string) ([]Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.Fail["records"]; err != nil {
		return nil, err
	}
	return append([]Record(nil), m.records...), nil
}

func (m *Mem) Create(_ context.Context, _ string, r Record) (Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.Fail["create"]; err != nil {
		return Record{}, err
	}
	m.Calls = append(m.Calls, "create "+r.Type+" "+Fqdn(r.Name)+" "+r.Content)
	return m.add(r), nil
}

func (m *Mem) Update(_ context.Context, _, id string, r Record) (Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.Fail["update"]; err != nil {
		return Record{}, err
	}
	for i := range m.records {
		if m.records[i].ID == id {
			r.ID = id
			r.Type, r.Name = normType(r.Type), Fqdn(r.Name)
			m.records[i] = r
			m.Calls = append(m.Calls, "update "+r.Type+" "+r.Name+" "+r.Content)
			return r, nil
		}
	}
	return Record{}, &Error{Provider: "mem", Status: 404, Message: "no such record"}
}

func (m *Mem) Delete(_ context.Context, _, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.Fail["delete"]; err != nil {
		return err
	}
	for i := range m.records {
		if m.records[i].ID == id {
			m.Calls = append(m.Calls, "delete "+m.records[i].Type+" "+m.records[i].Name+" "+m.records[i].Content)
			m.records = append(m.records[:i], m.records[i+1:]...)
			return nil
		}
	}
	return &Error{Provider: "mem", Status: 404, Message: "no such record"}
}

// Snapshot lists the records as "TYPE name content", sorted by insertion.
func (m *Mem) Snapshot() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []string
	for _, r := range m.records {
		out = append(out, strings.Join([]string{r.Type, r.Name, r.Content}, " "))
	}
	return out
}

var _ Provider = (*Mem)(nil)
