package iam

import "testing"

func TestMatch(t *testing.T) {
	for _, c := range []struct {
		p, s string
		want bool
	}{
		{"*", "anything", true}, {"service:Get*", "service:GetService", true}, {"service:Get*", "service:ScaleService", false},
		{"*:List*", "network:ListAllSecurityGroups", true}, {"srn:syncloud:project/shop/*", "srn:syncloud:project/shop/env/x", true},
		{"srn:syncloud:project/shop/*", "srn:syncloud:project/shopping/env/x", false}, {"a?c", "abc", true}, {"a?c", "ac", false},
		{"*Middleware", "traefik:CreateMiddleware", true}, {"*Middleware", "traefik:ListMiddlewares", false},
	} {
		if got := Match(c.p, c.s); got != c.want {
			t.Errorf("Match(%q, %q) = %v", c.p, c.s, got)
		}
	}
}

func TestEvaluate(t *testing.T) {
	doc, err := Parse([]byte(`{"Version":"2026-01","Statement":[
		{"Sid":"read","Effect":"Allow","Action":["service:Get*","service:List*"],"Resource":"srn:syncloud:project/shop/*"},
		{"Effect":"Allow","Action":"service:ScaleService","Resource":"srn:syncloud:project/shop/*","Condition":{"IpAddress":{"syn:SourceIp":["10.0.0.0/8"]}}},
		{"Sid":"nodelete","Effect":"Deny","Action":"service:Delete*","Resource":"*","Condition":{"Bool":{"syn:MFAPresent":"false"}}},
		{"Effect":"Allow","Action":"service:DeleteService","Resource":"*"}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	var st []Attached
	for i, s := range doc.Statement {
		st = append(st, Attached{Policy: "p", Statement: s, Index: i})
	}
	web := "srn:syncloud:project/shop/env/production/service/web"
	cases := []struct {
		action, res string
		c           Context
		want        bool
	}{
		{"service:GetService", web, Context{}, true},
		{"service:GetService", "srn:syncloud:project/billing/env/p/service/x", Context{}, false},
		{"service:ScaleService", web, Context{SourceIP: "10.1.2.3"}, true},
		{"service:ScaleService", web, Context{SourceIP: "203.0.113.1"}, false},
		{"service:DeleteService", web, Context{MFA: false}, false},
		{"service:DeleteService", web, Context{MFA: true}, true},
	}
	for _, c := range cases {
		if d := Evaluate(st, c.action, c.res, c.c); d.Allowed != c.want {
			t.Errorf("%s on %s %+v: %+v", c.action, c.res, c.c, d)
		}
	}
	if d := Evaluate(st, "service:DeleteService", web, Context{}); d.Sid != "nodelete" {
		t.Errorf("deny not named: %+v", d)
	}
	if !AllowsAny(st, "service:ListServices", Context{}) || AllowsAny(st, "node:ListNodes", Context{}) {
		t.Error("AllowsAny")
	}
	for _, bad := range []string{
		`{"Version":"2020","Statement":[{"Effect":"Allow","Action":"*","Resource":"*"}]}`,
		`{"Version":"2026-01","Statement":[]}`,
		`{"Version":"2026-01","Statement":[{"Effect":"Allow","Action":"bad action","Resource":"*"}]}`,
		`{"Version":"2026-01","Statement":[{"Effect":"Allow","Action":"*","Resource":"arn:aws:s3:::x"}]}`,
		`{"Version":"2026-01","Statement":[{"Effect":"Allow","Action":"*","Resource":"*","Condition":{"Bool":{"aws:x":"1"}}}]}`,
		`{"Version":"2026-01","Statement":[{"Effect":"Allow","Action":"*","Resource":"*","Condition":{"IpAddress":{"syn:SourceIp":"nope"}}}]}`,
		`{"Version":"2026-01","Statement":[{"Effect":"Allow","Action":"*","Resource":"*","Extra":1}]}`,
	} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("accepted %s", bad)
		}
	}
}
