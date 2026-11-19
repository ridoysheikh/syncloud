package cli

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/ridoysheikh/syncloud/internal/client"
	"github.com/spf13/cobra"
)

// Domains, DNS providers and records (Phase 28).

type domainRow struct {
	Name             string   `json:"name"`
	Mode             string   `json:"mode"`
	Provider         string   `json:"provider"`
	NameServers      []string `json:"nameServers"`
	WildcardPolicy   string   `json:"wildcardPolicy"`
	ChallengeZone    string   `json:"challengeZone"`
	ChallengeBuiltin bool     `json:"challengeBuiltin"`
	Grants           []struct {
		Project   string `json:"project"`
		Suffix    string `json:"suffix"`
		Wildcards bool   `json:"wildcards"`
	} `json:"grants"`
	Use struct {
		Services     int `json:"services"`
		Certificates int `json:"certificates"`
	} `json:"use"`
}

type recordRow struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Name    string `json:"name"`
	Content string `json:"content"`
	TTL     int    `json:"ttl"`
	Proxied bool   `json:"proxied"`
	Managed bool   `json:"managed"`
}

func grantsText(d domainRow) string {
	var parts []string
	for _, g := range d.Grants {
		s := g.Project
		if g.Project == "*" {
			s = "all projects"
		}
		if g.Suffix != d.Name {
			s += " (" + g.Suffix + ")"
		}
		if !g.Wildcards {
			s += " [no wildcards]"
		}
		parts = append(parts, s)
	}
	return joinOrDash(parts)
}

