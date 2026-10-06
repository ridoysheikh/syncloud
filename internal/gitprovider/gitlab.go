package gitprovider

import (
	"context"
	"fmt"
	"net/url"
	"strconv"
)

// gitlab is gitlab.com or a self-managed GitLab, with a personal, group or
// project access token (scope api).
type gitlab struct {
	h     *httpc
	token string
}

func (g *gitlab) auth() [2]string { return [2]string{"PRIVATE-TOKEN", g.token} }

// project addresses a project by its URL-encoded path.
func project(full string) string { return "/projects/" + url.PathEscape(full) }

type glProject struct {
	Path          string `json:"path_with_namespace"`
	HTTPURL       string `json:"http_url_to_repo"`
	WebURL        string `json:"web_url"`
	DefaultBranch string `json:"default_branch"`
	Visibility    string `json:"visibility"`
}

func (p glProject) repo() Repo {
	return Repo{FullName: p.Path, CloneURL: p.HTTPURL, WebURL: p.WebURL, DefaultBranch: p.DefaultBranch, Private: p.Visibility != "public"}
}

func (g *gitlab) Account(ctx context.Context) (string, error) {
	var out struct {
		Username string `json:"username"`
	}
	return out.Username, g.h.do(ctx, "GET", "/user", g.auth(), nil, &out)
}

func (g *gitlab) Repos(ctx context.Context, query string) ([]Repo, error) {
	var out []Repo
	for page := 1; page <= maxRepos/100; page++ {
		var ps []glProject
		path := fmt.Sprintf("/projects?membership=true&simple=true&per_page=100&order_by=last_activity_at&page=%d", page)
		if query != "" {
			path += "&search=" + url.QueryEscape(query) + "&search_namespaces=true"
		}
		if err := g.h.do(ctx, "GET", path, g.auth(), nil, &ps); err != nil {
			return nil, err
		}
		for _, p := range ps {
			out = append(out, p.repo())
		}
		if len(ps) < 100 {
			break
		}
	}
	return out, nil
}

func (g *gitlab) Repo(ctx context.Context, full string) (Repo, error) {
	var p glProject
	err := g.h.do(ctx, "GET", project(full), g.auth(), nil, &p)
	return p.repo(), err
}

func (g *gitlab) Branches(ctx context.Context, full string) ([]string, error) {
	var bs []struct {
		Name string `json:"name"`
	}
	if err := g.h.do(ctx, "GET", project(full)+"/repository/branches?per_page=100", g.auth(), nil, &bs); err != nil {
		return nil, err
	}
	out := make([]string, 0, len(bs))
	for _, b := range bs {
		out = append(out, b.Name)
	}
	return out, nil
}

// Token: GitLab accepts an access token as the password with any user name.
func (g *gitlab) Token(context.Context, string) (string, error) { return g.token, nil }

func (g *gitlab) CreateHook(ctx context.Context, full, hookURL, secret string) (string, error) {
	var out struct {
		ID int64 `json:"id"`
	}
	body := map[string]any{"url": hookURL, "push_events": true, "tag_push_events": true, "token": secret, "enable_ssl_verification": true}
	if err := g.h.do(ctx, "POST", project(full)+"/hooks", g.auth(), body, &out); err != nil {
		return "", err
	}
	return strconv.FormatInt(out.ID, 10), nil
}

func (g *gitlab) DeleteHook(ctx context.Context, full, id string) error {
	if id == "" {
		return nil
	}
	err := g.h.do(ctx, "DELETE", project(full)+"/hooks/"+url.PathEscape(id), g.auth(), nil, nil)
	if IsNotFound(err) {
		return nil
	}
	return err
}

func (g *gitlab) SetStatus(ctx context.Context, full, sha string, s Status) error {
	state := map[string]string{StatePending: "pending", StateRunning: "running", StateSuccess: "success", StateFailure: "failed"}[s.State]
	body := map[string]string{"state": state, "name": s.Context, "description": truncate(s.Description, 255)}
	if s.TargetURL != "" {
		body["target_url"] = s.TargetURL
	}
	return g.h.do(ctx, "POST", project(full)+"/statuses/"+url.PathEscape(sha), g.auth(), body, nil)
}
