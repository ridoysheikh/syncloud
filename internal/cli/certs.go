package cli

import (
	"encoding/base64"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// Certificates a person asks for: authorities, wildcards, import, CSR (Phase 28).

type certRow struct {
	ID          string   `json:"id"`
	Host        string   `json:"host"`
	Names       []string `json:"names"`
	Status      string   `json:"status"`
	Source      string   `json:"source"`
	Method      string   `json:"method"`
	Managed     bool     `json:"managed"`
	Authority   string   `json:"authority"`
	KeyType     string   `json:"keyType"`
	AutoRenew   bool     `json:"autoRenew"`
	NotAfter    string   `json:"notAfter"`
	DaysLeft    int      `json:"daysLeft"`
	Fingerprint string   `json:"fingerprint"`
	Serial      string   `json:"serial"`
	IssuerName  string   `json:"issuerName"`
	LastError   string   `json:"lastError"`
	UsedBy      []string `json:"usedBy"`
	Pinned      []string `json:"pinnedHosts"`
	Order       *struct {
		State      string `json:"state"`
		Challenges []struct {
			Name    string `json:"name"`
			Value   string `json:"value"`
			Auto    bool   `json:"auto"`
			Present bool   `json:"present"`
			Mode    string `json:"mode"`
			Target  string `json:"target"`
			CNAMEOK bool   `json:"cnameOk"`
		} `json:"challenges"`
	} `json:"order"`
}

func printCert(a *app, c certRow) {
	fmt.Fprintf(a.out, "Certificate:  %s\nNames:        %s\nStatus:       %s %s\nSource:       %s (%s), authority %s\nKey:          %s, auto-renew %v\n",
		c.ID, strings.Join(c.Names, ", "), c.Status, c.LastError, c.Source, c.Method, orDash(c.Authority), orDash(c.KeyType), c.AutoRenew)
	if c.Fingerprint != "" {
		fmt.Fprintf(a.out, "Issued by:    %s\nExpires:      %s (%d days)\nSerial:       %s\nSHA-256:      %s\nServes:       %s\n", orDash(c.IssuerName), c.NotAfter, c.DaysLeft, c.Serial, c.Fingerprint, joinOrDash(c.UsedBy))
	}
	if c.Order != nil {
		fmt.Fprintf(a.out, "Order:        %s\n", c.Order.State)
		for _, ch := range c.Order.Challenges {
			who := "create this TXT record at your DNS host"
			if ch.Auto {
				who = "written by SynCloud"
			}
			seen := "not seen yet"
			if ch.Present {
				seen = "visible"
			}
			fmt.Fprintf(a.out, "  TXT %s = %q  (%s, %s)\n", ch.Name, ch.Value, seen, who)
			if ch.Mode == "delegated" && !ch.CNAMEOK {
				fmt.Fprintf(a.out, "  CNAME %s -> %s  (create this once at your DNS host; every later renewal needs nothing)\n", ch.Name, ch.Target)
			}
		}
	}
}

func readFile(path string) (string, error) {
	b, err := os.ReadFile(path)
	return string(b), err
}

func (a *app) certsExtra(cc *cobra.Command) {
	var names []string
	var authority, method, keyType, fallback string
	var noAuto bool
	var days int
	request := &cobra.Command{
		Use: "request NAME...", Short: "Ask for a certificate: several names, wildcards, another authority",
		Example: `  synctl certs request example.com '*.example.com'          # wildcard: DNS validation
  synctl certs request '*.sub.example.com'                  # its TXT record: _acme-challenge.sub.example.com
  synctl certs request app.example.com --authority ca_ab12 --key-type rsa-2048`,
		Args: cobra.MinimumNArgs(1), Annotations: op("requestCertificate"),
		RunE: func(cmd *cobra.Command, args []string) error {
			body := map[string]any{"names": args, "authority": authority, "method": method, "keyType": keyType, "fallback": fallback}
			if noAuto {
				body["autoRenew"] = false
			}
			if days > 0 {
				body["days"] = days
			}
			var c certRow
			if err := a.do(cmd, "POST", "/api/v1/certificates", body, &c); err != nil {
				return err
			}
			if a.output == "json" {
				return a.printer().json(c)
			}
			printCert(a, c)
			fmt.Fprintf(a.out, "\nFollow it with: synctl certs get %s   (a manual DNS order: add the TXT records, then synctl certs verify %s)\n", c.ID, c.ID)
			return nil
		},
	}
	request.Flags().StringVar(&authority, "authority", "", "certificate authority id (default: the domain's, else the cluster's)")
	request.Flags().StringVar(&method, "method", "", "http-01 or dns-01 (default: dns-01 for wildcards)")
	request.Flags().StringVar(&keyType, "key-type", "", "ecdsa-p256 (default), rsa-2048, rsa-3072 or rsa-4096")
	request.Flags().StringVar(&fallback, "fallback", "", "authority to try when the first one fails")
	request.Flags().BoolVar(&noAuto, "no-auto-renew", false, "do not renew by itself")
	request.Flags().IntVar(&days, "days", 0, "origin certificates: validity in days")
	_ = names

	get := &cobra.Command{
		Use: "get ID", Short: "Show a certificate: names, issuer, expiry, the hosts it serves and the order's TXT records", Args: cobra.ExactArgs(1), Annotations: op("getCertificate"),
		RunE: func(cmd *cobra.Command, args []string) error {
			var c certRow
			if err := a.do(cmd, "GET", "/api/v1/certificates/"+url.PathEscape(args[0]), nil, &c); err != nil {
				return err
			}
			if a.output == "json" {
				return a.printer().json(c)
			}
			printCert(a, c)
			return nil
		},
	}

	var autoRenew, setAuthority, setKeyType, setFallback, alertDays, pins string
	update := &cobra.Command{
		Use: "update ID", Short: "Change auto-renew, the authority or key type (from the next issuance), alert days or pinned hosts", Args: cobra.ExactArgs(1), Annotations: op("updateCertificate"),
		RunE: func(cmd *cobra.Command, args []string) error {
			b := map[string]any{}
			f := cmd.Flags()
			if f.Changed("auto-renew") {
				b["autoRenew"] = autoRenew == "on" || autoRenew == "true"
			}
			if f.Changed("authority") {
				b["authority"] = setAuthority
			}
			if f.Changed("key-type") {
				b["keyType"] = setKeyType
			}
			if f.Changed("fallback") {
				b["fallback"] = setFallback
			}
			if f.Changed("alert-days") {
				var ds []int
				for _, p := range strings.Split(alertDays, ",") {
					if p = strings.TrimSpace(p); p != "" {
						n, err := strconv.Atoi(p)
						if err != nil {
							return fmt.Errorf("--alert-days: %q is not a number", p)
						}
						ds = append(ds, n)
					}
				}
				b["alertDays"] = ds
			}
			if f.Changed("pin") {
				h := []string{}
				for _, p := range strings.Split(pins, ",") {
					if p = strings.TrimSpace(p); p != "" {
						h = append(h, p)
					}
				}
				b["pinnedHosts"] = h
			}
			var c certRow
			if err := a.do(cmd, "PUT", "/api/v1/certificates/"+url.PathEscape(args[0]), b, &c); err != nil {
				return err
			}
			fmt.Fprintf(a.out, "Updated %s\n", c.ID)
			return nil
		},
	}
	update.Flags().StringVar(&autoRenew, "auto-renew", "", "on or off")
	update.Flags().StringVar(&setAuthority, "authority", "", "authority id")
	update.Flags().StringVar(&setKeyType, "key-type", "", "key type")
	update.Flags().StringVar(&setFallback, "fallback", "", "fallback authority id ('' = the cluster's)")
	update.Flags().StringVar(&alertDays, "alert-days", "", "days before expiry to alert at, e.g. 30,14,7,1")
	update.Flags().StringVar(&pins, "pin", "", "hosts this certificate must serve, comma-separated ('' clears)")

	simple := func(use, short, oid, method, suffix string, expectBody bool) *cobra.Command {
		return &cobra.Command{
			Use: use, Short: short, Args: cobra.ExactArgs(1), Annotations: op(oid),
			RunE: func(cmd *cobra.Command, args []string) error {
				var c certRow
				if err := a.do(cmd, method, "/api/v1/certificates/"+url.PathEscape(args[0])+suffix, nil, &c); err != nil {
					return err
				}
				if a.output == "json" {
					return a.printer().json(c)
				}
				printCert(a, c)
				return nil
			},
		}
	}
	verify := simple("verify ID", "Check the order's TXT records now and go on when they are all visible", "verifyCertificate", "POST", "/verify", true)
	revoke := simple("revoke ID", "Revoke at the authority (ACME), or stop serving an imported certificate", "revokeCertificate", "POST", "/revoke", true)
	reissue := &cobra.Command{
		Use: "reissue ID NAME...", Short: "Change the names and order again; the old certificate serves until the new one is valid", Args: cobra.MinimumNArgs(2), Annotations: op("reissueCertificate"),
		RunE: func(cmd *cobra.Command, args []string) error {
			var c certRow
			if err := a.do(cmd, "POST", "/api/v1/certificates/"+url.PathEscape(args[0])+"/reissue", map[string]any{"names": args[1:]}, &c); err != nil {
				return err
			}
			printCert(a, c)
			return nil
		},
	}
	var force bool
	del := &cobra.Command{
		Use: "delete ID", Aliases: []string{"rm"}, Short: "Delete a certificate that was asked for", Args: cobra.ExactArgs(1), Annotations: op("deleteCertificate"),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "/api/v1/certificates/" + url.PathEscape(args[0])
			if force {
				path += "?force=true"
			}
			if err := a.do(cmd, "DELETE", path, nil, nil); err != nil {
				return err
			}
			fmt.Fprintf(a.out, "Deleted %s\n", args[0])
			return nil
		},
	}
	del.Flags().BoolVar(&force, "force", false, "delete although a wildcard host is left with a placeholder")

	events := &cobra.Command{
		Use: "events ID", Aliases: []string{"history"}, Short: "A certificate's history", Args: cobra.ExactArgs(1), Annotations: op("listCertificateEvents"),
		RunE: func(cmd *cobra.Command, args []string) error {
			var out struct {
				Items []struct{ At, Kind, Detail, Actor string } `json:"items"`
			}
			if err := a.do(cmd, "GET", "/api/v1/certificates/"+url.PathEscape(args[0])+"/events", nil, &out); err != nil {
				return err
			}
			rows := make([][]string, 0, len(out.Items))
			for _, e := range out.Items {
				at := e.At
				if t, err := time.Parse(time.RFC3339, e.At); err == nil {
					at = t.Local().Format("2006-01-02 15:04")
				}
				rows = append(rows, []string{at, e.Kind, e.Detail, orDash(e.Actor)})
			}
			return a.printer().table(out.Items, []string{"WHEN", "EVENT", "DETAIL", "BY"}, rows)
		},
	}
	var outFile string
	chain := &cobra.Command{
		Use: "chain ID", Short: "The certificate chain as PEM (public: no key)", Args: cobra.ExactArgs(1), Annotations: op("getCertificateChain"),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := a.client()
			if err != nil {
				return err
			}
			status, raw, err := c.Raw(ctx(cmd), "GET", "/api/v1/certificates/"+url.PathEscape(args[0])+"/chain", nil)
			if err != nil {
				return err
			}
			if status != 200 {
				return fmt.Errorf("no certificate chain (%d)", status)
			}
			if outFile != "" {
				return os.WriteFile(outFile, raw, 0o644)
			}
			_, err = a.out.Write(raw)
			return err
		},
	}
	chain.Flags().StringVar(&outFile, "out", "", "write it to a file")
	var certOut, keyOut string
	export := &cobra.Command{
		Use: "export ID", Short: "Download the chain and the PRIVATE KEY (needs certificate:ExportCertificate; audited; 10 an hour)", Args: cobra.ExactArgs(1), Annotations: op("exportCertificate"),
		RunE: func(cmd *cobra.Command, args []string) error {
			var out struct{ Certificate, PrivateKey string }
			if err := a.do(cmd, "GET", "/api/v1/certificates/"+url.PathEscape(args[0])+"/bundle", nil, &out); err != nil {
				return err
			}
			if certOut == "" || keyOut == "" {
				return fmt.Errorf("give --cert FILE and --key FILE: the private key is never printed")
			}
			if err := os.WriteFile(certOut, []byte(out.Certificate), 0o644); err != nil {
				return err
			}
			if err := os.WriteFile(keyOut, []byte(out.PrivateKey), 0o600); err != nil {
				return err
			}
			fmt.Fprintf(a.out, "Wrote %s and %s (mode 0600)\n", certOut, keyOut)
			return nil
		},
	}
	export.Flags().StringVar(&certOut, "cert", "", "where to write the chain")
	export.Flags().StringVar(&keyOut, "key", "", "where to write the private key")

	var chainFile, keyFile, pass, p12, p12pass, replace string
	imp := &cobra.Command{
		Use: "import", Short: "Bring in a certificate from a provider: its chain and private key (PEM, or --pkcs12)",
		Example: `  synctl certs import --chain fullchain.pem --key privkey.pem
  synctl certs import --pkcs12 site.pfx --password secret
  synctl certs import --chain new.pem --key new-key.pem --replace cert_ab12cd34ef56`,
		Args: cobra.NoArgs, Annotations: op("importCertificate"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			body := map[string]any{"passphrase": pass, "password": p12pass, "replace": replace}
			if p12 != "" {
				raw, err := os.ReadFile(p12)
				if err != nil {
					return err
				}
				body["pkcs12"] = b64(raw)
			} else {
				if chainFile == "" || keyFile == "" {
					return fmt.Errorf("--chain and --key are required (or --pkcs12)")
				}
				ch, err := readFile(chainFile)
				if err != nil {
					return err
				}
				k, err := readFile(keyFile)
				if err != nil {
					return err
				}
				body["chain"], body["key"] = ch, k
			}
			var out struct {
				Certificate certRow  `json:"certificate"`
				Warnings    []string `json:"warnings"`
			}
			if err := a.do(cmd, "POST", "/api/v1/certificates/import", body, &out); err != nil {
				return err
			}
			if a.output == "json" {
				return a.printer().json(out)
			}
			printCert(a, out.Certificate)
			for _, w := range out.Warnings {
				fmt.Fprintln(a.errOut, "warning:", w)
			}
			return nil
		},
	}
	imp.Flags().StringVar(&chainFile, "chain", "", "certificate chain, PEM")
	imp.Flags().StringVar(&keyFile, "key", "", "private key, PEM")
	imp.Flags().StringVar(&pass, "passphrase", "", "the key's passphrase, if it is encrypted (not stored)")
	imp.Flags().StringVar(&p12, "pkcs12", "", "a .p12/.pfx file instead of --chain and --key")
	imp.Flags().StringVar(&p12pass, "password", "", "the PKCS#12 password")
	imp.Flags().StringVar(&replace, "replace", "", "the certificate this replaces: keeps its place and pinned hosts")

	// csr: make a request, then complete it with what the provider signs.
	var csrKey string
	csr := &cobra.Command{Use: "csr", Short: "Make a key and a certificate request (CSR) for a provider to sign; the key stays here"}
	csrNew := &cobra.Command{
		Use: "new NAME...", Short: "Create the request and print its CSR", Args: cobra.MinimumNArgs(1), Annotations: op("createCertificateRequest"),
		RunE: func(cmd *cobra.Command, args []string) error {
			var r struct {
				ID, CSR string
				Names   []string
			}
			if err := a.do(cmd, "POST", "/api/v1/certificate-requests", map[string]any{"names": args, "keyType": csrKey}, &r); err != nil {
				return err
			}
			if a.output == "json" {
				return a.printer().json(r)
			}
			fmt.Fprint(a.out, r.CSR)
			fmt.Fprintf(a.errOut, "\nRequest %s: give this CSR to your provider, then: synctl certs csr complete %s --chain signed.pem\n", r.ID, r.ID)
			return nil
		},
	}
	csrNew.Flags().StringVar(&csrKey, "key-type", "", "ecdsa-p256 (default), rsa-2048, rsa-3072 or rsa-4096")
	csrList := &cobra.Command{
		Use: "list", Aliases: []string{"ls"}, Short: "Requests waiting for the provider's certificate", Args: cobra.NoArgs, Annotations: op("listCertificateRequests"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			var out struct {
				Items []struct {
					ID, KeyType, CreatedAt string
					Names                  []string
				} `json:"items"`
			}
			if err := a.do(cmd, "GET", "/api/v1/certificate-requests", nil, &out); err != nil {
				return err
			}
			rows := make([][]string, 0, len(out.Items))
			for _, r := range out.Items {
				rows = append(rows, []string{r.ID, strings.Join(r.Names, ", "), r.KeyType, r.CreatedAt})
			}
			return a.printer().table(out.Items, []string{"ID", "NAMES", "KEY", "CREATED"}, rows)
		},
	}
	var signed string
	csrDone := &cobra.Command{
		Use: "complete ID", Short: "Upload what the provider signed for a request", Args: cobra.ExactArgs(1), Annotations: op("completeCertificateRequest"),
		RunE: func(cmd *cobra.Command, args []string) error {
			ch, err := readFile(signed)
			if err != nil {
				return err
			}
			var out struct {
				Certificate certRow  `json:"certificate"`
				Warnings    []string `json:"warnings"`
			}
			if err := a.do(cmd, "POST", "/api/v1/certificate-requests/"+url.PathEscape(args[0])+"/complete", map[string]string{"chain": ch}, &out); err != nil {
				return err
			}
			printCert(a, out.Certificate)
			for _, w := range out.Warnings {
				fmt.Fprintln(a.errOut, "warning:", w)
			}
			return nil
		},
	}
	csrDone.Flags().StringVar(&signed, "chain", "", "the signed certificate (and its chain), PEM")
	csrDel := &cobra.Command{
		Use: "delete ID", Aliases: []string{"rm"}, Short: "Discard a request and its key", Args: cobra.ExactArgs(1), Annotations: op("deleteCertificateRequest"),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.do(cmd, "DELETE", "/api/v1/certificate-requests/"+url.PathEscape(args[0]), nil, nil); err != nil {
				return err
			}
			fmt.Fprintf(a.out, "Discarded %s\n", args[0])
			return nil
		},
	}
	csr.AddCommand(csrNew, csrList, csrDone, csrDel)

	cc.AddCommand(request, get, update, verify, reissue, revoke, del, events, chain, export, imp, csr, a.authoritiesCmd())
}

