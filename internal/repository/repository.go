package repository

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/CypDasHuhn/git-review/internal/session"
)

const emptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

type Repository struct {
	Root   string
	GitDir string
}

type FragmentPatch struct {
	Fragment session.Fragment
	OldLines []string
	NewLines []string
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

func (r Repository) CommitInfo(ctx context.Context, ref string) (session.Commit, error) {
	commit, err := r.ResolveCommit(ctx, ref)
	if err != nil {
		return session.Commit{}, err
	}
	shortCommit, err := git(ctx, r.Root, "rev-parse", "--short", commit)
	if err != nil {
		return session.Commit{}, err
	}
	return session.Commit{Hash: commit, ShortHash: shortCommit}, nil
}

func (r Repository) ParentCommit(ctx context.Context, commit string) (string, error) {
	parent, err := git(ctx, r.Root, "rev-parse", commit+"^1")
	if err != nil {
		return emptyTree, nil
	}
	return parent, nil
}

func (r Repository) CommitFragments(ctx context.Context, commit string) ([]FragmentPatch, error) {
	parent, err := r.ParentCommit(ctx, commit)
	if err != nil {
		return nil, err
	}
	shortCommit, err := git(ctx, r.Root, "rev-parse", "--short", commit)
	if err != nil {
		return nil, err
	}
	output, err := git(ctx, r.Root, "diff", "--no-ext-diff", "--no-color", "--diff-algorithm=myers", "--unified=0", "-M", parent+".."+commit, "--")
	if err != nil {
		return nil, err
	}
	return parseFragmentPatches(output, commit, parent, shortCommit), nil
}

func (r Repository) ReviewFragments(ctx context.Context, base, target string) ([]session.Fragment, error) {
	commitsOutput, err := git(ctx, r.Root, "rev-list", "--reverse", "--no-merges", base+".."+target)
	if err != nil {
		return nil, err
	}
	var fragments []session.Fragment
	for _, commit := range strings.Fields(commitsOutput) {
		patches, err := r.CommitFragments(ctx, commit)
		if err != nil {
			return nil, err
		}
		for _, patch := range patches {
			fragments = append(fragments, patch.Fragment)
		}
	}
	return fragments, nil
}

func (r Repository) FragmentPatch(ctx context.Context, fragment session.Fragment) (FragmentPatch, error) {
	patches, err := r.CommitFragments(ctx, fragment.Commit)
	if err != nil {
		return FragmentPatch{}, err
	}
	for _, patch := range patches {
		if patch.Fragment.ID() == fragment.ID() {
			return patch, nil
		}
	}
	return FragmentPatch{}, fmt.Errorf("fragment %s no longer exists in commit %s", fragment.Label(), fragment.Commit)
}

func (r Repository) EnableRerere(ctx context.Context) error {
	_, err := git(ctx, r.Root, "config", "--local", "rerere.enabled", "true")
	if err != nil {
		return fmt.Errorf("enable rerere: %w", err)
	}
	return nil
}

func (r Repository) InstallPostMergeHook(ctx context.Context) error {
	hooksDirectory, err := git(ctx, r.Root, "rev-parse", "--git-path", "hooks")
	if err != nil {
		return fmt.Errorf("locate Git hooks directory: %w", err)
	}
	if !filepath.IsAbs(hooksDirectory) {
		hooksDirectory = filepath.Join(r.Root, hooksDirectory)
	}
	if err := os.MkdirAll(hooksDirectory, 0o755); err != nil {
		return fmt.Errorf("create Git hooks directory: %w", err)
	}
	hookPath := filepath.Join(hooksDirectory, "post-merge")
	const hookCommand = "sync-base --hook || true"
	contents, err := os.ReadFile(hookPath)
	if errors.Is(err, os.ErrNotExist) {
		contents = []byte("#!/bin/sh\n")
	} else if err != nil {
		return fmt.Errorf("read post-merge hook: %w", err)
	}
	if strings.Contains(string(contents), hookCommand) {
		return nil
	}
	if len(contents) > 0 && contents[len(contents)-1] != '\n' {
		contents = append(contents, '\n')
	}
	executable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("locate git-review executable: %w", err)
	}
	contents = append(contents, []byte(shellQuote(executable)+" "+hookCommand+"\n")...)
	if err := os.WriteFile(hookPath, contents, 0o755); err != nil {
		return fmt.Errorf("write post-merge hook: %w", err)
	}
	if err := os.Chmod(hookPath, 0o755); err != nil {
		return fmt.Errorf("make post-merge hook executable: %w", err)
	}
	return nil
}