func (a *app) domainsCmd() *cobra.Command {
	dc := &cobra.Command{Use: "domains", Aliases: []string{"zones"}, Short: "Registered domains: DNS providers, who may use them, wildcard certificates"}
	var q, provider string
	list := &cobra.Command{
		Use: "list", Aliases: []string{"ls"}, Short: "List registered domains", Args: cobra.NoArgs, Annotations: op("listDomains"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			var out struct {
				Items []domainRow `json:"items"`
			}
			p := url.Values{}
			if q != "" {
				p.Set("q", q)
			}
			if provider != "" {
				p.Set("provider", provider)
			}
			if err := a.do(cmd, "GET", "/api/v1/domains?"+p.Encode(), nil, &out); err != nil {
				return err
			}
			rows := make([][]string, 0, len(out.Items))
			for _, d := range out.Items {
				rows = append(rows, []string{d.Name, d.Mode, orDash(d.Provider), d.WildcardPolicy, strconv.Itoa(d.Use.Services), strconv.Itoa(d.Use.Certificates), grantsText(d)})
			}
			return a.printer().table(out.Items, []string{"DOMAIN", "MODE", "PROVIDER", "WILDCARD CERT", "SERVICES", "CERTS", "USABLE BY"}, rows)
		},
	}
	list.Flags().StringVar(&q, "q", "", "name contains")
	list.Flags().StringVar(&provider, "provider", "", "only this provider's domains (id)")

	var prov, authority, addChallengeZone string
	var addBuiltin bool
	var projects []string
	var wildcardCert, noWildcards bool
	add := &cobra.Command{
		Use: "add DOMAIN", Short: "Register a domain; with --provider SynCloud edits its records, without it SynCloud shows the records to create", Args: cobra.ExactArgs(1),
		Example: `  synctl domains add example.com --provider dnsp_ab12            # managed by Cloudflare
  synctl domains add example.org                                 # manual: records are shown, not written
  synctl domains add example.com --provider dnsp_ab12 --project shop --project blog`,
		Annotations: op("addDomain"),
		RunE: func(cmd *cobra.Command, args []string) error {
			body := map[string]any{"name": args[0], "provider": prov, "authority": authority}
			if addChallengeZone != "" {
				body["challengeZone"] = addChallengeZone
			}
			if addBuiltin {
				body["challengeBuiltin"] = true
			}
			if wildcardCert {
				body["wildcardPolicy"] = "on"
			}
			if len(projects) > 0 {
				var gs []map[string]any
				for _, p := range projects {
					gs = append(gs, map[string]any{"project": p, "wildcards": !noWildcards})
				}
				body["grants"] = gs
			}
			var d domainRow
			if err := a.do(cmd, "POST", "/api/v1/domains", body, &d); err != nil {
				return err
			}
			if a.output == "json" {
				return a.printer().json(d)
			}
			fmt.Fprintf(a.out, "Registered %s (%s), usable by %s\n", d.Name, d.Mode, grantsText(d))
			return nil
		},
	}
	add.Flags().StringVar(&prov, "provider", "", "DNS provider id (synctl dns providers list)")
	add.Flags().StringVar(&authority, "authority", "", "the domain's default certificate authority")
	add.Flags().BoolVar(&addBuiltin, "builtin-challenge", false, "answer _acme-challenge with SynCloud's own challenge server (a domain without a provider does so anyway while it is on)")
	add.Flags().StringVar(&addChallengeZone, "challenge-zone", "", "delegate _acme-challenge to this registered domain (one CNAME, then certificates renew by themselves)")
	add.Flags().StringArrayVar(&projects, "project", nil, "only this project may use it (repeatable; default: all projects)")
	add.Flags().BoolVar(&noWildcards, "no-wildcards", false, "with --project: its hosts may not be wildcards")
	add.Flags().BoolVar(&wildcardCert, "wildcard-cert", false, "keep one wildcard certificate (the domain and *.domain) for it")

	get := &cobra.Command{
		Use: "get DOMAIN", Short: "Show a domain with its name servers, grants and what uses it", Args: cobra.ExactArgs(1), Annotations: op("getDomain"),
		RunE: func(cmd *cobra.Command, args []string) error {
			var d domainRow
			if err := a.do(cmd, "GET", "/api/v1/domains/"+url.PathEscape(args[0]), nil, &d); err != nil {
				return err
			}
			if a.output == "json" {
				return a.printer().json(d)
			}
			fmt.Fprintf(a.out, "Domain:        %s\nMode:          %s\nProvider:      %s\nName servers:  %s\nWildcard cert: %s\nUsable by:     %s\nUsed by:       %d service domains, %d certificates\n",
				d.Name, d.Mode, orDash(d.Provider), joinOrDash(d.NameServers), d.WildcardPolicy, grantsText(d), d.Use.Services, d.Use.Certificates)
			return nil
		},
	}

	var setProvider, setWildcard, setAuthority, setChallengeZone string
	var setBuiltin bool
	var setProjects []string
	var setNoWildcards, allProjects bool
	update := &cobra.Command{
		Use: "update DOMAIN", Short: "Change a domain's provider, wildcard certificate policy, default authority or who may use it", Args: cobra.ExactArgs(1),
		Annotations: op("updateDomain"),
		RunE: func(cmd *cobra.Command, args []string) error {
			body := map[string]any{}
			if cmd.Flags().Changed("provider") {
				body["provider"] = setProvider
			}
			if cmd.Flags().Changed("wildcard-cert") {
				body["wildcardPolicy"] = setWildcard
			}
			if cmd.Flags().Changed("authority") {
				body["authority"] = setAuthority
			}
			if cmd.Flags().Changed("challenge-zone") {
				body["challengeZone"] = setChallengeZone
			}
			if cmd.Flags().Changed("builtin-challenge") {
				body["challengeBuiltin"] = setBuiltin
			}
			if allProjects || len(setProjects) > 0 {
				gs := []map[string]any{}
				if allProjects {
					gs = append(gs, map[string]any{"project": "*", "wildcards": !setNoWildcards})
				}
				for _, p := range setProjects {
					gs = append(gs, map[string]any{"project": p, "wildcards": !setNoWildcards})
				}
				body["grants"] = gs
			}
			var d domainRow
			if err := a.do(cmd, "PUT", "/api/v1/domains/"+url.PathEscape(args[0]), body, &d); err != nil {
				return err
			}
			if a.output == "json" {
				return a.printer().json(d)
			}
			fmt.Fprintf(a.out, "Updated %s: %s, usable by %s\n", d.Name, d.Mode, grantsText(d))
			return nil
		},
	}
	update.Flags().StringVar(&setProvider, "provider", "", "DNS provider id ('' disconnects)")
	update.Flags().StringVar(&setWildcard, "wildcard-cert", "", "on or off")
	update.Flags().BoolVar(&setBuiltin, "builtin-challenge", false, "answer _acme-challenge with SynCloud's own challenge server (--builtin-challenge=false stops)")
	update.Flags().StringVar(&setChallengeZone, "challenge-zone", "", "delegate _acme-challenge to this registered domain ('' stops delegating)")
	update.Flags().StringVar(&setAuthority, "authority", "", "default certificate authority ('' = the cluster's)")
	update.Flags().StringArrayVar(&setProjects, "project", nil, "replace who may use it with these projects (repeatable)")
	update.Flags().BoolVar(&allProjects, "all-projects", false, "let all projects use it")
	update.Flags().BoolVar(&setNoWildcards, "no-wildcards", false, "granted hosts may not be wildcards")

	var force bool
	remove := &cobra.Command{
		Use: "remove DOMAIN", Aliases: []string{"rm"}, Short: "Stop managing a domain (nothing is deleted at the provider)", Args: cobra.ExactArgs(1), Annotations: op("removeDomain"),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "/api/v1/domains/" + url.PathEscape(args[0])
			if force {
				path += "?force=true"
			}
			if err := a.do(cmd, "DELETE", path, nil, nil); err != nil {
				return err
			}
			fmt.Fprintf(a.out, "Removed %s\n", args[0])
			return nil
		},
	}
	remove.Flags().BoolVar(&force, "force", false, "remove it although services or certificates use it")
	dc.AddCommand(list, add, get, update, remove)
	return dc
}

