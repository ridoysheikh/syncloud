package gitprovider

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
)

// gitea is Gitea or Forgejo, with an access token (scopes: repository
// read/write, user read).
type gitea struct {
	h     *httpc
	token string
}

func (g *gitea) auth() [2]string { return [2]string{"Authorization", "token " + g.token} }

type gtRepo struct {
	FullName      string `json:"full_name"`
	CloneURL      string `json:"clone_url"`
	HTMLURL       string `json:"html_url"`
	DefaultBranch string `json:"default_branch"`
	Private       bool   `json:"private"`
}

func (r gtRepo) repo() Repo {
	return Repo{FullName: r.FullName, CloneURL: r.CloneURL, WebURL: r.HTMLURL, DefaultBranch: r.DefaultBranch, Private: r.Private}
}

func (g *gitea) Account(ctx context.Context) (string, error) {
	var out struct {
		Login string `json:"login"`
	}
	return out.Login, g.h.do(ctx, "GET", "/user", g.auth(), nil, &out)
}

func (g *gitea) Repos(ctx context.Context, query string) ([]Repo, error) {
	var out []Repo
	for page := 1; page <= maxRepos/50; page++ {
		var rs []gtRepo
		if err := g.h.do(ctx, "GET", fmt.Sprintf("/user/repos?limit=50&page=%d", page), g.auth(), nil, &rs); err != nil {
			return nil, err
		}
		for _, r := range rs {
			if matches(r.FullName, query) {
				out = append(out, r.repo())
			}
		}
		if len(rs) < 50 {
			break
		}
	}
	return out, nil
}

func (g *gitea) Repo(ctx context.Context, full string) (Repo, error) {
	var r gtRepo
	err := g.h.do(ctx, "GET", "/repos/"+pathEscapeRepo(full), g.auth(), nil, &r)
	return r.repo(), err
}

func (g *gitea) Branches(ctx context.Context, full string) ([]string, error) {
	var bs []struct {
		Name string `json:"name"`
	}
	if err := g.h.do(ctx, "GET", "/repos/"+pathEscapeRepo(full)+"/branches?limit=50", g.auth(), nil, &bs); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(bs))
	for _, b := range bs {
		out = append(out, b.Name)
	}
	return out, nil
}

// Token: Gitea accepts an access token as the password with any user name.
func (g *gitea) Token(context.Context, string) (string, error) { return g.token, nil }

func (g *gitea) CreateHook(ctx context.Context, full, hookURL, secret string) (string, error) {
	var out struct {
		ID int64 `json:"id"`
	}
	body := map[string]any{"type": "gitea", "active": true, "events": []string{"push", "create"},
		"config": map[string]string{"url": hookURL, "content_type": "json", "secret": secret}}
	if err := g.h.do(ctx, "POST", "/repos/"+pathEscapeRepo(full)+"/hooks", g.auth(), body, &out); err != nil {
		return "", err
	}
	return strconv.FormatInt(out.ID, 10), nil
}

func (g *gitea) DeleteHook(ctx context.Context, full, id string) error {
	if id == "" {
		return nil
	}
	err := g.h.do(ctx, "DELETE", "/repos/"+pathEscapeRepo(full)+"/hooks/"+url.PathEscape(id), g.auth(), nil, nil)
	if IsNotFound(err) {
		return nil
	}
	return err
}

func (g *gitea) SetStatus(ctx context.Context, full, sha string, s Status) error {
	state := map[string]string{StatePending: "pending", StateRunning: "pending", StateSuccess: "success", StateFailure: "failure"}[s.State]
	body := map[string]string{"state": state, "context": s.Context, "description": truncate(s.Description, 255)}
	if s.TargetURL != "" {
		body["target_url"] = s.TargetURL
	}
	return g.h.do(ctx, "POST", "/repos/"+pathEscapeRepo(full)+"/statuses/"+url.PathEscape(sha), g.auth(), body, nil)
}
