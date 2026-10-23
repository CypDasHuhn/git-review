package repository

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/CypDasHuhn/git-review/internal/session"
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

func (r Repository) AcceptPaths(ctx context.Context, ref string, paths []string, subject string) (bool, error) {
	head, err := r.ResolveCommit(ctx, ref)
	if err != nil {
		return false, err
	}
	index, err := os.CreateTemp("", "git-review-index-*")
	if err != nil {
		return false, fmt.Errorf("create temporary index: %w", err)
	}
	indexPath := index.Name()
	if err := index.Close(); err != nil {
		return false, err
	}
	if err := os.Remove(indexPath); err != nil {
		return false, err
	}
	defer os.Remove(indexPath)
	env := map[string]string{"GIT_INDEX_FILE": indexPath}
	if _, err := gitWithEnv(ctx, r.Root, env, "read-tree", head); err != nil {
		return false, err
	}
	args := append([]string{"add", "-A", "--"}, paths...)
	if _, err := gitWithEnv(ctx, r.Root, env, args...); err != nil {
		return false, err
	}
	tree, err := gitWithEnv(ctx, r.Root, env, "write-tree")
	if err != nil {
		return false, err
	}
	headTree, err := git(ctx, r.Root, "rev-parse", head+"^{tree}")
	if err != nil {
		return false, err
	}
	if tree == headTree {
		return false, nil
	}
	commit, err := git(ctx, r.Root, "commit-tree", tree, "-p", head, "-m", "accept: "+subject)
	if err != nil {
		return false, err
	}
	if _, err := git(ctx, r.Root, "update-ref", ref, commit, head); err != nil {
		return false, err
	}
	return true, nil
}

func (r Repository) UndoAcceptance(ctx context.Context, ref string) (bool, string, error) {
	head, err := r.ResolveCommit(ctx, ref)
	if err != nil {
		return false, "", err
	}
	subject, err := git(ctx, r.Root, "log", "-1", "--format=%s", head)
	if err != nil {
		return false, "", err
	}
	if !strings.HasPrefix(subject, "accept: ") {
		return false, "nothing to un-accept", nil
	}
	parent, err := git(ctx, r.Root, "rev-parse", head+"^")
	if err != nil {
		return false, "", err
	}
	if _, err := git(ctx, r.Root, "update-ref", ref, parent, head); err != nil {
		return false, "", err
	}
	return true, subject, nil
}

func (r Repository) ResetRef(ctx context.Context, ref, commit string) error {
	current, err := r.ResolveCommit(ctx, ref)
	if err != nil {
		return err
	}
	if _, err = git(ctx, r.Root, "update-ref", ref, commit, current); err != nil {
		return err
	}
	return nil
}

func (r Repository) Log(ctx context.Context, ref string) (string, error) {
	return git(ctx, r.Root, "log", "--format=%H%x09%h%x09%cr%x09%s", ref)
}

func (r Repository) DiffHunks(ctx context.Context, base, target string) ([]session.Hunk, error) {
	output, err := git(ctx, r.Root, "diff", "--no-ext-diff", "--unified=0", base+".."+target, "--")
	if err != nil {
		return nil, err
	}
	var hunks []session.Hunk
	path := ""
	oldPath := ""
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "diff --git ") {
			path = ""
			oldPath = ""
			continue
		}
		if strings.HasPrefix(line, "+++ b/") {
			path = strings.TrimPrefix(line, "+++ b/")
			continue
		}
		if strings.HasPrefix(line, "--- a/") {
			oldPath = strings.TrimPrefix(line, "--- a/")
			if path == "" {
				path = oldPath
			}
			continue
		}
		if !strings.HasPrefix(line, "@@ ") {
			continue
		}
		oldStart, oldLines, newStart, newLines, ok := parseHunkHeader(line)
		if !ok || path == "" {
			continue
		}
		hunks = append(hunks, session.Hunk{
			Path: path, OldPath: oldPath, OldStart: oldStart, OldLines: oldLines, NewStart: newStart, NewLines: newLines,
		})
	}
	return hunks, nil
}