func (a *app) dnsCmd() *cobra.Command {
	dc := &cobra.Command{Use: "dns", Short: "DNS providers, the records of registered domains, and lookups"}

	// ── providers ──
	pc := &cobra.Command{Use: "providers", Aliases: []string{"provider"}, Short: "DNS providers (Cloudflare) SynCloud writes records through"}
	type prov struct {
		ID, Name, Type, Status, LastError string
		Domains                           int
	}
	pc.AddCommand(&cobra.Command{
		Use: "list", Aliases: []string{"ls"}, Short: "List connected providers", Args: cobra.NoArgs, Annotations: op("listDnsProviders"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			var out struct {
				Items []prov `json:"items"`
			}
			if err := a.do(cmd, "GET", "/api/v1/dns/providers", nil, &out); err != nil {
				return err
			}
			rows := make([][]string, 0, len(out.Items))
			for _, p := range out.Items {
				rows = append(rows, []string{p.ID, p.Name, p.Type, p.Status, strconv.Itoa(p.Domains), orDash(p.LastError)})
			}
			return a.printer().table(out.Items, []string{"ID", "NAME", "TYPE", "STATUS", "DOMAINS", "LAST ERROR"}, rows)
		},
	})
	pc.AddCommand(&cobra.Command{
		Use: "get ID", Short: "Show a provider (its token is never shown)", Args: cobra.ExactArgs(1), Annotations: op("getDnsProvider"),
		RunE: func(cmd *cobra.Command, args []string) error {
			var p prov
			if err := a.do(cmd, "GET", "/api/v1/dns/providers/"+url.PathEscape(args[0]), nil, &p); err != nil {
				return err
			}
			if a.output == "json" {
				return a.printer().json(p)
			}
			fmt.Fprintf(a.out, "Provider: %s (%s)\nType:     %s\nStatus:   %s %s\nDomains:  %d\n", p.Name, p.ID, p.Type, p.Status, p.LastError, p.Domains)
			return nil
		},
	})
	var ptype, token string
	padd := &cobra.Command{
		Use: "add NAME", Short: "Connect a provider with an API token (Cloudflare: Zone:Zone:Read and Zone:DNS:Edit on the chosen zones)", Args: cobra.ExactArgs(1),
		Annotations: op("createDnsProvider"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if token == "" {
				return fmt.Errorf("--token is required")
			}
			var p prov
			if err := a.do(cmd, "POST", "/api/v1/dns/providers", map[string]string{"name": args[0], "type": ptype, "token": token}, &p); err != nil {
				return err
			}
			if a.output == "json" {
				return a.printer().json(p)
			}
			fmt.Fprintf(a.out, "Connected %s (%s). Register its domains with: synctl domains add DOMAIN --provider %s\n", p.Name, p.ID, p.ID)
			return nil
		},
	}
	padd.Flags().StringVar(&ptype, "type", "cloudflare", "provider type")
	padd.Flags().StringVar(&token, "token", "", "the provider's API token")
	var newToken string
	pupd := &cobra.Command{
		Use: "update ID", Short: "Replace a provider's token", Args: cobra.ExactArgs(1), Annotations: op("updateDnsProvider"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if newToken == "" {
				return fmt.Errorf("--token is required")
			}
			var p prov
			if err := a.do(cmd, "PUT", "/api/v1/dns/providers/"+url.PathEscape(args[0]), map[string]string{"token": newToken}, &p); err != nil {
				return err
			}
			fmt.Fprintf(a.out, "Replaced the token of %s\n", p.Name)
			return nil
		},
	}
	pupd.Flags().StringVar(&newToken, "token", "", "the new API token")
	pc.AddCommand(padd, pupd,
		&cobra.Command{
			Use: "remove ID", Aliases: []string{"rm"}, Short: "Disconnect a provider (its domains stay, as manual ones)", Args: cobra.ExactArgs(1), Annotations: op("deleteDnsProvider"),
			RunE: func(cmd *cobra.Command, args []string) error {
				if err := a.do(cmd, "DELETE", "/api/v1/dns/providers/"+url.PathEscape(args[0]), nil, nil); err != nil {
					return err
				}
				fmt.Fprintf(a.out, "Disconnected %s\n", args[0])
				return nil
			},
		},
		&cobra.Command{
			Use: "sync ID", Short: "Check the provider's token again", Args: cobra.ExactArgs(1), Annotations: op("syncDnsProvider"),
			RunE: func(cmd *cobra.Command, args []string) error {
				var p prov
				if err := a.do(cmd, "POST", "/api/v1/dns/providers/"+url.PathEscape(args[0])+"/sync", nil, &p); err != nil {
					return err
				}
				fmt.Fprintf(a.out, "%s: %s %s\n", p.Name, p.Status, p.LastError)
				return nil
			},
		},
		&cobra.Command{
			Use: "zones ID", Short: "The zones a provider's token can manage", Args: cobra.ExactArgs(1), Annotations: op("listDnsProviderZones"),
			RunE: func(cmd *cobra.Command, args []string) error {
				var out struct {
					Items []struct {
						Name       string   `json:"name"`
						Status     string   `json:"status"`
						NS         []string `json:"nameServers"`
						Registered bool     `json:"registered"`
					} `json:"items"`
				}
				if err := a.do(cmd, "GET", "/api/v1/dns/providers/"+url.PathEscape(args[0])+"/zones", nil, &out); err != nil {
					return err
				}
				rows := make([][]string, 0, len(out.Items))
				for _, z := range out.Items {
					rows = append(rows, []string{z.Name, orDash(z.Status), strconv.FormatBool(z.Registered), joinOrDash(z.NS)})
				}
				return a.printer().table(out.Items, []string{"ZONE", "STATUS", "REGISTERED", "NAME SERVERS"}, rows)
			},
		},
	)

	// ── records ──
	rc := &cobra.Command{Use: "records", Aliases: []string{"record"}, Short: "The DNS records of a registered domain"}
	var rtype, rname, rq string
	rlist := &cobra.Command{
		Use: "list DOMAIN", Aliases: []string{"ls"}, Short: "List a domain's records", Args: cobra.ExactArgs(1), Annotations: op("listDomainRecords"),
		RunE: func(cmd *cobra.Command, args []string) error {
			p := url.Values{}
			for k, v := range map[string]string{"type": rtype, "name": rname, "q": rq} {
				if v != "" {
					p.Set(k, v)
				}
			}
			var out struct {
				Items []recordRow `json:"items"`
			}
			if err := a.do(cmd, "GET", "/api/v1/domains/"+url.PathEscape(args[0])+"/records?"+p.Encode(), nil, &out); err != nil {
				return err
			}
			rows := make([][]string, 0, len(out.Items))
			for _, r := range out.Items {
				note := ""
				if r.Proxied {
					note = "proxied"
				}
				if r.Managed {
					note = strings.TrimSpace(note + " syncloud")
				}
				rows = append(rows, []string{r.ID, r.Type, r.Name, r.Content, strconv.Itoa(r.TTL), note})
			}
			return a.printer().table(out.Items, []string{"ID", "TYPE", "NAME", "CONTENT", "TTL", ""}, rows)
		},
	}
	rlist.Flags().StringVar(&rtype, "type", "", "only this type")
	rlist.Flags().StringVar(&rname, "name", "", "only this exact name")
	rlist.Flags().StringVar(&rq, "q", "", "name or content contains")

	var ttl int
	var proxied bool
	var prio int
	body := func(typ, name, content string) map[string]any {
		b := map[string]any{"type": strings.ToUpper(typ), "name": name, "content": content, "ttl": ttl, "proxied": proxied}
		if prio > 0 {
			b["priority"] = prio
		}
		return b
	}
	radd := &cobra.Command{
		Use: "add DOMAIN TYPE NAME CONTENT", Short: "Create a record (NAME may be a label, the full name, or @)", Args: cobra.ExactArgs(4),
		Example: `  synctl dns records add example.com A app 203.0.113.10
  synctl dns records add example.com TXT _acme-challenge.apps "token-value"`,
		Annotations: op("createDomainRecord"),
		RunE: func(cmd *cobra.Command, args []string) error {
			var r recordRow
			if err := a.do(cmd, "POST", "/api/v1/domains/"+url.PathEscape(args[0])+"/records", body(args[1], args[2], args[3]), &r); err != nil {
				return err
			}
			if a.output == "json" {
				return a.printer().json(r)
			}
			fmt.Fprintf(a.out, "Created %s %s %s (%s)\n", r.Type, r.Name, r.Content, r.ID)
			return nil
		},
	}
	rupd := &cobra.Command{
		Use: "update DOMAIN ID TYPE NAME CONTENT", Short: "Change a record", Args: cobra.ExactArgs(5), Annotations: op("updateDomainRecord"),
		RunE: func(cmd *cobra.Command, args []string) error {
			var r recordRow
			if err := a.do(cmd, "PUT", "/api/v1/domains/"+url.PathEscape(args[0])+"/records/"+url.PathEscape(args[1]), body(args[2], args[3], args[4]), &r); err != nil {
				return err
			}
			fmt.Fprintf(a.out, "Updated %s %s %s\n", r.Type, r.Name, r.Content)
			return nil
		},
	}
	for _, c := range []*cobra.Command{radd, rupd} {
		c.Flags().IntVar(&ttl, "ttl", 0, "seconds (default automatic)")
		c.Flags().BoolVar(&proxied, "proxied", false, "proxy through the provider (A, AAAA, CNAME)")
		c.Flags().IntVar(&prio, "priority", 0, "MX and SRV priority")
	}
	var rforce bool
	rdel := &cobra.Command{
		Use: "remove DOMAIN ID", Aliases: []string{"rm"}, Short: "Delete a record", Args: cobra.ExactArgs(2), Annotations: op("deleteDomainRecord"),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "/api/v1/domains/" + url.PathEscape(args[0]) + "/records/" + url.PathEscape(args[1])
			if rforce {
				path += "?force=true"
			}
			if err := a.do(cmd, "DELETE", path, nil, nil); err != nil {
				return err
			}
			fmt.Fprintf(a.out, "Deleted record %s\n", args[1])
			return nil
		},
	}
	rdel.Flags().BoolVar(&rforce, "force", false, "remove a record a service address depends on")
	rc.AddCommand(rlist, radd, rupd, rdel)

	// ── point, cleanup, lookup ──
	var ptargets []string
	var pproject string
	var pplan, preplace bool
	var pproxied bool
	point := &cobra.Command{
		Use: "point DOMAIN NAME", Short: "Make NAME reach the cluster: reconcile its A/AAAA records, with no duplicates", Args: cobra.ExactArgs(2),
		Long: `Creates the records NAME needs, keeps the ones that are right, collapses exact duplicates and removes
the records SynCloud created that are no longer needed. A record it did not create that is in the way is only
listed (use --replace to remove it). --plan shows what would change. Without a provider the records to create
by hand are printed.`,
		Annotations: op("pointDomain"),
		RunE: func(cmd *cobra.Command, args []string) error {
			q := url.Values{}
			if pplan {
				q.Set("plan", "true")
			}
			if preplace {
				q.Set("replace", "conflicts")
			}
			b := map[string]any{"name": args[1], "targets": ptargets, "project": pproject}
			if cmd.Flags().Changed("proxied") {
				b["proxied"] = pproxied
			}
			var out struct {
				Plan struct {
					Steps []struct {
						Action, Reason, Detail string
						NeedsConfirm           bool
						Record                 recordRow
					} `json:"steps"`
				} `json:"plan"`
				Applied *struct{ Created, Updated, Deleted int } `json:"applied"`
				Manual  []recordRow                              `json:"manual"`
			}
			if err := a.do(cmd, "POST", "/api/v1/domains/"+url.PathEscape(args[0])+"/records/point?"+q.Encode(), b, &out); err != nil {
				return err
			}
			if a.output == "json" {
				return a.printer().json(out)
			}
			for _, m := range out.Manual {
				fmt.Fprintf(a.out, "create at your DNS host: %s %s %s\n", m.Name, m.Type, m.Content)
			}
			for _, s := range out.Plan.Steps {
				note := s.Reason
				if s.NeedsConfirm {
					note += ", needs --replace"
				}
				fmt.Fprintf(a.out, "%-7s %-5s %s %s (%s)\n", s.Action, s.Record.Type, s.Record.Name, s.Record.Content, note)
			}
			if out.Applied != nil {
				fmt.Fprintf(a.out, "created %d, updated %d, removed %d\n", out.Applied.Created, out.Applied.Updated, out.Applied.Deleted)
			}
			return nil
		},
	}
	point.Flags().StringArrayVar(&ptargets, "target", nil, "an IP address to point at (repeatable; default: the project's load balancers, else this controller)")
	point.Flags().StringVar(&pproject, "project", "", "point at this project's load balancers")
	point.Flags().BoolVar(&pplan, "plan", false, "only show what would change")
	point.Flags().BoolVar(&preplace, "replace", false, "also remove records SynCloud did not create that are in the way")
	point.Flags().BoolVar(&pproxied, "proxied", false, "proxy through the provider")

	var apply bool
	cleanup := &cobra.Command{
		Use: "cleanup DOMAIN", Short: "Find exact duplicate records and leftover _acme-challenge records", Args: cobra.ExactArgs(1), Annotations: op("cleanupDomainRecords"),
		RunE: func(cmd *cobra.Command, args []string) error {
			var out struct {
				Duplicates []recordRow `json:"duplicates"`
				Challenges []recordRow `json:"challenges"`
				Removed    int         `json:"removed"`
			}
			path := "/api/v1/domains/" + url.PathEscape(args[0]) + "/records/cleanup"
			if apply {
				path += "?apply=true"
			}
			if err := a.do(cmd, "POST", path, nil, &out); err != nil {
				return err
			}
			if a.output == "json" {
				return a.printer().json(out)
			}
			for _, r := range out.Duplicates {
				fmt.Fprintf(a.out, "duplicate  %s %s %s\n", r.Type, r.Name, r.Content)
			}
			for _, r := range out.Challenges {
				fmt.Fprintf(a.out, "challenge  %s %s %s\n", r.Type, r.Name, r.Content)
			}
			if apply {
				fmt.Fprintf(a.out, "removed %d\n", out.Removed)
			} else if len(out.Duplicates)+len(out.Challenges) > 0 {
				fmt.Fprintln(a.out, "run again with --apply to remove the duplicates and the challenge records SynCloud created")
			}
			return nil
		},
	}
	cleanup.Flags().BoolVar(&apply, "apply", false, "remove what was found")

	var via string
	lookup := &cobra.Command{
		Use: "lookup NAME [TYPE]", Short: "Ask public resolvers and the zone's own servers for a name", Args: cobra.RangeArgs(1, 2), Annotations: op("queryDns"),
		RunE: func(cmd *cobra.Command, args []string) error {
			t := "A"
			if len(args) == 2 {
				t = strings.ToUpper(args[1])
			}
			var out struct {
				Answers []struct {
					Server, Kind, Rcode, Error string
					Values                     []string
					TTL                        int
				} `json:"answers"`
			}
			q := url.Values{"name": {args[0]}, "type": {t}}
			if via != "" {
				q.Set("via", via)
			}
			if err := a.do(cmd, "GET", "/api/v1/dns/lookup?"+q.Encode(), nil, &out); err != nil {
				return err
			}
			rows := make([][]string, 0, len(out.Answers))
			for _, x := range out.Answers {
				v := joinOrDash(x.Values)
				if x.Error != "" {
					v = "error: " + x.Error
				}
				rows = append(rows, []string{x.Kind, x.Server, orDash(x.Rcode), v})
			}
			return a.printer().table(out.Answers, []string{"FROM", "SERVER", "RESULT", "ANSWER"}, rows)
		},
	}
	lookup.Flags().StringVar(&via, "via", "", "public or authoritative (default both)")

	var chName string
	challenge := &cobra.Command{
		Use: "challenge DOMAIN", Short: "Where the _acme-challenge TXT record of a name goes, and whether the delegating CNAME is in place", Args: cobra.ExactArgs(1),
		Example: `  synctl domains challenge example.com
  synctl domains challenge example.com --name '*.shop.example.com'`,
		Annotations: op("getDomainChallenge"),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "/api/v1/domains/" + url.PathEscape(args[0]) + "/challenge"
			if chName != "" {
				path += "?name=" + url.QueryEscape(chName)
			}
			var r client.ChallengeRoute
			if err := a.do(cmd, "GET", path, nil, &r); err != nil {
				return err
			}
			return printChallenge(a, r)
		},
	}
	challenge.Flags().StringVar(&chName, "name", "", "a name under the domain, or *.name (default: the domain)")

	var dName string
	var dReplace bool
	delegate := &cobra.Command{
		Use: "delegate DOMAIN", Short: "Create the CNAME that delegates a name's _acme-challenge to the domain's validation domain", Args: cobra.ExactArgs(1),
		Long:        "Needs --challenge-zone set on the domain (synctl domains update DOMAIN --challenge-zone OTHER). When the domain has a provider SynCloud creates the CNAME; otherwise it shows the one record to create at your DNS host.",
		Annotations: op("delegateDomainChallenge"),
		RunE: func(cmd *cobra.Command, args []string) error {
			q := url.Values{}
			if dName != "" {
				q.Set("name", dName)
			}
			if dReplace {
				q.Set("replace", "conflicts")
			}
			var r client.ChallengeRoute
			if err := a.do(cmd, "POST", "/api/v1/domains/"+url.PathEscape(args[0])+"/challenge/delegation?"+q.Encode(), nil, &r); err != nil {
				return err
			}
			return printChallenge(a, r)
		},
	}
	delegate.Flags().StringVar(&dName, "name", "", "a name under the domain, or *.name (default: the domain)")
	delegate.Flags().BoolVar(&dReplace, "replace", false, "also remove records SynCloud did not create that are in the way")

	dc.AddCommand(pc, rc, point, cleanup, lookup, challenge, delegate, a.challengeServerCmd())
	return dc
}

