// Package iam evaluates AWS-style JSON policies (§7): statements allow or
// deny actions ("service:ScaleService") on resources
// ("srn:syncloud:project/shop/env/production/service/web"), with optional
// conditions. An explicit Deny beats an Allow, which beats the implicit deny.
package iam

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"regexp"
	"strings"
)

// Version is the only policy language version.
const Version = "2026-01"

// Document is a policy.
type Document struct {
	Version   string      `json:"Version"`
	Statement []Statement `json:"Statement"`
}

// Statement is one rule. Action and Resource accept a string or a list.
type Statement struct {
	Sid       string                           `json:"Sid,omitempty"`
	Effect    string                           `json:"Effect"` // Allow | Deny
	Action    StringList                       `json:"Action"`
	Resource  StringList                       `json:"Resource"`
	Condition map[string]map[string]StringList `json:"Condition,omitempty"`
}

// StringList unmarshals from a string or a list of strings.
type StringList []string

func (l *StringList) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		*l = StringList{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return errors.New("must be a string or a list of strings")
	}
	*l = many
	return nil
}

// Condition keys (§7.1).
const (
	KeyMFAPresent     = "syn:MFAPresent"     // "true" | "false"
	KeySourceIP       = "syn:SourceIp"       // the caller's address
	KeyCredentialType = "syn:CredentialType" // session | token | access_key | role_session
)

var conditionOps = map[string]bool{
	"Bool": true, "StringEquals": true, "StringNotEquals": true, "StringLike": true, "IpAddress": true, "NotIpAddress": true,
}

var (
	actionRE   = regexp.MustCompile(`^(\*|[a-z][a-zA-Z]*:[A-Za-z*?]+)$`)
	resourceRE = regexp.MustCompile(`^(\*|srn:syncloud:[A-Za-z0-9_./*?:@-]*)$`)
)