func (r Repository) CreateRef(ctx context.Context, ref, commit string) error {
	_, err := git(ctx, r.Root, "update-ref", ref, commit)
	if err != nil {
		return fmt.Errorf("create ref %q: %w", ref, err)
	}
	return nil
}

func (r Repository) IsAncestor(ctx context.Context, ancestor, descendant string) (bool, error) {
	command := exec.CommandContext(ctx, "git", "merge-base", "--is-ancestor", ancestor, descendant)
	command.Dir = r.Root
	output, err := command.CombinedOutput()
	if err == nil {
		return true, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) && exitError.ExitCode() == 1 {
		return false, nil
	}
	return false, fmt.Errorf("git merge-base --is-ancestor %s %s: %s", ancestor, descendant, strings.TrimSpace(string(output)))
}

func (r Repository) MergeRef(ctx context.Context, ref, other, subject string) (string, error) {
	head, err := r.ResolveCommit(ctx, ref)
	if err != nil {
		return "", err
	}
	treeOutput, err := git(ctx, r.Root, "merge-tree", "--write-tree", "--messages", head, other)
	if err != nil {
		return "", fmt.Errorf("merge %s into %s: %w", other, ref, err)
	}
	tree := strings.SplitN(treeOutput, "\n", 2)[0]
	commit, err := git(ctx, r.Root, "commit-tree", tree, "-p", head, "-p", other, "-m", subject)
	if err != nil {
		return "", fmt.Errorf("create merge commit for %s: %w", ref, err)
	}
	if _, err := git(ctx, r.Root, "update-ref", ref, commit, head); err != nil {
		return "", err
	}
	return commit, nil
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
	if !strings.HasPrefix(subject, "accept ") && !strings.HasPrefix(subject, "accept:") {
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

func (r Repository) ApplyFragment(ctx context.Context, ref string, patch FragmentPatch) (string, error) {
	head, err := r.ResolveCommit(ctx, ref)
	if err != nil {
		return "", err
	}
	sourcePath := patch.Fragment.Path
	if patch.Fragment.OldPath != "" {
		sourcePath = patch.Fragment.OldPath
	}
	sourceData, sourceExists, err := r.treeFile(ctx, patch.Fragment.Parent, sourcePath)
	if err != nil {
		return "", err
	}
	currentPath := patch.Fragment.Path
	currentData, currentExists, err := r.treeFile(ctx, head, currentPath)
	if err != nil {
		return "", err
	}
	if !currentExists && patch.Fragment.OldPath != "" && patch.Fragment.OldPath != patch.Fragment.Path {
		currentData, currentExists, err = r.treeFile(ctx, head, patch.Fragment.OldPath)
		if err != nil {
			return "", err
		}
		currentPath = patch.Fragment.OldPath
	}
	sourceLines, _ := splitLines(sourceData)
	currentLines, currentNewline := splitLines(currentData)
	if !sourceExists {
		sourceLines = nil
	}
	if !currentExists {
		currentLines = nil
		currentNewline = true
	}
	position, matchedLines, err := locateEdit(sourceLines, currentLines, patch.Fragment.OldStart, patch.Fragment.OldLines)
	if err != nil {
		if isMissingEditContext(err) && fragmentAlreadyApplied(currentLines, patch.NewLines) {
			return head, nil
		}
		return "", fmt.Errorf("locate fragment %s: %w", patch.Fragment.Label(), err)
	}
	if matchedLines > 0 && position+matchedLines > len(currentLines) {
		return "", fmt.Errorf("fragment %s exceeds current file", patch.Fragment.Label())
	}
	updated := make([]string, 0, len(currentLines)-matchedLines+len(patch.NewLines))
	updated = append(updated, currentLines[:position]...)
	updated = append(updated, patch.NewLines...)
	updated = append(updated, currentLines[position+matchedLines:]...)
	trailingNewline := currentNewline
	targetData, targetExists, err := r.treeFile(ctx, patch.Fragment.Commit, patch.Fragment.Path)
	if err != nil {
		return "", err
	}
	if targetExists {
		targetLines, targetNewline := splitLines(targetData)
		if patch.Fragment.NewLines > 0 && patch.Fragment.NewStart-1+patch.Fragment.NewLines >= len(targetLines) {
			trailingNewline = targetNewline
		}
	}
	indexPath, err := temporaryIndex()
	if err != nil {
		return "", err
	}
	defer os.Remove(indexPath)
	env := map[string]string{"GIT_INDEX_FILE": indexPath}
	if _, err := gitWithEnv(ctx, r.Root, env, "read-tree", head); err != nil {
		return "", err
	}
	if patch.Fragment.OldPath != "" && patch.Fragment.OldPath != patch.Fragment.Path {
		if _, err := gitWithEnv(ctx, r.Root, env, "update-index", "--force-remove", "--", patch.Fragment.OldPath); err != nil {
			return "", err
		}
	}
	if !targetExists {
		if _, err := gitWithEnv(ctx, r.Root, env, "update-index", "--force-remove", "--", patch.Fragment.Path); err != nil {
			return "", err
		}
	} else {
		data := joinLines(updated, trailingNewline)
		blob, err := gitWithInput(ctx, r.Root, env, data, "hash-object", "-w", "--stdin")
		if err != nil {
			return "", err
		}
		mode, err := r.fileMode(ctx, patch.Fragment.Commit, patch.Fragment.Path)
		if err != nil {
			mode, err = r.fileMode(ctx, head, currentPath)
			if err != nil {
				return "", err
			}
		}
		cacheInfo := mode + "," + blob + "," + patch.Fragment.Path
		if _, err := gitWithEnv(ctx, r.Root, env, "update-index", "--add", "--cacheinfo", cacheInfo); err != nil {
			return "", err
		}
	}
	tree, err := gitWithEnv(ctx, r.Root, env, "write-tree")
	if err != nil {
		return "", err
	}
	commit, err := git(ctx, r.Root, "commit-tree", tree, "-p", head, "-m", "accept "+patch.Fragment.Label())
	if err != nil {
		return "", err
	}
	if _, err := git(ctx, r.Root, "update-ref", ref, commit, head); err != nil {
		return "", err
	}
	return commit, nil
}

func isMissingEditContext(err error) bool {
	message := err.Error()
	return strings.Contains(message, "source lines are not present") || strings.Contains(message, "insertion anchors are ambiguous or missing")
}

func fragmentAlreadyApplied(current, newLines []string) bool {
	if len(newLines) == 0 {
		return true
	}
	if len(sequencePositions(current, newLines)) == 1 {
		return true
	}
	return len(normalizedSequencePositions(current, newLines)) == 1
}

func (r Repository) treeFile(ctx context.Context, ref, path string) ([]byte, bool, error) {
	if ref == "" {
		return nil, false, nil
	}
	data, err := gitRaw(ctx, r.Root, "show", ref+":"+path)
	if err != nil {
		return nil, false, nil
	}
	return data, true, nil
}

func (r Repository) fileMode(ctx context.Context, ref, path string) (string, error) {
	output, err := git(ctx, r.Root, "ls-tree", ref, "--", path)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(output)
	if len(fields) < 1 {
		return "", fmt.Errorf("file %q does not exist in %s", path, ref)
	}
	return fields[0], nil
}

func parseFragmentPatches(output, commit, parent, shortCommit string) []FragmentPatch {
	var patches []FragmentPatch
	path := ""
	oldPath := ""
	header := ""
	var lines []string
	flush := func() {
		if header == "" || path == "" {
			return
		}
		oldStart, oldLines, newStart, newLines, ok := parseHunkHeader(header)
		if !ok {
			header = ""
			lines = nil
			return
		}
		var oldContent, newContent []string
		for _, line := range lines {
			if line == "\\ No newline at end of file" || line == "" {
				continue
			}
			switch line[0] {
			case '-':
				oldContent = append(oldContent, line[1:])
			case '+':
				newContent = append(newContent, line[1:])
			}
		}
		patches = append(patches, FragmentPatch{
			Fragment: session.Fragment{
				Commit: commit, ShortCommit: shortCommit, Parent: parent, Path: path, OldPath: oldPath,
				OldStart: oldStart, OldLines: oldLines, NewStart: newStart, NewLines: newLines,
			},
			OldLines: oldContent,
			NewLines: newContent,
		})
		header = ""
		lines = nil
	}
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "diff --git ") {
			flush()
			path = ""
			oldPath = ""
			continue
		}
		if strings.HasPrefix(line, "@@ ") {
			flush()
			header = line
			continue
		}
		if header != "" {
			lines = append(lines, line)
			continue
		}
		if strings.HasPrefix(line, "--- ") {
			oldPath = diffPath(line[4:], "a/")
			if oldPath != "" && path == "" {
				path = oldPath
			}
			continue
		}
		if strings.HasPrefix(line, "+++ ") {
			newPath := diffPath(line[4:], "b/")
			if newPath == "" {
				path = oldPath
			} else {
				path = newPath
			}
		}
	}
	flush()
	return patches
}