func printChallenge(a *app, r client.ChallengeRoute) error {
	if a.output == "json" {
		return a.printer().json(r)
	}
	fmt.Fprintf(a.out, "Challenge:  %s\nMode:       %s\n", r.Name, r.Mode)
	switch r.Mode {
	case "direct":
		fmt.Fprintf(a.out, "SynCloud writes the TXT record at %s and removes it afterwards.\n", r.Name)
	case "builtin":
		state := "not visible yet"
		if r.CNAME != nil && r.CNAME.Present {
			state = "in place"
		} else if r.CNAME != nil && len(r.CNAME.Seen) > 0 {
			state = "points elsewhere: " + strings.Join(r.CNAME.Seen, ", ")
		}
		fmt.Fprintf(a.out, "CNAME:      %s -> %s (%s)\nSynCloud's own challenge server answers the TXT record at %s.\n", r.Name, r.Target, state, r.Target)
		if r.CNAME != nil && !r.CNAME.Present && !r.CNAME.Managed {
			fmt.Fprintf(a.out, "Create the CNAME once at your DNS host; every renewal after that needs nobody.\n")
		}
	case "delegated":
		state := "not visible yet"
		if r.CNAME != nil && r.CNAME.Present {
			state = "in place"
		} else if r.CNAME != nil && len(r.CNAME.Seen) > 0 {
			state = "points elsewhere: " + strings.Join(r.CNAME.Seen, ", ")
		}
		fmt.Fprintf(a.out, "CNAME:      %s -> %s (%s)\nSynCloud writes the TXT record at %s.\n", r.Name, r.Target, state, r.Target)
		if r.CNAME != nil && !r.CNAME.Present && !r.CNAME.Managed {
			fmt.Fprintf(a.out, "Create the CNAME once at your DNS host; every renewal after that needs nothing.\n")
		}
	default:
		fmt.Fprintf(a.out, "Add the TXT record at your DNS host when a certificate order asks for it.\n")
	}
	if r.Note != "" {
		fmt.Fprintf(a.out, "Note:       %s\n", r.Note)
	}
	return nil
}

