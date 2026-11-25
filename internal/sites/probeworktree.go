package sites

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/phallatlassian/archwright/internal/store"
)

// ProbeWorktree makes a throwaway worktree of a Site, detached at the base
// branch the remote has, for the trust probe (docs/spec/tmux-runtime.md). It
// is made as a Job's worktrees are, from the Works' own clone under the
// worktree root, so it is the folder a later Session will be trusted in. It
// belongs to no Job: cleanup removes it, and does so even for a worktree the
// probe changed.
func (m *Manager) ProbeWorktree(siteName string) (dir string, cleanup func(), err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	site, err := m.st.GetSite(siteName)
	if err != nil {
		return "", nil, err
	}
	if _, err := git(site.ClonePath, "fetch", "--quiet", "--no-tags", "--prune", "origin"); err != nil {
		return "", nil, fmt.Errorf("fetching site %s: %w", site.Name, err)
	}
	sha, err := resolveBase(site, site.BaseBranch)
	if err != nil {
		return "", nil, err
	}
	root, err := m.realRoot()
	if err != nil {
		return "", nil, err
	}
	dir = filepath.Join(root, site.Name, "probe-"+store.NewWorktreeID())
	if inside(dir, site.ClonePath) {
		return "", nil, fmt.Errorf("probe directory %s would be inside the clone of site %s", dir, site.Name)
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		return "", nil, err
	}
	if _, err := git(site.ClonePath, "worktree", "add", "--detach", dir, sha); err != nil {
		return "", nil, err
	}
	return dir, func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		git(site.ClonePath, "worktree", "remove", "--force", dir)
		os.RemoveAll(dir) // what git left if it could not remove it
		git(site.ClonePath, "worktree", "prune")
	}, nil
}