func diffPath(value, prefix string) string {
	value = strings.TrimSpace(value)
	if value == "/dev/null" {
		return ""
	}
	return strings.TrimPrefix(value, prefix)
}

func splitLines(data []byte) ([]string, bool) {
	if len(data) == 0 {
		return nil, false
	}
	trailingNewline := data[len(data)-1] == '\n'
	text := string(data)
	if trailingNewline {
		text = text[:len(text)-1]
	}
	if text == "" {
		return []string{}, trailingNewline
	}
	return strings.Split(text, "\n"), trailingNewline
}

func joinLines(lines []string, trailingNewline bool) []byte {
	text := strings.Join(lines, "\n")
	if trailingNewline {
		text += "\n"
	}
	return []byte(text)
}

type lineRange struct {
	start int
	end   int
}

func locateEdit(source, current []string, start, count int) (int, int, error) {
	position := start - 1
	if count == 0 {
		position = start
	}
	if position < 0 || position > len(source) || position+count > len(source) {
		return 0, 0, errors.New("source range is invalid")
	}
	if sameLines(source, current) {
		return position, count, nil
	}
	old := source[position : position+count]
	if count > 0 {
		candidates := sequencePositions(current, old)
		if len(candidates) == 0 {
			normalized := normalizedSequencePositions(current, old)
			if len(normalized) == 1 {
				return normalized[0].start, normalized[0].end - normalized[0].start, nil
			}
			if len(normalized) > 1 {
				return 0, 0, errors.New("source lines are ambiguous in the reviewed state")
			}
		}
		if len(candidates) == 1 {
			return candidates[0], count, nil
		}
		for size := 3; size > 0; size-- {
			before := source[max(0, position-size):position]
			after := source[position+count : min(len(source), position+count+size)]
			filtered := filterPositions(current, candidates, len(old), before, after)
			if len(filtered) == 1 {
				return filtered[0], count, nil
			}
			if len(before) > 0 {
				filtered = filterBeforePositions(current, candidates, before)
				if len(filtered) == 1 {
					return filtered[0], count, nil
				}
			}
			if len(after) > 0 {
				filtered = filterAfterPositions(current, candidates, len(old), after)
				if len(filtered) == 1 {
					return filtered[0], count, nil
				}
			}
		}
		if len(candidates) == 0 {
			return 0, 0, errors.New("source lines are not present in the reviewed state")
		}
		return 0, 0, errors.New("source lines are ambiguous in the reviewed state")
	}
	for size := 3; size > 0; size-- {
		before := source[max(0, position-size):position]
		after := source[position:min(len(source), position+size)]
		candidates := boundaryPositions(current, before, after)
		if len(candidates) == 1 {
			return candidates[0], 0, nil
		}
	}
	before := source[max(0, position-3):position]
	after := source[position:min(len(source), position+3)]
	candidates := fuzzyBoundaryPositions(current, before, after)
	if len(candidates) == 1 {
		return candidates[0], 0, nil
	}
	return 0, 0, errors.New("insertion anchors are ambiguous or missing in the reviewed state")
}