type challengeServerRow struct {
	Enabled   bool   `json:"enabled"`
	Zone      string `json:"zone"`
	NS        string `json:"ns"`
	IP        string `json:"ip"`
	Listen    string `json:"listen"`
	Listening bool   `json:"listening"`
	Error     string `json:"error"`
	Records   int    `json:"records"`
	Needed    []struct {
		Type    string `json:"type"`
		Name    string `json:"name"`
		Content string `json:"content"`
		Zone    string `json:"zone"`
		Managed bool   `json:"managed"`
	} `json:"needed"`
	Domains []string `json:"domains"`
}

func printChallengeServer(a *app, v challengeServerRow) error {
	if a.output == "json" {
		return a.printer().json(v)
	}
	if !v.Enabled {
		fmt.Fprintln(a.out, "SynCloud's challenge server is off. Set a zone under one of your domains:\n  synctl domains challenge-server set acme.example.com")
		return nil
	}
	state := "listening on " + v.Listen
	if !v.Listening {
		state = "NOT listening on " + v.Listen + ": " + orDash(v.Error)
	}
	fmt.Fprintf(a.out, "Zone:         %s\nName server:  %s (%s)\nServer:       %s\nDomains:      %s\n", v.Zone, v.NS, v.IP, state, joinOrDash(v.Domains))
	fmt.Fprintln(a.out, "Create these records at the DNS of the domain the zone is under:")
	for _, n := range v.Needed {
		how := "at your DNS host"
		if n.Managed {
			how = "SynCloud can create it (synctl domains challenge-server records)"
		}
		fmt.Fprintf(a.out, "  %-3s %s -> %s  (%s)\n", n.Type, n.Name, n.Content, how)
	}
	return nil
}

