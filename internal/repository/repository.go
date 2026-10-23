package repository

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
)

type Repository struct {
	Root   string
	GitDir string
}

func Open(ctx context.Context, directory string) (Repository, error) {
	root, err := git(ctx, directory, "rev-parse", "--show-toplevel")
	if err != nil {
		return Repository{}, fmt.Errorf("open Git repository: %w", err)
	}
	gitDir, err := git(ctx, root, "rev-parse", "--absolute-git-dir")
	if err != nil {
		return Repository{}, fmt.Errorf("locate Git directory: %w", err)
	}
	return Repository{Root: root, GitDir: gitDir}, nil
}

func (r Repository) CurrentBranch(ctx context.Context) (string, error) {
	branch, err := git(ctx, r.Root, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return "", fmt.Errorf("a review session requires a checked-out branch: %w", err)
	}
	return branch, nil
}

func (r Repository) DefaultRemoteHead(ctx context.Context) (string, error) {
	ref, err := git(ctx, r.Root, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD")
	if err != nil {
		return "", err
	}
	return ref, nil
}

func (r Repository) ResolveCommit(ctx context.Context, ref string) (string, error) {
	commit, err := git(ctx, r.Root, "rev-parse", "--verify", ref+"^{commit}")
	if err != nil {
		return "", err
	}
	return commit, nil
}

func (r Repository) EnableRerere(ctx context.Context) error {
	_, err := git(ctx, r.Root, "config", "--local", "rerere.enabled", "true")
	if err != nil {
		return fmt.Errorf("enable rerere: %w", err)
	}
	return nil
}

func (r Repository) CreateRef(ctx context.Context, ref string, commit string) error {
	_, err := git(ctx, r.Root, "update-ref", ref, commit)
	if err != nil {
		return fmt.Errorf("create reviewed state %q: %w", ref, err)
	}
	return nil
}

func git(ctx context.Context, directory string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", args...)
	if directory != "" {
		command.Dir = directory
	}
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}

func PathInRepository(root string, elements ...string) string {
	return filepath.Join(append([]string{root}, elements...)...)
}