func normalizedSequencePositions(lines, sequence []string) []lineRange {
	target := normalizedLines(sequence)
	if target == "" {
		return nil
	}
	var positions []lineRange
	for start := range lines {
		var builder strings.Builder
		for end := start; end < len(lines); end++ {
			if builder.Len() > 0 {
				builder.WriteByte(' ')
			}
			builder.WriteString(strings.TrimSpace(lines[end]))
			value := builder.String()
			if value == target {
				positions = append(positions, lineRange{start: start, end: end + 1})
				break
			}
			if len(value) >= len(target) {
				break
			}
		}
	}
	return positions
}

func normalizedLines(lines []string) string {
	values := make([]string, len(lines))
	for index, line := range lines {
		values[index] = strings.TrimSpace(line)
	}
	return strings.Join(values, " ")
}

func fuzzyBoundaryPositions(lines, before, after []string) []int {
	beforePositions := sequencePositions(lines, before)
	afterPositions := sequencePositions(lines, after)
	var candidates []int
	seen := map[int]bool{}
	for _, beforePosition := range beforePositions {
		beforeEnd := beforePosition + len(before)
		for _, afterPosition := range afterPositions {
			if beforeEnd > afterPosition {
				continue
			}
			if !seen[afterPosition] {
				seen[afterPosition] = true
				candidates = append(candidates, afterPosition)
			}
		}
	}
	return candidates
}