func (r Repository) CreateGroupProjection(ctx context.Context, base, target, ref, name string, hunks []session.Hunk) (string, error) {
	patch, err := r.patchForHunks(ctx, base, target, hunks)
	if err != nil {
		return "", err
	}
	if patch == "" {
		return "", fmt.Errorf("review group %q has no current changes", name)
	}
	index, err := os.CreateTemp("", "git-review-index-*")
	if err != nil {
		return "", fmt.Errorf("create temporary index: %w", err)
	}
	indexPath := index.Name()
	if err := index.Close(); err != nil {
		return "", err
	}
	if err := os.Remove(indexPath); err != nil {
		return "", err
	}
	defer os.Remove(indexPath)
	env := map[string]string{"GIT_INDEX_FILE": indexPath}
	if _, err := gitWithEnv(ctx, r.Root, env, "read-tree", base); err != nil {
		return "", err
	}
	if _, err := gitWithInput(ctx, r.Root, env, patch, "apply", "--cached", "--unidiff-zero"); err != nil {
		return "", err
	}
	for _, hunk := range hunks {
		if hunk.OldPath != "" && hunk.OldPath != hunk.Path {
			if _, err := gitWithEnv(ctx, r.Root, env, "update-index", "--force-remove", "--", hunk.OldPath); err != nil {
				return "", err
			}
		}
	}
	tree, err := gitWithEnv(ctx, r.Root, env, "write-tree")
	if err != nil {
		return "", err
	}
	commit, err := git(ctx, r.Root, "commit-tree", tree, "-p", base, "-m", "review group: "+name)
	if err != nil {
		return "", err
	}
	if _, err := git(ctx, r.Root, "update-ref", ref, commit); err != nil {
		return "", err
	}
	return commit, nil
}

func (r Repository) patchForHunks(ctx context.Context, base, target string, selected []session.Hunk) (string, error) {
	output, err := git(ctx, r.Root, "diff", "--no-ext-diff", "--unified=0", base+".."+target, "--")
	if err != nil {
		return "", err
	}
	wanted := map[session.Hunk]bool{}
	for _, hunk := range selected {
		wanted[hunk] = true
	}
	var result, prefix, hunk []string
	path := ""
	oldPath := ""
	addedPrefix := false
	flush := func() {
		if len(hunk) == 0 {
			return
		}
		oldStart, oldLines, newStart, newLines, ok := parseHunkHeader(hunk[0])
		if ok && wanted[session.Hunk{Path: path, OldPath: oldPath, OldStart: oldStart, OldLines: oldLines, NewStart: newStart, NewLines: newLines}] {
			if !addedPrefix {
				result = append(result, prefix...)
				addedPrefix = true
			}
			result = append(result, hunk...)
		}
		hunk = nil
	}
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "diff --git ") {
			flush()
			prefix = []string{line}
			path = ""
			oldPath = ""
			addedPrefix = false
			continue
		}
		if strings.HasPrefix(line, "@@ ") {
			flush()
			hunk = []string{line}
			continue
		}
		if len(hunk) > 0 {
			hunk = append(hunk, line)
			continue
		}
		prefix = append(prefix, line)
		if strings.HasPrefix(line, "+++ b/") {
			path = strings.TrimPrefix(line, "+++ b/")
		}
		if strings.HasPrefix(line, "--- a/") {
			oldPath = strings.TrimPrefix(line, "--- a/")
			if path == "" {
				path = oldPath
			}
		}
	}
	flush()
	return strings.Join(result, "\n") + "\n", nil
}

var hunkHeader = regexp.MustCompile(`^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@`)

func parseHunkHeader(header string) (int, int, int, int, bool) {
	matches := hunkHeader.FindStringSubmatch(header)
	if matches == nil {
		return 0, 0, 0, 0, false
	}
	parse := func(value string, fallback int) (int, bool) {
		if value == "" {
			return fallback, true
		}
		parsed, err := strconv.Atoi(value)
		return parsed, err == nil
	}
	oldStart, ok := parse(matches[1], 0)
	if !ok {
		return 0, 0, 0, 0, false
	}
	oldLines, ok := parse(matches[2], 1)
	if !ok {
		return 0, 0, 0, 0, false
	}
	newStart, ok := parse(matches[3], 0)
	if !ok {
		return 0, 0, 0, 0, false
	}
	newLines, ok := parse(matches[4], 1)
	if !ok {
		return 0, 0, 0, 0, false
	}
	return oldStart, oldLines, newStart, newLines, true
}

func git(ctx context.Context, directory string, args ...string) (string, error) {
	return gitWithEnv(ctx, directory, nil, args...)
}

func gitWithEnv(ctx context.Context, directory string, env map[string]string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", args...)
	if directory != "" {
		command.Dir = directory
	}
	if len(env) > 0 {
		command.Env = os.Environ()
		for key, value := range env {
			command.Env = append(command.Env, key+"="+value)
		}
	}
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}

func gitWithInput(ctx context.Context, directory string, env map[string]string, input string, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = directory
	command.Env = os.Environ()
	for key, value := range env {
		command.Env = append(command.Env, key+"="+value)
	}
	command.Stdin = strings.NewReader(input)
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}

func PathInRepository(root string, elements ...string) string {
	return filepath.Join(append([]string{root}, elements...)...)
}
