package workload

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/ridoysheikh/syncloud/internal/auth"
	"github.com/ridoysheikh/syncloud/internal/store"
)

// Project settings (Phase 15d): deploy locks, cloning environments, and
// deleting environments and projects with everything in them.

// ErrLocked means the environment's deploys are locked.
type ErrLocked struct{ Lock store.DeployLock }

func (e ErrLocked) Error() string {
	return "deploys to this environment are locked: " + e.Lock.Reason
}

// checkLock refuses a deployment to a locked or deleted environment.
func (m *Manager) checkLock(ctx context.Context, envID string) error {
	e, err := m.st.EnvironmentByID(ctx, envID)
	if err != nil {
		return err
	}
	if e.Deleting {
		return ErrInvalid{errors.New("the environment is being deleted")}
	}
	if e.Lock != nil {
		return ErrLocked{Lock: *e.Lock}
	}
	return nil
}

// finishDeletions removes an environment marked for deletion once its last
// service is gone, and then its project when that was marked too.
func (m *Manager) finishDeletions(ctx context.Context, envID string) {
	e, err := m.st.EnvironmentByID(ctx, envID)
	if err != nil || !e.Deleting {
		return
	}
	if services, dbs, err := m.st.EnvironmentContents(ctx, envID); err != nil || services > 0 || len(dbs) > 0 {
		return
	}
	if err := m.st.DeleteEnvironment(ctx, envID); err != nil {
		m.log.Error("delete environment", "environment", envID, "err", err)
		return
	}
	m.log.Info("environment deleted", "environment", e.Name)
	p, err := m.st.ProjectByID(ctx, e.ProjectID)
	if err != nil || !p.Deleting {
		return
	}
	if envs, err := m.st.ListEnvironments(ctx, p.ID); err == nil && len(envs) == 0 {
		if err := m.st.DeleteProject(ctx, p.ID); err != nil {
			m.log.Error("delete project", "project", p.Name, "err", err)
			return
		}
		m.log.Info("project deleted", "project", p.Name)
	}
}

// DeleteEnvironment deletes an environment. Without force it must be empty;
// with force its services are deleted first and the environment goes when
// they are gone. Databases are never deleted this way.
func (m *Manager) DeleteEnvironment(ctx context.Context, e store.Environment, force bool) (pending bool, err error) {
	services, dbs, err := m.st.EnvironmentContents(ctx, e.ID)
	if err != nil {
		return false, err
	}
	if len(dbs) > 0 {
		return false, ErrInvalid{fmt.Errorf("delete its databases first (%s): their data is not deleted along with an environment", joinNames(dbs))}
	}
	if services > 0 && !force {
		return false, ErrInvalid{fmt.Errorf("the environment still has %d %s: delete them, or delete everything", services, plural(services, "service"))}
	}
	if services == 0 {
		return false, m.st.DeleteEnvironment(ctx, e.ID)
	}
	if err := m.st.MarkEnvironmentDeleting(ctx, e.ID); err != nil {
		return false, err
	}
	svcs, err := m.st.ListServicesIn(ctx, e.ID)
	if err != nil {
		return true, err
	}
	for _, sv := range svcs {
		if err := m.Delete(ctx, sv.ID); err != nil && !errors.Is(err, store.ErrNotFound) {
			return true, err
		}
	}
	return true, nil
}

// DeleteProject deletes a project the same way, environment by environment.
func (m *Manager) DeleteProject(ctx context.Context, p store.Project, force bool) (pending bool, err error) {
	envs, err := m.st.ListEnvironments(ctx, p.ID)
	if err != nil {
		return false, err
	}
	var dbs []string
	total := 0
	for _, e := range envs {
		n, d, err := m.st.EnvironmentContents(ctx, e.ID)
		if err != nil {
			return false, err
		}
		total += n
		for _, x := range d {
			dbs = append(dbs, e.Name+"/"+x)
		}
	}
	if len(dbs) > 0 {
		return false, ErrInvalid{fmt.Errorf("delete its databases first (%s): their data is not deleted along with a project", joinNames(dbs))}
	}
	if total > 0 && !force {
		return false, ErrInvalid{fmt.Errorf("the project still has %d %s: delete them, or delete everything", total, plural(total, "service"))}
	}
	if total == 0 {
		return false, m.st.DeleteProject(ctx, p.ID)
	}
	if err := m.st.MarkProjectDeleting(ctx, p.ID); err != nil {
		return false, err
	}
	for _, e := range envs {
		if pending, err := m.DeleteEnvironment(ctx, e, true); err != nil {
			return true, err
		} else if !pending {
			continue // it was empty and is gone already
		}
	}
	// Every environment may have been empty: then the project goes now.
	if left, err := m.st.ListEnvironments(ctx, p.ID); err == nil && len(left) == 0 {
		return false, m.st.DeleteProject(ctx, p.ID)
	}
	return true, nil
}

func joinNames(n []string) string {
	if len(n) > 5 {
		return fmt.Sprintf("%s and %d more", joinNames(n[:5]), len(n)-5)
	}
	out := ""
	for i, x := range n {
		if i > 0 {
			out += ", "
		}
		out += x
	}
	return out
}

// CloneResult is what cloning an environment created.
type CloneResult struct {
	Environment store.Environment `json:"environment"`
	Services    []string          `json:"services"`
	// ServiceIDs maps each source service ID to its copy's (jobs and
	// security groups follow them).
	ServiceIDs map[string]string `json:"-"`
}

// CloneEnvironment creates an environment from another: its shared
// variables and every service's current spec, scaled to 0 unless start.
// Domains, public ports, Git sources, S3 bindings and databases are not
// copied.
func (m *Manager) CloneEnvironment(ctx context.Context, src store.Environment, name string, start bool, actor string) (CloneResult, error) {
	if err := ValidName(name); err != nil {
		return CloneResult{}, ErrInvalid{fmt.Errorf("environment name %w", err)}
	}
	e := store.Environment{ID: auth.NewID("env_"), ProjectID: src.ProjectID, Name: name, SharedEnv: map[string]string{}, CreatedAt: m.now().UTC().Truncate(1e9)}
	if err := m.st.CreateEnvironment(ctx, e); errors.Is(err, store.ErrNameTaken) {
		return CloneResult{}, ErrInvalid{fmt.Errorf("environment %s already exists", name)}
	} else if err != nil {
		return CloneResult{}, err
	}
	if len(src.SharedEnv) > 0 {
		if err := m.st.SetSharedEnv(ctx, e.ID, maps.Clone(src.SharedEnv)); err != nil {
			return CloneResult{}, err
		}
	}
	out := CloneResult{Environment: e, Services: []string{}, ServiceIDs: map[string]string{}}
	svcs, err := m.st.ListServicesIn(ctx, src.ID)
	if err != nil {
		return out, err
	}
	for _, sv := range svcs {
		if sv.Deleting {
			continue
		}
		spec, err := m.SpecFor(ctx, sv.ID, sv.Revision)
		if err != nil {
			return out, err
		}
		spec.SharedEnv, spec.S3, spec.RedeployedAt = nil, nil, ""
		desired := 0
		if start {
			desired = sv.DesiredCount
		}
		v, _, err := m.Apply(ctx, e, sv.Name, spec, desired, actor)
		if err != nil {
			return out, fmt.Errorf("service %s: %w", sv.Name, err)
		}
		out.Services = append(out.Services, sv.Name)
		out.ServiceIDs[sv.ID] = v.ID
	}
	slices.Sort(out.Services)
	out.Environment, _ = m.st.EnvironmentByID(ctx, e.ID)
	return out, nil
}