func sequencePositions(lines, sequence []string) []int {
	if len(sequence) == 0 {
		return nil
	}
	var positions []int
	for start := 0; start+len(sequence) <= len(lines); start++ {
		if sameLines(lines[start:start+len(sequence)], sequence) {
			positions = append(positions, start)
		}
	}
	return positions
}

func boundaryPositions(lines, before, after []string) []int {
	var positions []int
	for position := 0; position <= len(lines); position++ {
		if len(before) > position || len(after) > len(lines)-position {
			continue
		}
		if !sameLines(lines[position-len(before):position], before) {
			continue
		}
		if !sameLines(lines[position:position+len(after)], after) {
			continue
		}
		positions = append(positions, position)
	}
	return positions
}

func filterPositions(lines []string, positions []int, length int, before, after []string) []int {
	var filtered []int
	for _, position := range positions {
		if len(before) > position || len(after) > len(lines)-position-length {
			continue
		}
		if !sameLines(lines[position-len(before):position], before) {
			continue
		}
		if !sameLines(lines[position+length:position+length+len(after)], after) {
			continue
		}
		filtered = append(filtered, position)
	}
	return filtered
}

func filterBeforePositions(lines []string, positions []int, before []string) []int {
	var filtered []int
	for _, position := range positions {
		if len(before) > position {
			continue
		}
		if sameLines(lines[position-len(before):position], before) {
			filtered = append(filtered, position)
		}
	}
	return filtered
}

func filterAfterPositions(lines []string, positions []int, length int, after []string) []int {
	var filtered []int
	for _, position := range positions {
		if len(after) > len(lines)-position-length {
			continue
		}
		if sameLines(lines[position+length:position+length+len(after)], after) {
			filtered = append(filtered, position)
		}
	}
	return filtered
}

func sameLines(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func max(left, right int) int {
	if left > right {
		return left
	}
	return right
}

func min(left, right int) int {
	if left < right {
		return left
	}
	return right
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

func temporaryIndex() (string, error) {
	index, err := os.CreateTemp("", "git-review-index-*")
	if err != nil {
		return "", fmt.Errorf("create temporary index: %w", err)
	}
	path := index.Name()
	if err := index.Close(); err != nil {
		return "", err
	}
	if err := os.Remove(path); err != nil {
		return "", err
	}
	return path, nil
}

func git(ctx context.Context, directory string, args ...string) (string, error) {
	output, err := gitRaw(ctx, directory, args...)
	return strings.TrimSpace(string(output)), err
}

func gitRaw(ctx context.Context, directory string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, "git", args...)
	if directory != "" {
		command.Dir = directory
	}
	output, err := command.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(output)))
	}
	return output, nil
}

func gitWithEnv(ctx context.Context, directory string, env map[string]string, args ...string) (string, error) {
	output, err := gitWithEnvRaw(ctx, directory, env, args...)
	return strings.TrimSpace(string(output)), err
}

func gitWithEnvRaw(ctx context.Context, directory string, env map[string]string, args ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, "git", args...)
	if directory != "" {
		command.Dir = directory
	}
	command.Env = os.Environ()
	for key, value := range env {
		command.Env = append(command.Env, key+"="+value)
	}
	output, err := command.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(output)))
	}
	return output, nil
}

func gitWithInput(ctx context.Context, directory string, env map[string]string, input []byte, args ...string) (string, error) {
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = directory
	command.Env = os.Environ()
	for key, value := range env {
		command.Env = append(command.Env, key+"="+value)
	}
	command.Stdin = strings.NewReader(string(input))
	output, err := command.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