// challengeServerCmd is SynCloud's own ACME challenge DNS server.
func (a *app) challengeServerCmd() *cobra.Command {
	cs := &cobra.Command{Use: "challenge-server", Short: "SynCloud's own ACME challenge DNS server: any domain renews wildcard certificates with one CNAME and no provider"}
	cs.AddCommand(&cobra.Command{
		Use: "show", Short: "Show the server, its zone and the records to create", Args: cobra.NoArgs, Annotations: op("getChallengeServer"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			var v challengeServerRow
			if err := a.do(cmd, "GET", "/api/v1/domains/challenge-server", nil, &v); err != nil {
				return err
			}
			return printChallengeServer(a, v)
		},
	})
	var ns, ip string
	var off bool
	set := &cobra.Command{
		Use: "set [ZONE]", Short: "Turn the server on for a zone below a domain you own (--off switches it off)", Args: cobra.RangeArgs(0, 1),
		Long: "The zone is a name like acme.example.com. The server answers TXT records for that zone only; it is not a general name server. Create the A and NS records it lists (or run 'records'), then 'check'.",
		Example: `  synctl domains challenge-server set acme.example.com
  synctl domains challenge-server set acme.example.com --ns acme-ns.example.com --ip 203.0.113.9
  synctl domains challenge-server set --off`,
		Annotations: op("updateChallengeServer"),
		RunE: func(cmd *cobra.Command, args []string) error {
			zone := ""
			if len(args) == 1 {
				zone = args[0]
			}
			if zone == "" && !off {
				return fmt.Errorf("give the zone (acme.example.com) or --off")
			}
			var v challengeServerRow
			if err := a.do(cmd, "PUT", "/api/v1/domains/challenge-server", map[string]any{"zone": zone, "ns": ns, "ip": ip}, &v); err != nil {
				return err
			}
			return printChallengeServer(a, v)
		},
	}
	set.Flags().StringVar(&ns, "ns", "", "the name server host (default: <first label>-ns.<rest of the zone>)")
	set.Flags().StringVar(&ip, "ip", "", "this controller's public address (default: detected)")
	set.Flags().BoolVar(&off, "off", false, "switch the server off")
	var replace bool
	records := &cobra.Command{
		Use: "records", Short: "Create the A and NS records in the domains that have a provider", Args: cobra.NoArgs, Annotations: op("createChallengeServerRecords"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			path := "/api/v1/domains/challenge-server/records"
			if replace {
				path += "?replace=conflicts"
			}
			var out struct {
				Created int                `json:"created"`
				Server  challengeServerRow `json:"server"`
			}
			if err := a.do(cmd, "POST", path, nil, &out); err != nil {
				return err
			}
			if a.output == "json" {
				return a.printer().json(out)
			}
			fmt.Fprintf(a.out, "Created %d record(s).\n", out.Created)
			return printChallengeServer(a, out.Server)
		},
	}
	records.Flags().BoolVar(&replace, "replace", false, "also remove records SynCloud did not create that are in the way")
	cs.AddCommand(set, records, &cobra.Command{
		Use: "check", Short: "Test the server from the outside: NS record, address, and a probe through public resolvers", Args: cobra.NoArgs, Annotations: op("checkChallengeServer"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			var out struct {
				OK     bool `json:"ok"`
				Checks []struct {
					Name   string `json:"name"`
					OK     bool   `json:"ok"`
					Detail string `json:"detail"`
				} `json:"checks"`
			}
			if err := a.do(cmd, "POST", "/api/v1/domains/challenge-server/check", nil, &out); err != nil {
				return err
			}
			if a.output == "json" {
				return a.printer().json(out)
			}
			for _, c := range out.Checks {
				mark := "ok  "
				if !c.OK {
					mark = "FAIL"
				}
				fmt.Fprintf(a.out, "%s  %-28s %s\n", mark, c.Name, c.Detail)
			}
			if !out.OK {
				return fmt.Errorf("the challenge server is not reachable yet")
			}
			return nil
		},
	})
	return cs
}
