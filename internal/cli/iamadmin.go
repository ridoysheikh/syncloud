package cli

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"syncloud/internal/client"
)

// IAM administration, STS, the audit log, device login, quotas and usage (§7).

type iamUser struct {
	ID         string     `json:"id"`
	Email      string     `json:"email"`
	Name       string     `json:"name"`
	Kind       string     `json:"kind"`
	IsRoot     bool       `json:"isRoot"`
	Disabled   bool       `json:"disabled"`
	MFAEnabled bool       `json:"mfaEnabled"`
	Groups     []string   `json:"groups"`
	Policies   []string   `json:"policies"`
	LastLogin  *time.Time `json:"lastLoginAt"`
}

type iamGroup struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Description string   `json:"description"`
	Members     []string `json:"members"`
	Policies    []string `json:"policies"`
}

type iamPolicy struct {
	ID          string          `json:"id"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Managed     bool            `json:"managed"`
	PerProject  bool            `json:"perProject"`
	Document    json.RawMessage `json:"document"`
	AttachedTo  int             `json:"attachedTo"`
}

type iamRole struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Trust       struct {
		Users      []string `json:"users"`
		Groups     []string `json:"groups"`
		RequireMFA bool     `json:"requireMfa"`
	} `json:"trust"`
	MaxSessionSeconds int      `json:"maxSessionSeconds"`
	Policies          []string `json:"policies"`
}

type tempCreds struct {
	AccessKeyID     string    `json:"accessKeyId"`
	SecretAccessKey string    `json:"secretAccessKey"`
	SessionToken    string    `json:"sessionToken"`
	Expiration      time.Time `json:"expiration"`
	Role            string    `json:"role"`
}

func listOf[T any](a *app, cmd *cobra.Command, path string) ([]T, error) {
	c, err := a.client()
	if err != nil {
		return nil, err
	}
	var out struct {
		Items []T `json:"items"`
	}
	return out.Items, c.Do(ctx(cmd), "GET", path, nil, &out)
}

// userID resolves an email, name or ID to a user ID.
func (a *app) userID(cmd *cobra.Command, ref string) (string, error) {
	us, err := listOf[iamUser](a, cmd, "/api/v1/iam/users")
	if err != nil {
		return "", err
	}
	for _, u := range us {
		if u.ID == ref || strings.EqualFold(u.Email, ref) || (u.Kind == "service" && u.Name == ref) {
			return u.ID, nil
		}
	}
	return "", fmt.Errorf("no user %q", ref)
}

func (a *app) groupID(cmd *cobra.Command, ref string) (string, error) {
	gs, err := listOf[iamGroup](a, cmd, "/api/v1/iam/groups")
	if err != nil {
		return "", err
	}
	for _, g := range gs {
		if g.ID == ref || g.Name == ref {
			return g.ID, nil
		}
	}
	return "", fmt.Errorf("no group %q", ref)
}

func (a *app) roleID(cmd *cobra.Command, ref string) (string, error) {
	rs, err := listOf[iamRole](a, cmd, "/api/v1/iam/roles")
	if err != nil {
		return "", err
	}
	for _, r := range rs {
		if r.ID == ref || r.Name == ref {
			return r.ID, nil
		}
	}
	return "", fmt.Errorf("no role %q", ref)
}

// policyID resolves a customer policy name; managed names pass through.
func (a *app) policyID(cmd *cobra.Command, ref string) (string, error) {
	ps, err := listOf[iamPolicy](a, cmd, "/api/v1/iam/policies")
	if err != nil {
		return "", err
	}
	base, _, _ := strings.Cut(ref, ":")
	for _, p := range ps {
		if p.Managed && p.Name == base {
			return ref, nil
		}
		if !p.Managed && (p.ID == ref || p.Name == ref) {
			return p.ID, nil
		}
	}
	return "", fmt.Errorf("no policy %q", ref)
}

func (a *app) do(cmd *cobra.Command, method, path string, body, out any) error {
	c, err := a.client()
	if err != nil {
		return err
	}
	return c.Do(ctx(cmd), method, path, body, out)
}

func (a *app) iamAdminCmds() []*cobra.Command {
	users := &cobra.Command{Use: "users", Aliases: []string{"user"}, Short: "Users and service accounts"}
	var kind, password, name string
	create := &cobra.Command{
		Use: "create EMAIL|NAME", Short: "Create a user (email) or, with --service, a service account (name)", Args: cobra.ExactArgs(1),
		Annotations: op("createUser"),
		RunE: func(cmd *cobra.Command, args []string) error {
			body := map[string]string{"email": args[0], "name": name, "kind": kind, "password": password}
			if kind == "service" {
				body["name"], body["email"] = args[0], ""
			} else if name == "" {
				body["name"] = strings.Split(args[0], "@")[0]
			}
			var u iamUser
			if err := a.do(cmd, "POST", "/api/v1/iam/users", body, &u); err != nil {
				return err
			}
			fmt.Fprintf(a.out, "Created %s %s (%s)\n", map[string]string{"service": "service account", "user": "user"}[u.Kind], u.Email, u.ID)
			return nil
		},
	}
	create.Flags().StringVar(&name, "name", "", "display name")
	create.Flags().StringVar(&password, "password", "", "initial password (12+ characters)")
	create.Flags().StringVar(&kind, "kind", "user", "user or service")
	var disabled bool
	var newPassword, rename string
	update := &cobra.Command{
		Use: "update USER", Short: "Rename, disable or enable a user, or reset its password", Args: cobra.ExactArgs(1),
		Annotations: op("updateUser"),
		RunE: func(cmd *cobra.Command, args []string) error {
			id, err := a.userID(cmd, args[0])
			if err != nil {
				return err
			}
			body := map[string]any{}
			if cmd.Flags().Changed("disabled") {
				body["disabled"] = disabled
			}
			if rename != "" {
				body["name"] = rename
			}
			if newPassword != "" {
				body["password"] = newPassword
			}
			if err := a.do(cmd, "PUT", "/api/v1/iam/users/"+id, body, nil); err != nil {
				return err
			}
			fmt.Fprintln(a.out, "Updated "+args[0])
			return nil
		},
	}
	update.Flags().BoolVar(&disabled, "disabled", false, "disable (true) or enable (false) the account")
	update.Flags().StringVar(&rename, "name", "", "new display name")
	update.Flags().StringVar(&newPassword, "password", "", "reset the password")
	users.AddCommand(
		&cobra.Command{
			Use: "list", Aliases: []string{"ls"}, Short: "List users and service accounts", Args: cobra.NoArgs,
			Annotations: op("listUsers"),
			RunE: func(cmd *cobra.Command, _ []string) error {
				us, err := listOf[iamUser](a, cmd, "/api/v1/iam/users")
				if err != nil {
					return err
				}
				rows := [][]string{}
				for _, u := range us {
					state := "active"
					if u.Disabled {
						state = "disabled"
					}
					if u.IsRoot {
						state += ", root"
					}
					rows = append(rows, []string{u.Email, u.Kind, state, strconv.FormatBool(u.MFAEnabled), joinOrDash(u.Groups), joinOrDash(u.Policies), ago(u.LastLogin)})
				}
				return a.printer().table(us, []string{"EMAIL", "KIND", "STATE", "MFA", "GROUPS", "POLICIES", "LAST LOGIN"}, rows)
			},
		},
		create, update,
		&cobra.Command{
			Use: "delete USER", Aliases: []string{"rm"}, Short: "Delete a user", Args: cobra.ExactArgs(1),
			Annotations: op("deleteUser"),
			RunE: func(cmd *cobra.Command, args []string) error {
				id, err := a.userID(cmd, args[0])
				if err != nil {
					return err
				}
				if err := a.do(cmd, "DELETE", "/api/v1/iam/users/"+id, nil, nil); err != nil {
					return err
				}
				fmt.Fprintln(a.out, "Deleted "+args[0])
				return nil
			},
		},
	)

	groups := &cobra.Command{Use: "groups", Aliases: []string{"group"}, Short: "Groups of users"}
	var members []string
	var gdesc string
	gapply := &cobra.Command{
		Use: "apply NAME --member EMAIL…", Short: "Create or update a group (members are replaced)", Args: cobra.ExactArgs(1),
		Annotations: op("createGroup", "updateGroup"),
		RunE: func(cmd *cobra.Command, args []string) error {
			ids := []string{}
			for _, m := range members {
				id, err := a.userID(cmd, m)
				if err != nil {
					return err
				}
				ids = append(ids, id)
			}
			body := map[string]any{"name": args[0], "description": gdesc, "members": ids}
			if gid, err := a.groupID(cmd, args[0]); err == nil {
				if err := a.do(cmd, "PUT", "/api/v1/iam/groups/"+gid, body, nil); err != nil {
					return err
				}
				fmt.Fprintf(a.out, "Updated group %s (%d members)\n", args[0], len(ids))
				return nil
			}
			if err := a.do(cmd, "POST", "/api/v1/iam/groups", body, nil); err != nil {
				return err
			}
			fmt.Fprintf(a.out, "Created group %s (%d members)\n", args[0], len(ids))
			return nil
		},
	}
	gapply.Flags().StringSliceVar(&members, "member", nil, "member email (repeatable)")
	gapply.Flags().StringVar(&gdesc, "description", "", "description")
	groups.AddCommand(
		&cobra.Command{
			Use: "list", Aliases: []string{"ls"}, Short: "List groups", Args: cobra.NoArgs,
			Annotations: op("listGroups"),
			RunE: func(cmd *cobra.Command, _ []string) error {
				gs, err := listOf[iamGroup](a, cmd, "/api/v1/iam/groups")
				if err != nil {
					return err
				}
				rows := [][]string{}
				for _, g := range gs {
					rows = append(rows, []string{g.Name, strconv.Itoa(len(g.Members)), joinOrDash(g.Policies), orDash(g.Description)})
				}
				return a.printer().table(gs, []string{"NAME", "MEMBERS", "POLICIES", "DESCRIPTION"}, rows)
			},
		},
		gapply,
		&cobra.Command{
			Use: "delete NAME", Aliases: []string{"rm"}, Short: "Delete a group", Args: cobra.ExactArgs(1),
			Annotations: op("deleteGroup"),
			RunE: func(cmd *cobra.Command, args []string) error {
				id, err := a.groupID(cmd, args[0])
				if err != nil {
					return err
				}
				if err := a.do(cmd, "DELETE", "/api/v1/iam/groups/"+id, nil, nil); err != nil {
					return err
				}
				fmt.Fprintln(a.out, "Deleted group "+args[0])
				return nil
			},
		},
	)

	policies := &cobra.Command{Use: "policies", Aliases: []string{"policy"}, Short: "JSON policies: managed (built in) and your own"}
	var pfile, pdesc string
	papply := &cobra.Command{
		Use: "apply NAME -f FILE", Short: "Create or update a policy from a JSON document", Args: cobra.ExactArgs(1),
		Annotations: op("createPolicy", "updatePolicy"),
		Example:     `  echo '{"Version":"2026-01","Statement":[{"Effect":"Allow","Action":["service:Get*","service:ScaleService"],"Resource":"srn:syncloud:project/shop/*"}]}' | synctl iam policies apply shop-scaler -f -`,
		RunE: func(cmd *cobra.Command, args []string) error {
			b, err := a.readInput(pfile)
			if err != nil {
				return err
			}
			body := map[string]any{"name": args[0], "description": pdesc, "document": json.RawMessage(b)}
			if id, err := a.policyID(cmd, args[0]); err == nil && strings.HasPrefix(id, "pol_") {
				if err := a.do(cmd, "PUT", "/api/v1/iam/policies/"+id, body, nil); err != nil {
					return err
				}
				fmt.Fprintln(a.out, "Updated policy "+args[0])
				return nil
			}
			if err := a.do(cmd, "POST", "/api/v1/iam/policies", body, nil); err != nil {
				return err
			}
			fmt.Fprintln(a.out, "Created policy "+args[0])
			return nil
		},
	}
	papply.Flags().StringVarP(&pfile, "file", "f", "", "policy JSON, or - for stdin")
	papply.Flags().StringVar(&pdesc, "description", "", "description")
	_ = papply.MarkFlagRequired("file")
	policies.AddCommand(
		&cobra.Command{
			Use: "list", Aliases: []string{"ls"}, Short: "List policies", Args: cobra.NoArgs,
			Annotations: op("listPolicies"),
			RunE: func(cmd *cobra.Command, _ []string) error {
				ps, err := listOf[iamPolicy](a, cmd, "/api/v1/iam/policies")
				if err != nil {
					return err
				}
				rows := [][]string{}
				for _, p := range ps {
					k := "customer"
					if p.Managed {
						k = "managed"
						if p.PerProject {
							k = "managed, per project (Name:project)"
						}
					}
					rows = append(rows, []string{p.Name, k, strconv.Itoa(p.AttachedTo), orDash(p.Description)})
				}
				return a.printer().table(ps, []string{"NAME", "KIND", "ATTACHED", "DESCRIPTION"}, rows)
			},
		},
		&cobra.Command{
			Use: "get NAME", Short: "Print a policy document", Args: cobra.ExactArgs(1),
			RunE: func(cmd *cobra.Command, args []string) error {
				ps, err := listOf[iamPolicy](a, cmd, "/api/v1/iam/policies")
				if err != nil {
					return err
				}
				for _, p := range ps {
					if p.Name == args[0] || p.ID == args[0] {
						return a.printer().json(p.Document)
					}
				}
				return fmt.Errorf("no policy %q", args[0])
			},
		},
		papply,
		&cobra.Command{
			Use: "delete NAME", Aliases: []string{"rm"}, Short: "Delete a policy", Args: cobra.ExactArgs(1),
			Annotations: op("deletePolicy"),
			RunE: func(cmd *cobra.Command, args []string) error {
				id, err := a.policyID(cmd, args[0])
				if err != nil {
					return err
				}
				if err := a.do(cmd, "DELETE", "/api/v1/iam/policies/"+id, nil, nil); err != nil {
					return err
				}
				fmt.Fprintln(a.out, "Deleted policy "+args[0])
				return nil
			},
		},
	)

	var toUser, toGroup, toRole string
	attachment := func(cmd *cobra.Command, policy string) (map[string]string, error) {
		pid, err := a.policyID(cmd, policy)
		if err != nil {
			return nil, err
		}
		body := map[string]string{"policy": pid}
		switch {
		case toUser != "":
			body["principalType"] = "user"
			body["principalId"], err = a.userID(cmd, toUser)
		case toGroup != "":
			body["principalType"] = "group"
			body["principalId"], err = a.groupID(cmd, toGroup)
		case toRole != "":
			body["principalType"] = "role"
			body["principalId"], err = a.roleID(cmd, toRole)
		default:
			err = errors.New("give --user, --group or --role")
		}
		return body, err
	}
	attach := &cobra.Command{
		Use: "attach POLICY (--user|--group|--role) NAME", Short: "Attach a policy (project templates as Developer:shop)", Args: cobra.ExactArgs(1),
		Annotations: op("attachPolicy"),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := attachment(cmd, args[0])
			if err != nil {
				return err
			}
			if err := a.do(cmd, "POST", "/api/v1/iam/attachments", body, nil); err != nil {
				return err
			}
			fmt.Fprintf(a.out, "Attached %s to %s %s\n", args[0], body["principalType"], toUser+toGroup+toRole)
			return nil
		},
	}
	detach := &cobra.Command{
		Use: "detach POLICY (--user|--group|--role) NAME", Short: "Detach a policy", Args: cobra.ExactArgs(1),
		Annotations: op("detachPolicy"),
		RunE: func(cmd *cobra.Command, args []string) error {
			body, err := attachment(cmd, args[0])
			if err != nil {
				return err
			}
			if err := a.do(cmd, "DELETE", "/api/v1/iam/attachments", body, nil); err != nil {
				return err
			}
			fmt.Fprintf(a.out, "Detached %s from %s %s\n", args[0], body["principalType"], toUser+toGroup+toRole)
			return nil
		},
	}
	for _, c := range []*cobra.Command{attach, detach} {
		c.Flags().StringVar(&toUser, "user", "", "user email or ID")
		c.Flags().StringVar(&toGroup, "group", "", "group name")
		c.Flags().StringVar(&toRole, "role", "", "role name")
	}
	attachments := &cobra.Command{
		Use: "attachments", Short: "Which policies are attached where", Args: cobra.NoArgs,
		Annotations: op("listAttachments"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			atts, err := listOf[map[string]string](a, cmd, "/api/v1/iam/attachments")
			if err != nil {
				return err
			}
			rows := [][]string{}
			for _, x := range atts {
				rows = append(rows, []string{x["policy"], x["principalType"], x["principalId"]})
			}
			return a.printer().table(atts, []string{"POLICY", "TYPE", "PRINCIPAL"}, rows)
		},
	}

	roles := &cobra.Command{Use: "roles", Aliases: []string{"role"}, Short: "Roles assumable with sts assume-role"}
	var trustUsers, trustGroups []string
	var requireMFA bool
	var maxSession int
	var rdesc string
	rapply := &cobra.Command{
		Use: "apply NAME --trust-user EMAIL…", Short: "Create or update a role and who may assume it", Args: cobra.ExactArgs(1),
		Annotations: op("createRole", "updateRole"),
		RunE: func(cmd *cobra.Command, args []string) error {
			trust := map[string]any{"users": []string{}, "groups": []string{}, "requireMfa": requireMFA}
			for _, u := range trustUsers {
				id, err := a.userID(cmd, u)
				if err != nil {
					return err
				}
				trust["users"] = append(trust["users"].([]string), id)
			}
			for _, g := range trustGroups {
				id, err := a.groupID(cmd, g)
				if err != nil {
					return err
				}
				trust["groups"] = append(trust["groups"].([]string), id)
			}
			body := map[string]any{"name": args[0], "description": rdesc, "trust": trust, "maxSessionSeconds": maxSession}
			if id, err := a.roleID(cmd, args[0]); err == nil {
				if err := a.do(cmd, "PUT", "/api/v1/iam/roles/"+id, body, nil); err != nil {
					return err
				}
				fmt.Fprintln(a.out, "Updated role "+args[0])
				return nil
			}
			if err := a.do(cmd, "POST", "/api/v1/iam/roles", body, nil); err != nil {
				return err
			}
			fmt.Fprintln(a.out, "Created role "+args[0]+"; attach policies with synctl iam attach POLICY --role "+args[0])
			return nil
		},
	}
	rapply.Flags().StringSliceVar(&trustUsers, "trust-user", nil, "user who may assume it (repeatable)")
	rapply.Flags().StringSliceVar(&trustGroups, "trust-group", nil, "group whose members may assume it (repeatable)")
	rapply.Flags().BoolVar(&requireMFA, "require-mfa", false, "only sessions signed in with MFA may assume it")
	rapply.Flags().IntVar(&maxSession, "max-session", 3600, "longest session in seconds (900–43200)")
	rapply.Flags().StringVar(&rdesc, "description", "", "description")
	roles.AddCommand(
		&cobra.Command{
			Use: "list", Aliases: []string{"ls"}, Short: "List roles", Args: cobra.NoArgs,
			Annotations: op("listRoles"),
			RunE: func(cmd *cobra.Command, _ []string) error {
				rs, err := listOf[iamRole](a, cmd, "/api/v1/iam/roles")
				if err != nil {
					return err
				}
				rows := [][]string{}
				for _, r := range rs {
					rows = append(rows, []string{r.Name, fmt.Sprintf("%d users, %d groups", len(r.Trust.Users), len(r.Trust.Groups)), strconv.FormatBool(r.Trust.RequireMFA),
						(time.Duration(r.MaxSessionSeconds) * time.Second).String(), joinOrDash(r.Policies)})
				}
				return a.printer().table(rs, []string{"NAME", "TRUSTS", "MFA", "MAX SESSION", "POLICIES"}, rows)
			},
		},
		rapply,
		&cobra.Command{
			Use: "delete NAME", Aliases: []string{"rm"}, Short: "Delete a role", Args: cobra.ExactArgs(1),
			Annotations: op("deleteRole"),
			RunE: func(cmd *cobra.Command, args []string) error {
				id, err := a.roleID(cmd, args[0])
				if err != nil {
					return err
				}
				if err := a.do(cmd, "DELETE", "/api/v1/iam/roles/"+id, nil, nil); err != nil {
					return err
				}
				fmt.Fprintln(a.out, "Deleted role "+args[0])
				return nil
			},
		},
	)

	mfa := &cobra.Command{Use: "mfa", Short: "Multi-factor authentication (TOTP)"}
	var mfaUser string
	mfaDisable := &cobra.Command{
		Use: "disable [CODE]", Short: "Turn MFA off (yours with a current code, or another user's with --user)", Args: cobra.MaximumNArgs(1),
		Annotations: op("disableMFA"),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := "/api/v1/iam/mfa"
			if mfaUser != "" {
				id, err := a.userID(cmd, mfaUser)
				if err != nil {
					return err
				}
				path += "?userId=" + url.QueryEscape(id)
			}
			code := ""
			if len(args) > 0 {
				code = args[0]
			}
			if err := a.do(cmd, "DELETE", path, map[string]string{"code": code}, nil); err != nil {
				return err
			}
			fmt.Fprintln(a.out, "MFA is off.")
			return nil
		},
	}
	mfaDisable.Flags().StringVar(&mfaUser, "user", "", "another user (administrators)")
	mfa.AddCommand(
		&cobra.Command{
			Use: "begin", Short: "Start enrolling (prints the secret and otpauth URI)", Args: cobra.NoArgs,
			Annotations: op("beginMFA"),
			RunE: func(cmd *cobra.Command, _ []string) error {
				var out map[string]string
				if err := a.do(cmd, "POST", "/api/v1/iam/mfa", nil, &out); err != nil {
					return err
				}
				fmt.Fprintf(a.out, "Secret: %s\nURI:    %s\nThen: synctl iam mfa enable CODE\n", out["secret"], out["uri"])
				return nil
			},
		},
		&cobra.Command{
			Use: "enable CODE", Short: "Finish enrolling with a code from your authenticator", Args: cobra.ExactArgs(1),
			Annotations: op("enableMFA"),
			RunE: func(cmd *cobra.Command, args []string) error {
				if err := a.do(cmd, "POST", "/api/v1/iam/mfa/enable", map[string]string{"code": args[0]}, nil); err != nil {
					return err
				}
				fmt.Fprintln(a.out, "MFA is on.")
				return nil
			},
		},
		mfaDisable,
	)

	changePw := &cobra.Command{
		Use: "password", Short: "Change your password (reads the current and new one from stdin, one per line)", Args: cobra.NoArgs,
		Annotations: op("changePassword"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			sc := bufio.NewScanner(a.in)
			var lines []string
			for len(lines) < 2 && sc.Scan() {
				lines = append(lines, sc.Text())
			}
			if len(lines) < 2 {
				return errors.New("give the current and the new password on two lines")
			}
			if err := a.do(cmd, "POST", "/api/v1/iam/password", map[string]string{"current": lines[0], "new": lines[1]}, nil); err != nil {
				return err
			}
			fmt.Fprintln(a.out, "Password changed.")
			return nil
		},
	}

	var reqMFA string
	settings := &cobra.Command{
		Use: "settings [--require-mfa true|false]", Short: "Show or change account-wide IAM settings", Args: cobra.NoArgs,
		Annotations: op("getIAMSettings", "putIAMSettings"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			var out map[string]bool
			if reqMFA != "" {
				if err := a.do(cmd, "PUT", "/api/v1/iam/settings", map[string]bool{"requireMfa": reqMFA == "true"}, &out); err != nil {
					return err
				}
			} else if err := a.do(cmd, "GET", "/api/v1/iam/settings", nil, &out); err != nil {
				return err
			}
			fmt.Fprintf(a.out, "MFA required for everyone: %v\n", out["requireMfa"])
			return nil
		},
	}
	settings.Flags().StringVar(&reqMFA, "require-mfa", "", "true or false")

	var simMFA bool
	var simIP, simCred string
	simulate := &cobra.Command{
		Use: "simulate PRINCIPAL ACTION RESOURCE", Short: "Can a user (email) or role (role:NAME) do ACTION on RESOURCE?", Args: cobra.ExactArgs(3),
		Example:     "  synctl iam simulate ann@example.com service:ScaleService srn:syncloud:project/shop/env/production/service/web",
		Annotations: op("simulatePolicy"),
		RunE: func(cmd *cobra.Command, args []string) error {
			var d struct {
				Allowed bool   `json:"allowed"`
				Reason  string `json:"reason"`
			}
			if err := a.do(cmd, "POST", "/api/v1/iam/simulate", map[string]any{"principal": args[0], "action": args[1], "resource": args[2],
				"mfa": simMFA, "sourceIp": simIP, "credentialType": simCred}, &d); err != nil {
				return err
			}
			if a.output == "json" {
				return a.printer().json(d)
			}
			word := "DENIED"
			if d.Allowed {
				word = "ALLOWED"
			}
			fmt.Fprintf(a.out, "%s: %s\n", word, d.Reason)
			return nil
		},
	}
	simulate.Flags().BoolVar(&simMFA, "mfa", false, "as if signed in with MFA")
	simulate.Flags().StringVar(&simIP, "source-ip", "", "as if from this address")
	simulate.Flags().StringVar(&simCred, "credential-type", "", "session, token, access_key, role_session")

	actions := &cobra.Command{
		Use: "actions [FILTER]", Short: "List IAM actions and the API routes they cover", Args: cobra.MaximumNArgs(1),
		Annotations: op("listActions"),
		RunE: func(cmd *cobra.Command, args []string) error {
			as, err := listOf[map[string]any](a, cmd, "/api/v1/iam/actions")
			if err != nil {
				return err
			}
			rows := [][]string{}
			for _, x := range as {
				act, _ := x["action"].(string)
				if len(args) > 0 && !strings.Contains(strings.ToLower(act), strings.ToLower(args[0])) {
					continue
				}
				m, _ := x["method"].(string)
				p, _ := x["path"].(string)
				rows = append(rows, []string{act, strings.TrimSpace(m + " " + p)})
			}
			return a.printer().table(as, []string{"ACTION", "ROUTE"}, rows)
		},
	}
	perms := &cobra.Command{
		Use: "permissions", Short: "Your policy statements", Args: cobra.NoArgs,
		Annotations: op("listMyPermissions"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			var out struct {
				Statements []struct {
					Policy   string   `json:"policy"`
					Effect   string   `json:"effect"`
					Action   []string `json:"action"`
					Resource []string `json:"resource"`
				} `json:"statements"`
				MFA      bool   `json:"mfa"`
				CredType string `json:"credentialType"`
			}
			if err := a.do(cmd, "GET", "/api/v1/iam/me/permissions", nil, &out); err != nil {
				return err
			}
			rows := [][]string{}
			for _, s := range out.Statements {
				rows = append(rows, []string{s.Policy, s.Effect, strings.Join(s.Action, ","), strings.Join(s.Resource, ",")})
			}
			return a.printer().table(out, []string{"POLICY", "EFFECT", "ACTION", "RESOURCE"}, rows)
		},
	}
	return []*cobra.Command{users, groups, policies, attach, detach, attachments, roles, mfa, changePw, settings, simulate, actions, perms}
}

func (a *app) stsCmd() *cobra.Command {
	sts := &cobra.Command{Use: "sts", Short: "Temporary credentials (security token service)"}
	var duration int
	var asEnv bool
	assume := &cobra.Command{
		Use: "assume-role ROLE", Short: "Get temporary credentials for a role (print them, or as environment variables with --env)", Args: cobra.ExactArgs(1),
		Example:     `  eval "$(synctl sts assume-role deployer --env)"`,
		Annotations: op("assumeRole"),
		RunE: func(cmd *cobra.Command, args []string) error {
			var c tempCreds
			if err := a.do(cmd, "POST", "/api/v1/sts/assume-role", map[string]any{"role": args[0], "durationSeconds": duration}, &c); err != nil {
				return err
			}
			if asEnv {
				fmt.Fprintf(a.out, "export %s=%s\nexport %s=%s\nexport %s=%s\n", EnvKeyID, c.AccessKeyID, EnvSecret, c.SecretAccessKey, EnvSessionToken, c.SessionToken)
				return nil
			}
			return a.printer().json(c)
		},
	}
	assume.Flags().IntVar(&duration, "duration", 3600, "seconds (900 up to the role's maximum)")
	assume.Flags().BoolVar(&asEnv, "env", false, "print shell exports")
	sts.AddCommand(assume)
	return sts
}

func (a *app) auditCmd() *cobra.Command {
	var actor, action, resource, text, since string
	var limit int
	var export string
	cmd := &cobra.Command{
		Use: "audit", Short: "Search the audit log (who did what, where and when)", Args: cobra.NoArgs,
		Example:     "  synctl audit --actor ann@example.com --since 24h\n  synctl audit --action 'service:*' --export audit.csv",
		Annotations: op("listAuditEvents", "exportAuditEvents"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			for k, v := range map[string]string{"actor": actor, "action": action, "resource": resource, "q": text, "since": since} {
				if v != "" {
					q.Set(k, v)
				}
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			if export != "" {
				status, body, err := c.Raw(ctx(cmd), "GET", "/api/v1/audit/export?"+q.Encode(), nil)
				if err != nil {
					return err
				}
				if status != 200 {
					return fmt.Errorf("HTTP %d: %s", status, body)
				}
				if export == "-" {
					_, err = a.out.Write(body)
					return err
				}
				return os.WriteFile(export, body, 0o600)
			}
			q.Set("limit", strconv.Itoa(limit))
			var out struct {
				Items []struct {
					At       time.Time      `json:"at"`
					Actor    string         `json:"actor"`
					Action   string         `json:"action"`
					Resource string         `json:"resource"`
					IP       string         `json:"ip"`
					Detail   map[string]any `json:"detail"`
				} `json:"items"`
			}
			if err := c.Do(ctx(cmd), "GET", "/api/v1/audit?"+q.Encode(), nil, &out); err != nil {
				return err
			}
			rows := [][]string{}
			for _, e := range out.Items {
				res := ""
				if e.Detail["denied"] == true {
					res = "DENIED"
				}
				rows = append(rows, []string{e.At.Local().Format(time.DateTime), orDash(e.Actor), e.Action, e.Resource, e.IP, orDash(res)})
			}
			return a.printer().table(out, []string{"TIME", "ACTOR", "ACTION", "RESOURCE", "IP", "RESULT"}, rows)
		},
	}
	cmd.Flags().StringVar(&actor, "actor", "", "user email or ID")
	cmd.Flags().StringVar(&action, "action", "", "action, wildcards allowed (service:*)")
	cmd.Flags().StringVar(&resource, "resource", "", "resource, wildcards allowed")
	cmd.Flags().StringVar(&text, "search", "", "free text")
	cmd.Flags().StringVar(&since, "since", "", "duration (24h) or RFC 3339 time")
	cmd.Flags().IntVar(&limit, "limit", 100, "events (at most 1000)")
	cmd.Flags().StringVar(&export, "export", "", "write all matching events as CSV to a file (- for stdout)")
	return cmd
}

// loginCmd signs synctl in through the browser (device flow): no long-lived
// keys on laptops.
func (a *app) loginCmd() *cobra.Command {
	var endpoint string
	cmd := &cobra.Command{
		Use: "login", Short: "Sign in through the dashboard and store 12-hour credentials in the profile", Args: cobra.NoArgs,
		Annotations: op("startDeviceLogin", "pollDeviceLogin"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			name := a.profile
			if name == "" {
				name = os.Getenv(EnvProfile)
			}
			if name == "" {
				name = "default"
			}
			f, err := loadCredentials()
			if err != nil {
				return err
			}
			p := f.Profiles[name]
			if endpoint != "" {
				p.Endpoint = endpoint
			} else if a.endpoint != "" {
				p.Endpoint = a.endpoint
			} else if v := os.Getenv(EnvEndpoint); v != "" {
				p.Endpoint = v
			}
			if p.Endpoint == "" {
				return errors.New("give the dashboard address: synctl login --endpoint https://…")
			}
			c, err := client.New(p.Endpoint, client.Credentials{})
			if err != nil {
				return err
			}
			var start struct {
				DeviceCode       string `json:"deviceCode"`
				UserCode         string `json:"userCode"`
				VerificationPath string `json:"verificationPath"`
				Interval         int    `json:"interval"`
				ExpiresIn        int    `json:"expiresIn"`
			}
			if err := c.Do(ctx(cmd), "POST", "/api/v1/auth/device", nil, &start); err != nil {
				return err
			}
			fmt.Fprintf(a.errOut, "Open %s%s?code=%s and confirm the code %s\n", strings.TrimRight(p.Endpoint, "/"), start.VerificationPath, start.UserCode, start.UserCode)
			deadline := time.Now().Add(time.Duration(start.ExpiresIn) * time.Second)
			body, _ := json.Marshal(map[string]string{"deviceCode": start.DeviceCode})
			for time.Now().Before(deadline) {
				select {
				case <-ctx(cmd).Done():
					return ctx(cmd).Err()
				case <-time.After(time.Duration(max(start.Interval, 1)) * time.Second):
				}
				status, raw, err := c.Raw(ctx(cmd), "POST", "/api/v1/auth/device/token", body)
				if err != nil {
					return err
				}
				if status == 202 {
					continue
				}
				if status != 200 {
					return fmt.Errorf("login failed (HTTP %d): %s", status, raw)
				}
				var creds tempCreds
				if err := json.Unmarshal(raw, &creds); err != nil {
					return err
				}
				p.AccessKeyID, p.SecretAccessKey, p.SessionToken, p.Token = creds.AccessKeyID, creds.SecretAccessKey, creds.SessionToken, ""
				exp := creds.Expiration
				p.Expiration = &exp
				if f.Profiles == nil {
					f.Profiles = map[string]Profile{}
				}
				f.Profiles[name] = p
				if _, err := saveCredentials(f); err != nil {
					return err
				}
				fmt.Fprintf(a.out, "Signed in. Profile %q is valid until %s.\n", name, exp.Local().Format(time.DateTime))
				return nil
			}
			return errors.New("the code expired before it was confirmed")
		},
	}
	cmd.Flags().StringVar(&endpoint, "endpoint", "", "dashboard address (default: the profile's)")
	return cmd
}

func (a *app) quotaCmd() *cobra.Command {
	var s scope
	q := &cobra.Command{Use: "quota", Aliases: []string{"quotas"}, Short: "Project quotas (§7.2)"}
	a.scopeFlags(q, &s)
	var envLevel bool
	var lim struct {
		CPU              float64 `json:"cpu,omitempty"`
		MemoryMiB        int     `json:"memoryMiB,omitempty"`
		Tasks            int     `json:"tasks,omitempty"`
		Services         int     `json:"services,omitempty"`
		Jobs             int     `json:"jobs,omitempty"`
		Domains          int     `json:"domains,omitempty"`
		ConcurrentBuilds int     `json:"concurrentBuilds,omitempty"`
		LogsMiBPerDay    int     `json:"logsMiBPerDay,omitempty"`
	}
	path := func() string {
		p := "/api/v1/projects/" + url.PathEscape(s.project)
		if envLevel {
			p += "/environments/" + url.PathEscape(s.env)
		}
		return p + "/quota"
	}
	type view struct {
		Project     string             `json:"project"`
		Environment string             `json:"environment"`
		Limits      map[string]float64 `json:"limits"`
		Usage       map[string]float64 `json:"usage"`
		Warnings    []string           `json:"warnings"`
	}
	show := func(vs []view) error {
		rows := [][]string{}
		f := func(u, l float64) string {
			if l == 0 {
				return strconv.FormatFloat(u, 'f', -1, 64)
			}
			return fmt.Sprintf("%s/%s", strconv.FormatFloat(u, 'f', -1, 64), strconv.FormatFloat(l, 'f', -1, 64))
		}
		for _, v := range vs {
			scope := v.Project
			if v.Environment != "" {
				scope += "/" + v.Environment
			}
			rows = append(rows, []string{scope, f(v.Usage["cpu"], v.Limits["cpu"]), f(v.Usage["memoryMiB"], v.Limits["memoryMiB"]), f(v.Usage["tasks"], v.Limits["tasks"]),
				f(v.Usage["services"], v.Limits["services"]), f(v.Usage["jobs"], v.Limits["jobs"]), f(v.Usage["domains"], v.Limits["domains"]), joinOrDash(v.Warnings)})
		}
		return a.printer().table(vs, []string{"SCOPE", "CPU", "MEMORY MIB", "TASKS", "SERVICES", "JOBS", "DOMAINS", "WARNINGS"}, rows)
	}
	set := &cobra.Command{
		Use: "set -p PROJECT [--environment-level -e ENV] --cpu N …", Short: "Set limits (0 = unlimited)", Args: cobra.NoArgs,
		Annotations: op("setQuota", "setEnvironmentQuota"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := s.need(); err != nil {
				return err
			}
			if err := a.do(cmd, "PUT", path(), lim, nil); err != nil {
				return err
			}
			fmt.Fprintln(a.out, "Quota set.")
			return nil
		},
	}
	set.Flags().Float64Var(&lim.CPU, "cpu", 0, "cores reserved")
	set.Flags().IntVar(&lim.MemoryMiB, "memory", 0, "MiB reserved")
	set.Flags().IntVar(&lim.Tasks, "tasks", 0, "desired tasks")
	set.Flags().IntVar(&lim.Services, "services", 0, "services")
	set.Flags().IntVar(&lim.Jobs, "jobs", 0, "jobs")
	set.Flags().IntVar(&lim.Domains, "domains", 0, "custom domains")
	set.Flags().IntVar(&lim.ConcurrentBuilds, "builds", 0, "builds at once (more wait in the queue)")
	set.Flags().IntVar(&lim.LogsMiBPerDay, "logs-mib-per-day", 0, "log volume per day (warned, not enforced)")
	for _, c := range []*cobra.Command{set} {
		c.Flags().BoolVar(&envLevel, "environment-level", false, "set the environment's quota (-e) instead of the project's")
	}
	unset := &cobra.Command{
		Use: "unset -p PROJECT [--environment-level -e ENV]", Short: "Remove a quota", Args: cobra.NoArgs,
		Annotations: op("deleteQuota", "deleteEnvironmentQuota"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := s.need(); err != nil {
				return err
			}
			if err := a.do(cmd, "DELETE", path(), nil, nil); err != nil {
				return err
			}
			fmt.Fprintln(a.out, "Quota removed.")
			return nil
		},
	}
	unset.Flags().BoolVar(&envLevel, "environment-level", false, "remove the environment's quota")
	q.AddCommand(
		&cobra.Command{
			Use: "list", Aliases: []string{"ls", "get"}, Short: "Quotas and usage (one project with -p)", Args: cobra.NoArgs,
			Annotations: op("listQuotas", "getQuota"),
			RunE: func(cmd *cobra.Command, _ []string) error {
				p := "/api/v1/quotas"
				if s.project != "" {
					p = "/api/v1/projects/" + url.PathEscape(s.project) + "/quota"
				}
				vs, err := listOf[view](a, cmd, p)
				if err != nil {
					return err
				}
				return show(vs)
			},
		},
		set, unset,
	)
	return q
}

func (a *app) usageCmd() *cobra.Command {
	var project, from, to, month, export string
	cmd := &cobra.Command{
		Use: "usage", Short: "Metered usage per project and day (reserved and used CPU/memory, network out, logs, build minutes)", Args: cobra.NoArgs,
		Annotations: op("getUsage", "exportUsage"),
		RunE: func(cmd *cobra.Command, _ []string) error {
			q := url.Values{}
			for k, v := range map[string]string{"project": project, "from": from, "to": to, "month": month} {
				if v != "" {
					q.Set(k, v)
				}
			}
			c, err := a.client()
			if err != nil {
				return err
			}
			if export != "" {
				status, body, err := c.Raw(ctx(cmd), "GET", "/api/v1/usage/export?"+q.Encode(), nil)
				if err != nil {
					return err
				}
				if status != 200 {
					return fmt.Errorf("HTTP %d: %s", status, body)
				}
				if export == "-" {
					_, err = a.out.Write(body)
					return err
				}
				return os.WriteFile(export, body, 0o600)
			}
			var out struct {
				Items []struct {
					Project     string  `json:"project"`
					Environment string  `json:"environment"`
					Day         string  `json:"day"`
					CPURes      float64 `json:"cpuReservedHours"`
					MemRes      float64 `json:"memReservedGiBHours"`
					CPUUsed     float64 `json:"cpuUsedHours"`
					MemUsed     float64 `json:"memUsedGiBHours"`
					NetOut      int64   `json:"netOutBytes"`
					Logs        int64   `json:"logBytes"`
					Build       int64   `json:"buildSeconds"`
				} `json:"items"`
			}
			if err := c.Do(ctx(cmd), "GET", "/api/v1/usage?"+q.Encode(), nil, &out); err != nil {
				return err
			}
			rows := [][]string{}
			f := func(v float64) string { return strconv.FormatFloat(v, 'f', 2, 64) }
			for _, u := range out.Items {
				rows = append(rows, []string{u.Day, u.Project + "/" + u.Environment, f(u.CPURes), f(u.CPUUsed), f(u.MemRes), f(u.MemUsed),
					strconv.FormatInt(u.NetOut>>20, 10), strconv.FormatInt(u.Logs>>20, 10), f(float64(u.Build) / 60)})
			}
			return a.printer().table(out, []string{"DAY", "SCOPE", "CPU RES h", "CPU USED h", "MEM RES GiB·h", "MEM USED GiB·h", "NET OUT MiB", "LOGS MiB", "BUILD MIN"}, rows)
		},
	}
	cmd.Flags().StringVarP(&project, "project", "p", "", "one project")
	cmd.Flags().StringVar(&from, "from", "", "YYYY-MM-DD")
	cmd.Flags().StringVar(&to, "to", "", "YYYY-MM-DD")
	cmd.Flags().StringVar(&month, "month", "", "YYYY-MM (the monthly report)")
	cmd.Flags().StringVar(&export, "export", "", "write CSV to a file (- for stdout)")
	return cmd
}