func (a *app) authoritiesCmd() *cobra.Command {
	ac := &cobra.Command{Use: "authorities", Aliases: []string{"authority", "ca"}, Short: "Certificate authorities: Let's Encrypt (default), others from providers"}
	type auth struct {
		ID, Name, Kind, Status, LastError, DirectoryURL string
		Default, Fallback, Builtin                      bool
		HasEab                                          bool `json:"hasEab"`
		Certificates                                    int
	}
	ac.AddCommand(&cobra.Command{
		Use: "list", Aliases: []string{"ls"}, Short: "List authorities and the presets that can be added", Args: cobra.NoArgs, Annotations: op("listCertificateAuthorities"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			var out struct {
				Items   []auth                                          `json:"items"`
				Presets []struct{ ID, Name, DirectoryURL, Hint string } `json:"presets"`
			}
			if err := a.do(cmd, "GET", "/api/v1/certificate-authorities", nil, &out); err != nil {
				return err
			}
			rows := make([][]string, 0, len(out.Items))
			for _, x := range out.Items {
				role := ""
				if x.Default {
					role = "default"
				}
				if x.Fallback {
					role = strings.TrimSpace(role + " fallback")
				}
				rows = append(rows, []string{x.ID, x.Name, x.Kind, x.Status, role, strconv.Itoa(x.Certificates), x.DirectoryURL})
			}
			if err := a.printer().table(out.Items, []string{"ID", "NAME", "KIND", "STATUS", "ROLE", "CERTS", "DIRECTORY"}, rows); err != nil {
				return err
			}
			if a.output != "json" && len(out.Presets) > 0 {
				fmt.Fprintln(a.out, "\nPresets for `synctl certs authorities add --preset`:")
				for _, p := range out.Presets {
					fmt.Fprintf(a.out, "  %-10s %s\n", p.ID, p.Name)
				}
			}
			return nil
		},
	})
	var preset, dir, email, kid, hmac, keyType, kind, provider string
	add := &cobra.Command{
		Use: "add NAME", Short: "Add an authority: a preset or any ACME directory, with its external account binding",
		Example: `  synctl certs authorities add ZeroSSL --preset zerossl --eab-kid KID --eab-hmac HMAC
  synctl certs authorities add Corp --directory https://acme.corp.example/directory --email ops@corp.example
  synctl certs authorities add "Cloudflare origin" --kind cloudflare-origin --provider dnsp_ab12`,
		Args: cobra.ExactArgs(1), Annotations: op("createCertificateAuthority"),
		RunE: func(cmd *cobra.Command, args []string) error {
			var x auth
			if err := a.do(cmd, "POST", "/api/v1/certificate-authorities", map[string]any{"name": args[0], "preset": preset, "directoryUrl": dir, "email": email,
				"eabKid": kid, "eabHmac": hmac, "keyType": keyType, "kind": kind, "provider": provider}, &x); err != nil {
				return err
			}
			fmt.Fprintf(a.out, "Added %s (%s). Test it with: synctl certs authorities test %s\n", x.Name, x.ID, x.ID)
			return nil
		},
	}
	add.Flags().StringVar(&preset, "preset", "", "zerossl, google, sslcom, buypass or custom")
	add.Flags().StringVar(&dir, "directory", "", "the ACME directory URL")
	add.Flags().StringVar(&email, "email", "", "account contact email")
	add.Flags().StringVar(&kid, "eab-kid", "", "external account binding: key id")
	add.Flags().StringVar(&hmac, "eab-hmac", "", "external account binding: HMAC key (as the provider shows it)")
	add.Flags().StringVar(&keyType, "key-type", "", "the key type certificates get")
	add.Flags().StringVar(&kind, "kind", "", "acme (default) or cloudflare-origin")
	add.Flags().StringVar(&provider, "provider", "", "cloudflare-origin: the DNS provider id")

	var makeDefault, makeFallback, noFallback bool
	var newDir, newEmail, newKid, newHmac string
	upd := &cobra.Command{
		Use: "update ID", Short: "Change an authority, or make it the default or the fallback", Args: cobra.ExactArgs(1), Annotations: op("updateCertificateAuthority"),
		RunE: func(cmd *cobra.Command, args []string) error {
			b := map[string]any{}
			if makeDefault {
				b["default"] = true
			}
			if makeFallback {
				b["fallback"] = true
			}
			if noFallback {
				b["fallback"] = false
			}
			for k, v := range map[string]string{"directoryUrl": newDir, "email": newEmail, "eabKid": newKid, "eabHmac": newHmac} {
				if v != "" {
					b[k] = v
				}
			}
			var x auth
			if err := a.do(cmd, "PUT", "/api/v1/certificate-authorities/"+url.PathEscape(args[0]), b, &x); err != nil {
				return err
			}
			fmt.Fprintf(a.out, "Updated %s\n", x.Name)
			return nil
		},
	}
	upd.Flags().BoolVar(&makeDefault, "default", false, "new certificates use it")
	upd.Flags().BoolVar(&makeFallback, "fallback", false, "tried when the default fails or is rate limited")
	upd.Flags().BoolVar(&noFallback, "no-fallback", false, "turn the fallback off")
	upd.Flags().StringVar(&newDir, "directory", "", "ACME directory URL")
	upd.Flags().StringVar(&newEmail, "email", "", "account contact email")
	upd.Flags().StringVar(&newKid, "eab-kid", "", "binding key id")
	upd.Flags().StringVar(&newHmac, "eab-hmac", "", "binding HMAC key")
	ac.AddCommand(add, upd,
		&cobra.Command{
			Use: "test ID", Short: "Register the account at the authority and show the result", Args: cobra.ExactArgs(1), Annotations: op("testCertificateAuthority"),
			RunE: func(cmd *cobra.Command, args []string) error {
				var x auth
				if err := a.do(cmd, "POST", "/api/v1/certificate-authorities/"+url.PathEscape(args[0])+"/test", nil, &x); err != nil {
					return err
				}
				fmt.Fprintf(a.out, "%s: %s\n", x.Name, x.Status)
				return nil
			},
		},
		&cobra.Command{
			Use: "get ID", Short: "Show an authority", Args: cobra.ExactArgs(1), Annotations: op("getCertificateAuthority"),
			RunE: func(cmd *cobra.Command, args []string) error {
				var x auth
				if err := a.do(cmd, "GET", "/api/v1/certificate-authorities/"+url.PathEscape(args[0]), nil, &x); err != nil {
					return err
				}
				if a.output == "json" {
					return a.printer().json(x)
				}
				fmt.Fprintf(a.out, "%s (%s)\nKind: %s  Status: %s %s\nDirectory: %s\nDefault: %v  Fallback: %v  Binding: %v\n", x.Name, x.ID, x.Kind, x.Status, x.LastError, orDash(x.DirectoryURL), x.Default, x.Fallback, x.HasEab)
				return nil
			},
		},
		&cobra.Command{
			Use: "remove ID", Aliases: []string{"rm"}, Short: "Remove an authority nobody uses", Args: cobra.ExactArgs(1), Annotations: op("deleteCertificateAuthority"),
			RunE: func(cmd *cobra.Command, args []string) error {
				if err := a.do(cmd, "DELETE", "/api/v1/certificate-authorities/"+url.PathEscape(args[0]), nil, nil); err != nil {
					return err
				}
				fmt.Fprintf(a.out, "Removed %s\n", args[0])
				return nil
			},
		},
	)
	return ac
}

func b64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }
