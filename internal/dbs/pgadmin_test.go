package dbs

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
)

func TestSplitSQL(t *testing.T) {
	sql := `SELECT 1; -- a; comment
SELECT 'a;b', "c;d", E'it\'s;'; /* x; /* nested; */ y */
CREATE FUNCTION f() RETURNS int AS $body$ SELECT 1; $body$ LANGUAGE sql;
DO $$ BEGIN PERFORM 1; END $$;
SELECT $1::int ;
-- only a comment;
`
	got := splitSQL(sql)
	want := []string{
		"SELECT 1",
		"-- a; comment\nSELECT 'a;b', \"c;d\", E'it\\'s;'",
		"/* x; /* nested; */ y */\nCREATE FUNCTION f() RETURNS int AS $body$ SELECT 1; $body$ LANGUAGE sql",
		"DO $$ BEGIN PERFORM 1; END $$",
		"SELECT $1::int",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d statements: %+v", len(got), got)
	}
	for i, w := range want {
		if got[i].text != w {
			t.Errorf("statement %d = %q, want %q", i, got[i].text, w)
		}
		if sql[got[i].offset:got[i].offset+len(w)] != w {
			t.Errorf("statement %d offset %d is wrong", i, got[i].offset)
		}
	}
	if n := len(splitSQL("  ;; -- nothing\n")); n != 0 {
		t.Errorf("empty input gave %d statements", n)
	}
}

func TestRoleOptions(t *testing.T) {
	yes, no, limit, until := true, false, 5, "2027-01-31T00:00:00Z"
	o, err := roleOptions(PgRoleInput{Login: &yes, CreateDB: &no, ConnLimit: &limit, ValidUntil: &until}, "secret")
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range []string{" WITH LOGIN NOCREATEDB CONNECTION LIMIT 5 VALID UNTIL '2027-01-31T00:00:00Z' PASSWORD 'SCRAM-SHA-256$4096:"} {
		if !strings.HasPrefix(o, w) {
			t.Errorf("options %q, want prefix %q", o, w)
		}
	}
	if strings.Contains(o, "secret") {
		t.Error("the plain password reached the statement")
	}
	if o, _ := roleOptions(PgRoleInput{}, ""); o != "" {
		t.Errorf("no changes rendered %q", o)
	}
	bad := "tomorrow"
	if _, err := roleOptions(PgRoleInput{ValidUntil: &bad}, ""); err == nil {
		t.Error("a bad time was accepted")
	}
	if err := checkMemberships([]PgMembership{{Role: "pg_read_server_files"}}); err == nil {
		t.Error("file access membership was allowed")
	}
	if err := checkMemberships([]PgMembership{{Role: pgSuperuser}}); err == nil {
		t.Error("superuser membership was allowed")
	}
}

func TestScramVerifier(t *testing.T) {
	v, err := scramVerifier("pw")
	if err != nil {
		t.Fatal(err)
	}
	// SCRAM-SHA-256$<iter>:<salt>$<StoredKey>:<ServerKey>
	head, keys, ok := strings.Cut(strings.TrimPrefix(v, "SCRAM-SHA-256$"), "$")
	if !ok || !strings.HasPrefix(head, "4096:") {
		t.Fatalf("verifier %q", v)
	}
	for _, k := range strings.Split(keys, ":") {
		if b, err := base64.StdEncoding.DecodeString(k); err != nil || len(b) != 32 {
			t.Errorf("key %q", k)
		}
	}
	if w, _ := scramVerifier("pw"); w == v {
		t.Error("salts repeat")
	}
}

func TestPrivilegeSQL(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		ch   PgPrivilegeChange
		want string
	}{
		{PgPrivilegeChange{Privileges: []string{"select", "INSERT"}, Grantee: "ro", Target: "all-tables", Object: PgObjectRef{Schema: "public"}},
			`GRANT SELECT, INSERT ON ALL TABLES IN SCHEMA "public" TO "ro"`},
		{PgPrivilegeChange{Revoke: true, Privileges: []string{"ALL"}, Grantee: "public", Target: "all-sequences", Object: PgObjectRef{Schema: "s"}},
			`REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA "s" FROM PUBLIC`},
		{PgPrivilegeChange{Privileges: []string{"SELECT"}, Grantee: "ro", GrantOption: true, Target: "default-tables", Object: PgObjectRef{Schema: "public"}, ForRole: "app"},
			`ALTER DEFAULT PRIVILEGES FOR ROLE "app" IN SCHEMA "public" GRANT SELECT ON TABLES TO "ro" WITH GRANT OPTION`},
	}
	for _, c := range cases {
		got, err := privilegeSQL(ctx, nil, c.ch)
		if err != nil || got != c.want {
			t.Errorf("got %q (%v), want %q", got, err, c.want)
		}
	}
	for _, bad := range []PgPrivilegeChange{
		{Privileges: []string{"EXECUTE"}, Grantee: "ro", Target: "all-tables", Object: PgObjectRef{Schema: "public"}},
		{Privileges: []string{"SELECT"}, Grantee: pgSuperuser, Target: "all-tables", Object: PgObjectRef{Schema: "public"}},
		{Privileges: []string{"SELECT"}, Grantee: "ro", Target: "default-tables", Object: PgObjectRef{Schema: "public"}},
		{Privileges: []string{"SELECT; DROP TABLE x"}, Grantee: "ro", Target: "all-tables", Object: PgObjectRef{Schema: "public"}},
		{Privileges: []string{"SELECT"}, Grantee: "ro", Target: "everything"},
	} {
		if s, err := privilegeSQL(ctx, nil, bad); err == nil {
			t.Errorf("accepted %+v as %q", bad, s)
		}
	}
}