// Parse reads and validates a policy document.
func Parse(text []byte) (Document, error) {
	var d Document
	dec := json.NewDecoder(strings.NewReader(string(text)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return d, fmt.Errorf("policy JSON: %w", err)
	}
	return d, d.Validate()
}

// Validate checks every statement.
func (d Document) Validate() error {
	if d.Version != Version {
		return fmt.Errorf("Version must be %q", Version)
	}
	if len(d.Statement) == 0 || len(d.Statement) > 100 {
		return errors.New("a policy has between 1 and 100 statements")
	}
	for i, s := range d.Statement {
		where := fmt.Sprintf("statement %d", i+1)
		if s.Sid != "" {
			where += " (" + s.Sid + ")"
		}
		if s.Effect != "Allow" && s.Effect != "Deny" {
			return fmt.Errorf("%s: Effect must be Allow or Deny", where)
		}
		if len(s.Action) == 0 {
			return fmt.Errorf("%s: needs an Action", where)
		}
		for _, a := range s.Action {
			if !actionRE.MatchString(a) {
				return fmt.Errorf("%s: action %q: use service:Operation, wildcards allowed (service:Get*, *)", where, a)
			}
		}
		if len(s.Resource) == 0 {
			return fmt.Errorf("%s: needs a Resource (use * for everything)", where)
		}
		for _, r := range s.Resource {
			if !resourceRE.MatchString(r) {
				return fmt.Errorf("%s: resource %q: use srn:syncloud:… or *", where, r)
			}
		}
		for op, kv := range s.Condition {
			if !conditionOps[op] {
				return fmt.Errorf("%s: unknown condition operator %q", where, op)
			}
			for k, vs := range kv {
				switch k {
				case KeyMFAPresent, KeySourceIP, KeyCredentialType:
				default:
					return fmt.Errorf("%s: unknown condition key %q", where, k)
				}
				if strings.Contains(op, "IpAddress") {
					for _, v := range vs {
						if _, err := parsePrefix(v); err != nil {
							return fmt.Errorf("%s: %q is not an IP address or CIDR", where, v)
						}
					}
				}
			}
		}
	}
	return nil
}

func parsePrefix(s string) (netip.Prefix, error) {
	if p, err := netip.ParsePrefix(s); err == nil {
		return p.Masked(), nil
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return netip.Prefix{}, err
	}
	return netip.PrefixFrom(a, a.BitLen()), nil
}

// Match reports whether a pattern with * (any run) and ? (one character)
// matches s. Action service names are matched case-sensitively, as written.
func Match(pattern, s string) bool {
	// Iterative glob matching with backtracking on the last *.
	p, i := 0, 0
	star, mark := -1, 0
	for i < len(s) {
		switch {
		case p < len(pattern) && (pattern[p] == '?' || pattern[p] == s[i]):
			p++
			i++
		case p < len(pattern) && pattern[p] == '*':
			star, mark = p, i
			p++
		case star >= 0:
			p = star + 1
			mark++
			i = mark
		default:
			return false
		}
	}
	for p < len(pattern) && pattern[p] == '*' {
		p++
	}
	return p == len(pattern)
}

// Context holds the condition values of a request.
type Context struct {
	MFA      bool
	SourceIP string
	CredType string
}

func (c Context) value(key string) string {
	switch key {
	case KeyMFAPresent:
		if c.MFA {
			return "true"
		}
		return "false"
	case KeySourceIP:
		return c.SourceIP
	case KeyCredentialType:
		return c.CredType
	}
	return ""
}

// ConditionsHold reports whether a statement's conditions hold for c.
func (s Statement) ConditionsHold(c Context) bool { return s.conditionsHold(c) }

func (s Statement) conditionsHold(c Context) bool {
	for op, kv := range s.Condition {
		for k, vs := range kv {
			v := c.value(k)
			ok := false
			switch op {
			case "Bool", "StringEquals":
				for _, want := range vs {
					ok = ok || strings.EqualFold(v, want) && op == "Bool" || v == want
				}
			case "StringNotEquals":
				ok = true
				for _, want := range vs {
					ok = ok && v != want
				}
			case "StringLike":
				for _, want := range vs {
					ok = ok || Match(want, v)
				}
			case "IpAddress", "NotIpAddress":
				a, err := netip.ParseAddr(v)
				in := false
				for _, want := range vs {
					if p, perr := parsePrefix(want); perr == nil && err == nil && p.Contains(a.Unmap()) {
						in = true
					}
				}
				ok = in == (op == "IpAddress")
			}
			if !ok {
				return false
			}
		}
	}
	return true
}

func (s Statement) matches(action, resource string) bool {
	am, rm := false, false
	for _, a := range s.Action {
		if Match(a, action) {
			am = true
			break
		}
	}
	for _, r := range s.Resource {
		if Match(r, resource) {
			rm = true
			break
		}
	}
	return am && rm
}

// Attached is a statement with where it came from, for explanations.
type Attached struct {
	Policy    string // policy name
	Statement Statement
	Index     int
}

// Decision is the result of an evaluation.
type Decision struct {
	Allowed bool      `json:"allowed"`
	Reason  string    `json:"reason"` // "allowed by …", "explicitly denied by …", "no policy allows it"
	By      *Attached `json:"-"`
	Policy  string    `json:"policy,omitempty"`
	Sid     string    `json:"sid,omitempty"`
}

func describe(a Attached) string {
	s := fmt.Sprintf("%s statement %d", a.Policy, a.Index+1)
	if a.Statement.Sid != "" {
		s += " (" + a.Statement.Sid + ")"
	}
	return s
}

// Evaluate decides one request against a principal's statements.
func Evaluate(stmts []Attached, action, resource string, c Context) Decision {
	var allow *Attached
	for i := range stmts {
		a := &stmts[i]
		if !a.Statement.matches(action, resource) || !a.Statement.conditionsHold(c) {
			continue
		}
		if a.Statement.Effect == "Deny" {
			return Decision{Reason: "explicitly denied by " + describe(*a), By: a, Policy: a.Policy, Sid: a.Statement.Sid}
		}
		if allow == nil {
			allow = a
		}
	}
	if allow != nil {
		return Decision{Allowed: true, Reason: "allowed by " + describe(*allow), By: allow, Policy: allow.Policy, Sid: allow.Statement.Sid}
	}
	return Decision{Reason: "no policy allows " + action + " on " + resource}
}

// AllowsAny reports whether some statement could allow action on some
// resource (list endpoints, whose results are then filtered per item).
func AllowsAny(stmts []Attached, action string, c Context) bool {
	for _, a := range stmts {
		if a.Statement.Effect != "Allow" || !a.Statement.conditionsHold(c) {
			continue
		}
		for _, p := range a.Statement.Action {
			if Match(p, action) {
				return true
			}
		}
	}
	return false
}
