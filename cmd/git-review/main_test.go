package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/CypDasHuhn/git-review/internal/session"
)

func TestVersionFlags(t *testing.T) {
	binary := buildBinary(t)
	for _, flag := range []string{"--version", "-v"} {
		if output := runCommand(t, projectRoot(t), binary, flag); output != "git-review 0.4.14\n" {
			t.Fatalf("%s output = %q, want git-review 0.4.14", flag, output)
		}
	}
}

func TestStatusNoInitReportsMissingSessionWithoutWriting(t *testing.T) {
	repository := newRepository(t, map[string]string{"README.md": "initial\n"})
	binary := buildBinary(t)
	beforeRefs := runCommand(t, repository, "git", "for-each-ref", "--format=%(refname)", "refs/git-review")
	beforeStatus := runCommand(t, repository, "git", "status", "--porcelain")
	var status struct {
		SessionExists bool   `json:"sessionExists"`
		WorkBranch    string `json:"workBranch"`
	}
	if err := json.Unmarshal([]byte(runCommand(t, repository, binary, "status", "--no-init")), &status); err != nil {
		t.Fatal(err)
	}
	if status.SessionExists || status.WorkBranch != "main" {
		t.Fatalf("status = %#v, want missing main session", status)
	}
	if after := runCommand(t, repository, "git", "for-each-ref", "--format=%(refname)", "refs/git-review"); after != beforeRefs {
		t.Fatalf("review refs changed from %q to %q", beforeRefs, after)
	}
	if after := runCommand(t, repository, "git", "status", "--porcelain"); after != beforeStatus {
		t.Fatalf("working tree status changed from %q to %q", beforeStatus, after)
	}
	if _, err := os.Stat(filepath.Join(repository, ".git", "git-review")); !os.IsNotExist(err) {
		t.Fatalf("git-review metadata directory exists after no-init status: %v", err)
	}
}

func TestStatusNoInitPreservesExistingSession(t *testing.T) {
	repository := newRepository(t, map[string]string{"README.md": "initial\n"})
	binary := buildBinary(t)
	runCommand(t, repository, binary, "init", "--base", "HEAD")
	metadata := filepath.Join(repository, ".git", "git-review")
	beforeFiles := snapshotDirectory(t, metadata)
	beforeRefs := runCommand(t, repository, "git", "for-each-ref", "--format=%(refname) %(objectname)", "refs/git-review")
	beforeStatus := runCommand(t, repository, "git", "status", "--porcelain")
	var status struct {
		SessionExists bool `json:"sessionExists"`
	}
	if err := json.Unmarshal([]byte(runCommand(t, repository, binary, "status", "--no-init")), &status); err != nil {
		t.Fatal(err)
	}
	if !status.SessionExists {
		t.Fatalf("status = %#v, want existing session", status)
	}
	if after := snapshotDirectory(t, metadata); !reflect.DeepEqual(after, beforeFiles) {
		t.Fatalf("review metadata changed from %#v to %#v", beforeFiles, after)
	}
	if after := runCommand(t, repository, "git", "for-each-ref", "--format=%(refname) %(objectname)", "refs/git-review"); after != beforeRefs {
		t.Fatalf("review refs changed from %q to %q", beforeRefs, after)
	}
	if after := runCommand(t, repository, "git", "status", "--porcelain"); after != beforeStatus {
		t.Fatalf("working tree status changed from %q to %q", beforeStatus, after)
	}
}

func TestHelpListsFragmentWorkflow(t *testing.T) {
	binary := buildBinary(t)
	rootHelp := runCommand(t, projectRoot(t), binary, "--help")
	for _, line := range []string{
		"git-review status [--json] [--no-init]",
		"git-review fragment inspect --commit <commit> [--path <path>] [--json]",
		"git-review fragment list --json [--unassigned]",
		"git-review group project --name <name>",
		"git-review group assign --name <name> --fragment <id> [--fragment <id>...]",
	} {
		if !strings.Contains(rootHelp, line) {
			t.Fatalf("root help = %q, missing %q", rootHelp, line)
		}
	}
	fragmentHelp := runCommand(t, projectRoot(t), binary, "fragment", "--help")
	if !strings.Contains(fragmentHelp, "git-review fragment inspect") || !strings.Contains(fragmentHelp, "git-review fragment list") {
		t.Fatalf("fragment help = %q", fragmentHelp)
	}
	assignHelp := runCommand(t, projectRoot(t), binary, "group", "assign", "--help")
	if !strings.Contains(assignHelp, "--fragment <id>") || !strings.Contains(assignHelp, "--commit <commit>") {
		t.Fatalf("group assign help = %q", assignHelp)
	}
}

func TestInitCreatesReviewSession(t *testing.T) {
	repository := newRepository(t, map[string]string{"README.md": "initial\n"})
	hookPath := filepath.Join(repository, ".git", "hooks", "post-merge")
	if err := os.WriteFile(hookPath, []byte("#!/bin/sh\necho existing\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	binary := buildBinary(t)
	runCommand(t, repository, binary, "init", "--base", "HEAD")
	status := readStatus(t, repository, binary)
	if status.WorkBranch != "main" {
		t.Fatalf("work branch = %q, want main", status.WorkBranch)
	}
	if status.ComparisonBaseAdvanced {
		t.Fatal("comparison base unexpectedly advanced")
	}
	if status.ReviewedCommit != status.PinnedComparisonCommit || status.InitialReviewedCommit != status.PinnedComparisonCommit {
		t.Fatalf("reviewed state = %#v, want pinned initial commit", status)
	}
	if value := runCommand(t, repository, "git", "config", "--local", "rerere.enabled"); value != "true\n" {
		t.Fatalf("rerere.enabled = %q, want true", value)
	}
	hook, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatal(err)
	}
	if content := string(hook); !strings.Contains(content, "echo existing") || strings.Count(content, "sync-base --hook") != 1 {
		t.Fatalf("post-merge hook = %q", content)
	}
}

func TestInitInstallsHookInCommonDirectoryForWorktree(t *testing.T) {
	repository := newRepository(t, map[string]string{"README.md": "initial\n"})
	worktree := filepath.Join(t.TempDir(), "feature")
	runCommand(t, repository, "git", "worktree", "add", "-b", "feature", worktree, "main")
	defer runCommand(t, repository, "git", "worktree", "remove", "--force", worktree)
	binary := buildBinary(t)
	runCommand(t, worktree, binary, "init", "--base", "main")
	hookPath := filepath.Join(repository, ".git", "hooks", "post-merge")
	hook, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(string(hook), "sync-base --hook") != 1 {
		t.Fatalf("post-merge hook = %q", hook)
	}
}

func TestAcceptOutputsEmptyListsAsArrays(t *testing.T) {
	repository := newRepository(t, map[string]string{"README.md": "initial\n"})
	binary := buildBinary(t)
	runCommand(t, repository, binary, "init", "--base", "HEAD")
	writeFile(t, repository, "changed.txt", "changed\n")
	runCommand(t, repository, "git", "add", "changed.txt")
	runCommand(t, repository, "git", "commit", "-m", "change")

	output := runCommand(t, repository, binary, "accept", "--path", "changed.txt")
	var response map[string]json.RawMessage
	if err := json.Unmarshal([]byte(output), &response); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"accepted", "materialized", "pending"} {
		raw, ok := response[field]
		if !ok || strings.TrimSpace(string(raw)) == "null" {
			t.Fatalf("%s = %q, want JSON array", field, raw)
		}
		var values []string
		if err := json.Unmarshal(raw, &values); err != nil {
			t.Fatalf("%s = %q is not a string array: %v", field, raw, err)
		}
	}
}

func TestAcceptMaterializesRenameOnlyFragment(t *testing.T) {
	repository := newRepository(t, map[string]string{"Brandbook/tokens.json": "tokens\n"})
	binary := buildBinary(t)
	runCommand(t, repository, binary, "init", "--base", "HEAD")
	if err := os.MkdirAll(filepath.Join(repository, "planning", "brandbook"), 0o755); err != nil {
		t.Fatal(err)
	}
	runCommand(t, repository, "git", "mv", "Brandbook/tokens.json", "planning/brandbook/tokens.json")
	runCommand(t, repository, "git", "commit", "-m", "move brandbook")
	fragments := readChanges(t, repository, binary)
	if len(fragments) != 1 || fragments[0].OldPath != "Brandbook/tokens.json" || fragments[0].Path != "planning/brandbook/tokens.json" {
		t.Fatalf("fragments = %#v, want rename fragment", fragments)
	}
	runCommand(t, repository, binary, "accept", "--path", "planning/brandbook/tokens.json")
	status := readStatus(t, repository, binary)
	if status.AcceptedCount != 1 || status.MaterializedCount != 1 || status.PendingCount != 0 {
		t.Fatalf("status after rename acceptance = %#v, want materialized rename", status)
	}
	if content := runCommand(t, repository, "git", "show", session.ReviewedRef("main")+":planning/brandbook/tokens.json"); content != "tokens\n" {
		t.Fatalf("reviewed renamed content = %q", content)
	}
	if _, err := runCommandError(t, repository, "git", "cat-file", "-e", session.ReviewedRef("main")+":Brandbook/tokens.json"); err == nil {
		t.Fatal("old renamed path still exists in reviewed state")
	}
}

func TestAcceptMaterializesRenameOnlyFragmentWithoutSourceInReviewBase(t *testing.T) {
	repository := newRepository(t, map[string]string{"Brandbook/tokens.json": "tokens\n"})
	binary := buildBinary(t)
	initialCommit := strings.TrimSpace(runCommand(t, repository, "git", "rev-parse", "HEAD"))
	runCommand(t, repository, "git", "rm", "Brandbook/tokens.json")
	runCommand(t, repository, "git", "commit", "-m", "remove brandbook")
	runCommand(t, repository, binary, "init", "--base", "HEAD")
	runCommand(t, repository, "git", "branch", "source", initialCommit)
	tree := strings.TrimSpace(runCommand(t, repository, "git", "rev-parse", "source^{tree}"))
	mergeCommit := strings.TrimSpace(runCommand(t, repository, "git", "commit-tree", tree, "-p", "HEAD", "-p", "source", "-m", "merge source"))
	runCommand(t, repository, "git", "update-ref", "refs/heads/main", mergeCommit)
	runCommand(t, repository, "git", "reset", "--hard", mergeCommit)
	if err := os.MkdirAll(filepath.Join(repository, "planning", "brandbook"), 0o755); err != nil {
		t.Fatal(err)
	}
	runCommand(t, repository, "git", "mv", "Brandbook/tokens.json", "planning/brandbook/tokens.json")
	runCommand(t, repository, "git", "commit", "-m", "move brandbook")
	fragments := readChanges(t, repository, binary)
	var rename session.Fragment
	for _, fragment := range fragments {
		if fragment.OldPath != "" {
			rename = fragment
			break
		}
	}
	if rename.Path != "planning/brandbook/tokens.json" || rename.OldPath != "Brandbook/tokens.json" {
		t.Fatalf("rename fragment = %#v", rename)
	}
	runCommand(t, repository, binary, "accept", "--path", rename.Path)
	status := readStatus(t, repository, binary)
	if status.AcceptedCount != 1 || status.MaterializedCount != 1 || status.PendingCount != 0 {
		t.Fatalf("status after rename acceptance = %#v, want materialized rename", status)
	}
	if content := runCommand(t, repository, "git", "show", session.ReviewedRef("main")+":planning/brandbook/tokens.json"); content != "tokens\n" {
		t.Fatalf("reviewed renamed content = %q", content)
	}
	if _, err := runCommandError(t, repository, "git", "cat-file", "-e", session.ReviewedRef("main")+":Brandbook/tokens.json"); err == nil {
		t.Fatal("old renamed path still exists in reviewed state")
	}
	if _, err := runCommandError(t, repository, "git", "diff", "--quiet", session.ReviewedRef("main"), "HEAD", "--", "planning/brandbook/tokens.json"); err != nil {
		t.Fatalf("reviewed rename still differs from HEAD: %v", err)
	}
}

func TestAcceptPathIncludesRenameHistory(t *testing.T) {
	repository := newRepository(t, map[string]string{"README.md": "initial\n"})
	binary := buildBinary(t)
	runCommand(t, repository, binary, "init", "--base", "HEAD")
	writeFile(t, repository, "ENTRA_EXTERNAL_ID_SETUP.md", "one\ntwo\n")
	runCommand(t, repository, "git", "add", "ENTRA_EXTERNAL_ID_SETUP.md")
	runCommand(t, repository, "git", "commit", "-m", "add entra setup")
	if err := os.MkdirAll(filepath.Join(repository, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	runCommand(t, repository, "git", "mv", "ENTRA_EXTERNAL_ID_SETUP.md", "docs/ENTRA_EXTERNAL_ID_SETUP.md")
	runCommand(t, repository, "git", "commit", "-m", "move docs")
	fragments := readChanges(t, repository, binary)
	var rename session.Fragment
	for _, fragment := range fragments {
		if fragment.OldPath != "" && fragment.OldPath != fragment.Path {
			rename = fragment
			break
		}
	}
	if rename.Path != "docs/ENTRA_EXTERNAL_ID_SETUP.md" || rename.OldPath != "ENTRA_EXTERNAL_ID_SETUP.md" {
		t.Fatalf("rename fragment = %#v", rename)
	}
	runCommand(t, repository, binary, "accept", "--path", rename.Path)
	status := readStatus(t, repository, binary)
	if status.AcceptedCount != 2 || status.MaterializedCount != 2 || status.PendingCount != 0 {
		t.Fatalf("status after path acceptance = %#v, want rename history materialized", status)
	}
	if content := runCommand(t, repository, "git", "show", session.ReviewedRef("main")+":docs/ENTRA_EXTERNAL_ID_SETUP.md"); content != "one\ntwo\n" {
		t.Fatalf("reviewed renamed content = %q", content)
	}
	if _, err := runCommandError(t, repository, "git", "diff", "--quiet", session.ReviewedRef("main"), "HEAD", "--", "docs/ENTRA_EXTERNAL_ID_SETUP.md"); err != nil {
		t.Fatalf("reviewed rename still differs from HEAD: %v", err)
	}
}

func TestAcceptPathRemovesStaleReviewedPaths(t *testing.T) {
	repository := newRepository(t, map[string]string{"README.md": "initial\n"})
	binary := buildBinary(t)
	base := strings.TrimSpace(runCommand(t, repository, "git", "rev-parse", "HEAD"))
	runCommand(t, repository, binary, "init", "--base", "HEAD")
	writeFile(t, repository, "planning/brandbook/tokens.json", "tokens\n")
	runCommand(t, repository, "git", "add", "planning/brandbook/tokens.json")
	runCommand(t, repository, "git", "commit", "-m", "add planning brandbook")
	runCommand(t, repository, binary, "accept", "--path", "planning/brandbook")
	runCommand(t, repository, "git", "reset", "--hard", base)
	writeFile(t, repository, "docs/brandbook/tokens.json", "tokens\n")
	runCommand(t, repository, "git", "add", "docs/brandbook/tokens.json")
	runCommand(t, repository, "git", "commit", "-m", "rewrite docs location")
	output := runCommand(t, repository, binary, "accept", "--path", "planning/brandbook")
	var response struct {
		Reconciled []string `json:"reconciled"`
	}
	if err := json.Unmarshal([]byte(output), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Reconciled) != 1 || response.Reconciled[0] != "planning/brandbook/tokens.json" {
		t.Fatalf("reconciled paths = %#v, want stale planning path", response.Reconciled)
	}
	if _, err := runCommandError(t, repository, "git", "cat-file", "-e", session.ReviewedRef("main")+":planning/brandbook/tokens.json"); err == nil {
		t.Fatal("stale planning path still exists in reviewed state")
	}
	if _, err := runCommandError(t, repository, "git", "diff", "--quiet", session.ReviewedRef("main"), "HEAD", "--", "planning/brandbook"); err != nil {
		t.Fatalf("stale planning path still differs from HEAD: %v", err)
	}
	runCommand(t, repository, binary, "undo")
	if content := runCommand(t, repository, "git", "show", session.ReviewedRef("main")+":planning/brandbook/tokens.json"); content != "tokens\n" {
		t.Fatalf("planning path after undo = %q, want restored content", content)
	}
}

func TestAcceptPathReconcilesMergeOnlyDiff(t *testing.T) {
	repository := newRepository(t, map[string]string{"example.txt": "before\n"})
	binary := buildBinary(t)
	base := strings.TrimSpace(runCommand(t, repository, "git", "rev-parse", "HEAD"))
	runCommand(t, repository, binary, "init", "--base", "HEAD")
	writeFile(t, repository, "example.txt", "after\n")
	runCommand(t, repository, "git", "add", "example.txt")
	runCommand(t, repository, "git", "commit", "-m", "change example")
	runCommand(t, repository, binary, "accept", "--path", "example.txt")
	writeFile(t, repository, "example.txt", "after,\n")
	runCommand(t, repository, "git", "add", "example.txt")
	tree := strings.TrimSpace(runCommand(t, repository, "git", "write-tree"))
	mergeCommit := strings.TrimSpace(runCommand(t, repository, "git", "commit-tree", tree, "-p", "HEAD", "-p", base, "-m", "merge formatting"))
	runCommand(t, repository, "git", "update-ref", "refs/heads/main", mergeCommit)
	runCommand(t, repository, "git", "reset", "--hard", mergeCommit)
	output := runCommand(t, repository, binary, "accept", "--path", "example.txt")
	var response struct {
		Reconciled []string `json:"reconciled"`
	}
	if err := json.Unmarshal([]byte(output), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Reconciled) != 1 || response.Reconciled[0] != "example.txt" {
		t.Fatalf("reconciled paths = %#v, want merge-only path", response.Reconciled)
	}
	if content := runCommand(t, repository, "git", "show", session.ReviewedRef("main")+":example.txt"); content != "after,\n" {
		t.Fatalf("reviewed content = %q, want merge-only content", content)
	}
	if _, err := runCommandError(t, repository, "git", "diff", "--quiet", session.ReviewedRef("main"), "HEAD", "--", "example.txt"); err != nil {
		t.Fatalf("merge-only path still differs from HEAD: %v", err)
	}
}

func TestSyncBaseMergesCurrentComparisonIntoReviewedState(t *testing.T) {
	repository := newRepository(t, map[string]string{"base.txt": "initial\n"})
	runCommand(t, repository, "git", "checkout", "-b", "develop")
	runCommand(t, repository, "git", "checkout", "-b", "feature")
	binary := buildBinary(t)
	runCommand(t, repository, binary, "init", "--base", "develop")
	writeFile(t, repository, "accepted.txt", "accepted\n")
	writeFile(t, repository, "unaccepted.txt", "unaccepted\n")
	runCommand(t, repository, "git", "add", "accepted.txt", "unaccepted.txt")
	runCommand(t, repository, "git", "commit", "-m", "feature changes")
	runCommand(t, repository, binary, "accept", "--path", "accepted.txt")
	runCommand(t, repository, "git", "checkout", "develop")
	writeFile(t, repository, "develop.txt", "develop\n")
	runCommand(t, repository, "git", "add", "develop.txt")
	runCommand(t, repository, "git", "commit", "-m", "develop changes")
	developCommit := strings.TrimSpace(runCommand(t, repository, "git", "rev-parse", "HEAD"))
	runCommand(t, repository, "git", "checkout", "feature")
	runCommand(t, repository, "git", "merge", "--no-edit", "develop")
	status := readStatus(t, repository, binary)
	if status.PinnedComparisonCommit != developCommit || status.ComparisonBaseAdvanced {
		t.Fatalf("status after base sync = %#v", status)
	}
	parents := strings.Fields(runCommand(t, repository, "git", "show", "-s", "--format=%P", session.ReviewedRef("feature")))
	if len(parents) != 2 {
		t.Fatalf("reviewed state has %d parents, want 2", len(parents))
	}
	changes := readChanges(t, repository, binary)
	if len(changes) != 1 || changes[0].Path != "unaccepted.txt" {
		t.Fatalf("changes = %#v, want unaccepted fragment", changes)
	}
}

func TestGroupAddCommitFromBeforeMergedReviewBase(t *testing.T) {
	repository := newRepository(t, map[string]string{"base.txt": "initial\n"})
	runCommand(t, repository, "git", "checkout", "-b", "feature")
	writeFile(t, repository, "redispatch-api/defaults.txt", "feature\n")
	runCommand(t, repository, "git", "add", "redispatch-api/defaults.txt")
	runCommand(t, repository, "git", "commit", "-m", "feature change")
	featureCommit := strings.TrimSpace(runCommand(t, repository, "git", "rev-parse", "HEAD"))
	runCommand(t, repository, "git", "checkout", "main")
	runCommand(t, repository, "git", "checkout", "-b", "develop")
	writeFile(t, repository, "develop.txt", "develop\n")
	runCommand(t, repository, "git", "add", "develop.txt")
	runCommand(t, repository, "git", "commit", "-m", "develop change")
	runCommand(t, repository, "git", "checkout", "feature")
	runCommand(t, repository, "git", "merge", "--no-edit", "develop")
	binary := buildBinary(t)
	runCommand(t, repository, binary, "init", "--base", "develop")
	changes := readChanges(t, repository, binary)
	if len(changes) != 1 || changes[0].Commit != featureCommit {
		t.Fatalf("changes = %#v, want feature fragment", changes)
	}
	runCommand(t, repository, binary, "group", "add", "--name", "plant backend default", "--commit", featureCommit, "--path", "redispatch-api/...")
	var groups []groupStatus
	if err := json.Unmarshal([]byte(runCommand(t, repository, binary, "group", "list", "--json")), &groups); err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || len(groups[0].Fragments) != 1 || groups[0].Fragments[0].Path != "redispatch-api/defaults.txt" {
		t.Fatalf("groups = %#v, want feature fragment assigned by commit and path", groups)
	}
}

func TestReviewFragmentsIncludesMergedSideBranch(t *testing.T) {
	repository := newRepository(t, map[string]string{"base.txt": "initial\n"})
	runCommand(t, repository, "git", "checkout", "-b", "develop")
	writeFile(t, repository, "develop.txt", "develop\n")
	runCommand(t, repository, "git", "add", "develop.txt")
	runCommand(t, repository, "git", "commit", "-m", "develop change")
	runCommand(t, repository, "git", "checkout", "main")
	runCommand(t, repository, "git", "checkout", "-b", "source-side")
	writeFile(t, repository, "source-side.txt", "source\n")
	runCommand(t, repository, "git", "add", "source-side.txt")
	runCommand(t, repository, "git", "commit", "-m", "source-side change")
	sourceCommit := strings.TrimSpace(runCommand(t, repository, "git", "rev-parse", "HEAD"))
	runCommand(t, repository, "git", "checkout", "develop")
	runCommand(t, repository, "git", "checkout", "-b", "integration-side")
	runCommand(t, repository, "git", "merge", "--no-edit", "--no-ff", "source-side")
	writeFile(t, repository, "late-side.txt", "late\n")
	runCommand(t, repository, "git", "add", "late-side.txt")
	runCommand(t, repository, "git", "commit", "-m", "late-side change")
	lateCommit := strings.TrimSpace(runCommand(t, repository, "git", "rev-parse", "HEAD"))
	binary := buildBinary(t)
	runCommand(t, repository, binary, "init", "--base", "develop")
	changes := readChanges(t, repository, binary)
	if len(changes) != 2 {
		t.Fatalf("changes = %#v, want source-side and late-side fragments", changes)
	}
	fragmentBy(t, changes, sourceCommit, "source-side.txt")
	fragmentBy(t, changes, lateCommit, "late-side.txt")
	runCommand(t, repository, binary, "group", "add", "--name", "source-side", "--commit", sourceCommit)
	var groups []groupStatus
	if err := json.Unmarshal([]byte(runCommand(t, repository, binary, "group", "list", "--json")), &groups); err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || len(groups[0].Fragments) != 1 || groups[0].Fragments[0].Path != "source-side.txt" {
		t.Fatalf("groups = %#v, want merged side-branch fragment assigned by commit", groups)
	}
}

func TestSelectorAssignmentAcrossMergedHistory(t *testing.T) {
	repository := newRepository(t, map[string]string{"base.txt": "initial\n"})
	runCommand(t, repository, "git", "checkout", "-b", "develop")
	writeFile(t, repository, "develop.txt", "develop\n")
	runCommand(t, repository, "git", "add", "develop.txt")
	runCommand(t, repository, "git", "commit", "-m", "develop change")
	runCommand(t, repository, "git", "checkout", "main")
	runCommand(t, repository, "git", "checkout", "-b", "source-side")
	writeFile(t, repository, "redispatch-api/one/Mapping/Companies/first.txt", "first\n")
	writeFile(t, repository, "redispatch-api/one/Other/first.txt", "other\n")
	runCommand(t, repository, "git", "add", "redispatch-api/one/Mapping/Companies/first.txt", "redispatch-api/one/Other/first.txt")
	runCommand(t, repository, "git", "commit", "-m", "company mapping one")
	firstCommit := strings.TrimSpace(runCommand(t, repository, "git", "rev-parse", "HEAD"))
	writeFile(t, repository, "redispatch-api/two/Mapping/Companies/second.txt", "second\n")
	runCommand(t, repository, "git", "add", "redispatch-api/two/Mapping/Companies/second.txt")
	runCommand(t, repository, "git", "commit", "-m", "company mapping two")
	secondCommit := strings.TrimSpace(runCommand(t, repository, "git", "rev-parse", "HEAD"))
	runCommand(t, repository, "git", "checkout", "develop")
	runCommand(t, repository, "git", "checkout", "-b", "integration-side")
	runCommand(t, repository, "git", "merge", "--no-edit", "--no-ff", "source-side")
	binary := buildBinary(t)
	runCommand(t, repository, binary, "init", "--base", "develop")
	var dryRun assignmentReport
	if err := json.Unmarshal([]byte(runCommand(t, repository, binary, "group", "assign", "--name", "company sync", "--path-glob", "redispatch-api/**/Mapping/Companies/**", "--dry-run", "--json")), &dryRun); err != nil {
		t.Fatal(err)
	}
	if dryRun.Matched != 2 || dryRun.Assigned != 2 || !dryRun.DryRun {
		t.Fatalf("dry-run report = %#v, want two matched and assignable fragments", dryRun)
	}
	var intersection assignmentReport
	if err := json.Unmarshal([]byte(runCommand(t, repository, binary, "group", "assign", "--name", "company sync", "--commit", firstCommit, "--path-glob", "redispatch-api/**/Mapping/Companies/**", "--dry-run", "--json")), &intersection); err != nil {
		t.Fatal(err)
	}
	if intersection.Matched != 1 || intersection.Assigned != 1 {
		t.Fatalf("commit/path intersection report = %#v, want one first-commit fragment", intersection)
	}
	if groups := runCommand(t, repository, binary, "group", "list", "--json"); groups != "[]\n" {
		t.Fatalf("groups after dry-run = %q, want empty", groups)
	}
	var assigned assignmentReport
	if err := json.Unmarshal([]byte(runCommand(t, repository, binary, "group", "assign", "--name", "company sync", "--commit", firstCommit, "--commit", secondCommit, "--path-glob", "redispatch-api/**/Mapping/Companies/**", "--json")), &assigned); err != nil {
		t.Fatal(err)
	}
	if assigned.Matched != 2 || assigned.Assigned != 2 || assigned.AlreadyAssigned != 0 || assigned.Skipped != 0 {
		t.Fatalf("assignment report = %#v, want two newly assigned fragments", assigned)
	}
	var groups []groupStatus
	if err := json.Unmarshal([]byte(runCommand(t, repository, binary, "group", "list", "--json")), &groups); err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || len(groups[0].Fragments) != 2 {
		t.Fatalf("groups = %#v, want two fragments assigned across merged commits", groups)
	}
	var projection struct {
		Fragments []string `json:"fragments"`
		Blocked   []string `json:"blocked"`
	}
	if err := json.Unmarshal([]byte(runCommand(t, repository, binary, "group", "project", "--name", "company sync")), &projection); err != nil {
		t.Fatal(err)
	}
	if len(projection.Fragments) != 2 || len(projection.Blocked) != 0 {
		t.Fatalf("merged-history projection = %#v, want two displayable fragments", projection)
	}
}

func TestSelectorAssignmentPreservesAndReassignsExistingFragments(t *testing.T) {
	repository := newRepository(t, map[string]string{"base.txt": "initial\n"})
	binary := buildBinary(t)
	runCommand(t, repository, binary, "init", "--base", "HEAD")
	writeFile(t, repository, "mapping/one.txt", "one\n")
	writeFile(t, repository, "mapping/two.txt", "two\n")
	runCommand(t, repository, "git", "add", "mapping/one.txt", "mapping/two.txt")
	runCommand(t, repository, "git", "commit", "-m", "mapping changes")
	runCommand(t, repository, binary, "group", "assign", "--name", "first", "--path-glob", "mapping/**", "--json")
	var skipped assignmentReport
	if err := json.Unmarshal([]byte(runCommand(t, repository, binary, "group", "assign", "--name", "second", "--path-glob", "mapping/**", "--json")), &skipped); err != nil {
		t.Fatal(err)
	}
	if skipped.Matched != 2 || skipped.Assigned != 0 || skipped.AlreadyAssigned != 2 {
		t.Fatalf("preservation report = %#v, want existing assignments skipped", skipped)
	}
	var moved assignmentReport
	if err := json.Unmarshal([]byte(runCommand(t, repository, binary, "group", "assign", "--name", "second", "--path-glob", "mapping/**", "--reassign", "--json")), &moved); err != nil {
		t.Fatal(err)
	}
	if moved.Matched != 2 || moved.Assigned != 2 {
		t.Fatalf("reassignment report = %#v, want two moved fragments", moved)
	}
	var groups []groupStatus
	if err := json.Unmarshal([]byte(runCommand(t, repository, binary, "group", "list", "--json")), &groups); err != nil {
		t.Fatal(err)
	}
	if len(groups) != 2 || len(groups[0].Fragments) != 0 || len(groups[1].Fragments) != 2 {
		t.Fatalf("groups after reassignment = %#v, want fragments moved to second", groups)
	}
}

func TestFragmentAndGroupSummaryCounts(t *testing.T) {
	repository := newRepository(t, map[string]string{"base.txt": "initial\n"})
	binary := buildBinary(t)
	runCommand(t, repository, binary, "init", "--base", "HEAD")
	writeFile(t, repository, "company/one.txt", "one\n")
	writeFile(t, repository, "other/two.txt", "two\n")
	runCommand(t, repository, "git", "add", "company/one.txt", "other/two.txt")
	runCommand(t, repository, "git", "commit", "-m", "first mapping")
	writeFile(t, repository, "company/three.txt", "three\n")
	runCommand(t, repository, "git", "add", "company/three.txt")
	runCommand(t, repository, "git", "commit", "-m", "second mapping")
	runCommand(t, repository, binary, "group", "assign", "--name", "company sync", "--path-glob", "company/**", "--json")
	var fragments []fragmentStatus
	if err := json.Unmarshal([]byte(runCommand(t, repository, binary, "fragment", "list", "--json")), &fragments); err != nil {
		t.Fatal(err)
	}
	var fragmentSummary reviewSummary
	if err := json.Unmarshal([]byte(runCommand(t, repository, binary, "fragment", "list", "--summary", "--json")), &fragmentSummary); err != nil {
		t.Fatal(err)
	}
	if fragmentSummary.Total != len(fragments) || fragmentSummary.Assigned != 2 || fragmentSummary.Unassigned != 1 {
		t.Fatalf("fragment summary = %#v, fragments = %#v", fragmentSummary, fragments)
	}
	var groupSummary reviewSummary
	if err := json.Unmarshal([]byte(runCommand(t, repository, binary, "group", "list", "--summary", "--json")), &groupSummary); err != nil {
		t.Fatal(err)
	}
	if groupSummary.Total != fragmentSummary.Total || groupSummary.Assigned != fragmentSummary.Assigned || len(groupSummary.Groups) != 1 {
		t.Fatalf("group summary = %#v, fragment summary = %#v", groupSummary, fragmentSummary)
	}
	var filtered reviewSummary
	if err := json.Unmarshal([]byte(runCommand(t, repository, binary, "fragment", "list", "--summary", "--json", "--group", "company sync")), &filtered); err != nil {
		t.Fatal(err)
	}
	if filtered.Total != 2 || filtered.Assigned != 2 || filtered.Unassigned != 0 || len(filtered.Groups) != 1 || filtered.Groups[0].Commits != 2 || filtered.Groups[0].Paths != 2 {
		t.Fatalf("filtered summary = %#v, want two company fragments from two commits", filtered)
	}
}

func TestGroupProjectCreatesProjectionWithoutChangingReviewedState(t *testing.T) {
	repository := newRepository(t, map[string]string{"example.txt": "before\n"})
	binary := buildBinary(t)
	runCommand(t, repository, binary, "init", "--base", "HEAD")
	writeFile(t, repository, "example.txt", "first\n")
	runCommand(t, repository, "git", "add", "example.txt")
	runCommand(t, repository, "git", "commit", "-m", "first change")
	first := strings.TrimSpace(runCommand(t, repository, "git", "rev-parse", "HEAD"))
	writeFile(t, repository, "example.txt", "second\n")
	runCommand(t, repository, "git", "add", "example.txt")
	runCommand(t, repository, "git", "commit", "-m", "second change")
	second := strings.TrimSpace(runCommand(t, repository, "git", "rev-parse", "HEAD"))
	runCommand(t, repository, binary, "group", "add", "--name", "example", "--commit", first)
	runCommand(t, repository, binary, "group", "add", "--name", "example", "--commit", second)
	reviewedBefore := strings.TrimSpace(runCommand(t, repository, "git", "rev-parse", session.ReviewedRef("main")))
	var projection struct {
		Name        string   `json:"name"`
		Ref         string   `json:"ref"`
		Commit      string   `json:"commit"`
		ReviewedRef string   `json:"reviewedRef"`
		Fragments   []string `json:"fragments"`
		Blocked     []string `json:"blocked"`
		Complete    bool     `json:"complete"`
	}
	if err := json.Unmarshal([]byte(runCommand(t, repository, binary, "group", "project", "--name", "example")), &projection); err != nil {
		t.Fatal(err)
	}
	if projection.Name != "example" || projection.Ref == "" || projection.Commit == "" || len(projection.Fragments) != 2 || len(projection.Blocked) != 0 || projection.Complete {
		t.Fatalf("projection = %#v, want two displayable fragments", projection)
	}
	if reviewedAfter := strings.TrimSpace(runCommand(t, repository, "git", "rev-parse", session.ReviewedRef("main"))); reviewedAfter != reviewedBefore {
		t.Fatalf("reviewed ref changed from %s to %s", reviewedBefore, reviewedAfter)
	}
	if content := runCommand(t, repository, "git", "show", projection.Ref+":example.txt"); content != "second\n" {
		t.Fatalf("projection content = %q, want second\n", content)
	}
	if files := runCommand(t, repository, "git", "diff", "--name-only", projection.ReviewedRef+".."+projection.Ref); files != "example.txt\n" {
		t.Fatalf("projection files = %q, want example.txt", files)
	}
	runCommand(t, repository, binary, "group", "accept", "--name", "example", "--path", "example.txt")
	if err := json.Unmarshal([]byte(runCommand(t, repository, binary, "group", "project", "--name", "example")), &projection); err != nil {
		t.Fatal(err)
	}
	if !projection.Complete || len(projection.Fragments) != 0 || len(projection.Blocked) != 0 {
		t.Fatalf("completed projection = %#v, want no remaining fragments", projection)
	}
}

func TestGroupProjectReportsUnmaterializedDependencies(t *testing.T) {
	repository := newRepository(t, map[string]string{"example.txt": "before\n"})
	binary := buildBinary(t)
	runCommand(t, repository, binary, "init", "--base", "HEAD")
	writeFile(t, repository, "example.txt", "first\n")
	runCommand(t, repository, "git", "add", "example.txt")
	runCommand(t, repository, "git", "commit", "-m", "first change")
	first := strings.TrimSpace(runCommand(t, repository, "git", "rev-parse", "HEAD"))
	writeFile(t, repository, "example.txt", "second\n")
	runCommand(t, repository, "git", "add", "example.txt")
	runCommand(t, repository, "git", "commit", "-m", "second change")
	second := strings.TrimSpace(runCommand(t, repository, "git", "rev-parse", "HEAD"))
	runCommand(t, repository, binary, "group", "add", "--name", "first", "--commit", first)
	runCommand(t, repository, binary, "group", "add", "--name", "second", "--commit", second)
	var projection struct {
		Fragments []string `json:"fragments"`
		Blocked   []string `json:"blocked"`
		Complete  bool     `json:"complete"`
	}
	if err := json.Unmarshal([]byte(runCommand(t, repository, binary, "group", "project", "--name", "second")), &projection); err != nil {
		t.Fatal(err)
	}
	if len(projection.Fragments) != 0 || len(projection.Blocked) != 1 || projection.Complete {
		t.Fatalf("blocked projection = %#v, want dependency blocker", projection)
	}
}

func TestGroupProjectHandlesMergeFormattingDrift(t *testing.T) {
	repository := newRepository(t, map[string]string{"example.txt": "start\nend\n"})
	runCommand(t, repository, "git", "checkout", "-b", "develop")
	writeFile(t, repository, "example.txt", "start\ncontext\nend\n")
	runCommand(t, repository, "git", "add", "example.txt")
	runCommand(t, repository, "git", "commit", "-m", "develop context")
	runCommand(t, repository, "git", "checkout", "main")
	runCommand(t, repository, "git", "checkout", "-b", "feature")
	writeFile(t, repository, "example.txt", "start\nif value {\n    one\n}\nend\n")
	runCommand(t, repository, "git", "add", "example.txt")
	runCommand(t, repository, "git", "commit", "-m", "feature block")
	featureCommit := strings.TrimSpace(runCommand(t, repository, "git", "rev-parse", "HEAD"))
	if output, err := runCommandError(t, repository, "git", "merge", "--no-ff", "--no-commit", "develop"); err == nil {
		t.Fatalf("merge unexpectedly succeeded: %s", output)
	}
	writeFile(t, repository, "example.txt", "start\ncontext\nif value\n{\n    one\n}\nend\n")
	runCommand(t, repository, "git", "add", "example.txt")
	runCommand(t, repository, "git", "commit", "-m", "merge context")
	writeFile(t, repository, "example.txt", "start\ncontext\nresult\nend\n")
	runCommand(t, repository, "git", "add", "example.txt")
	runCommand(t, repository, "git", "commit", "-m", "replace block")
	replacementCommit := strings.TrimSpace(runCommand(t, repository, "git", "rev-parse", "HEAD"))
	binary := buildBinary(t)
	runCommand(t, repository, binary, "init", "--base", "develop")
	runCommand(t, repository, binary, "group", "add", "--name", "runtime", "--commit", featureCommit)
	runCommand(t, repository, binary, "group", "add", "--name", "runtime", "--commit", replacementCommit)
	var projection struct {
		Fragments []string `json:"fragments"`
		Blocked   []string `json:"blocked"`
		Complete  bool     `json:"complete"`
		Ref       string   `json:"ref"`
	}
	if err := json.Unmarshal([]byte(runCommand(t, repository, binary, "group", "project", "--name", "runtime")), &projection); err != nil {
		t.Fatal(err)
	}
	if len(projection.Fragments) != 2 || len(projection.Blocked) != 0 || projection.Complete {
		t.Fatalf("projection = %#v, want two displayable fragments", projection)
	}
	if content := runCommand(t, repository, "git", "show", projection.Ref+":example.txt"); content != "start\ncontext\nresult\nend\n" {
		t.Fatalf("projection content = %q, want formatted merge drift resolved", content)
	}
}

func TestGroupProjectHandlesFragmentAlreadyPresentInBase(t *testing.T) {
	repository := newRepository(t, map[string]string{"example.txt": "old\n"})
	runCommand(t, repository, "git", "checkout", "-b", "develop")
	writeFile(t, repository, "example.txt", "new\n")
	runCommand(t, repository, "git", "add", "example.txt")
	runCommand(t, repository, "git", "commit", "-m", "develop change")
	runCommand(t, repository, "git", "checkout", "main")
	runCommand(t, repository, "git", "checkout", "-b", "feature")
	writeFile(t, repository, "example.txt", "new\n")
	runCommand(t, repository, "git", "add", "example.txt")
	runCommand(t, repository, "git", "commit", "-m", "duplicate change")
	commit := strings.TrimSpace(runCommand(t, repository, "git", "rev-parse", "HEAD"))
	binary := buildBinary(t)
	runCommand(t, repository, binary, "init", "--base", "develop")
	runCommand(t, repository, binary, "group", "add", "--name", "duplicate", "--commit", commit)
	var projection struct {
		Fragments []string `json:"fragments"`
		Blocked   []string `json:"blocked"`
		Complete  bool     `json:"complete"`
		Ref       string   `json:"ref"`
	}
	if err := json.Unmarshal([]byte(runCommand(t, repository, binary, "group", "project", "--name", "duplicate")), &projection); err != nil {
		t.Fatal(err)
	}
	if len(projection.Fragments) != 1 || len(projection.Blocked) != 0 || projection.Complete {
		t.Fatalf("projection = %#v, want already-present fragment handled", projection)
	}
	if content := runCommand(t, repository, "git", "show", projection.Ref+":example.txt"); content != "new\n" {
		t.Fatalf("projection content = %q, want new\\n", content)
	}
}

func TestFragmentIdentitySurvivesLineDrift(t *testing.T) {
	repository := newRepository(t, map[string]string{"example.txt": "one\ntwo\nthree\nfour\nfive\nsix\n"})
	binary := buildBinary(t)
	runCommand(t, repository, binary, "init", "--base", "HEAD")
	writeFile(t, repository, "example.txt", "zero\none\ntwo\nthree\nfour\nfive changed\nsix\n")
	runCommand(t, repository, "git", "add", "example.txt")
	runCommand(t, repository, "git", "commit", "-m", "two fragments")
	commit := strings.TrimSpace(runCommand(t, repository, "git", "rev-parse", "HEAD"))
	fragments := readChanges(t, repository, binary)
	if len(fragments) != 2 {
		t.Fatalf("fragments = %#v, want insertion and replacement", fragments)
	}
	var insertion, replacement session.Fragment
	for _, fragment := range fragments {
		if fragment.OldLines == 0 {
			insertion = fragment
		} else {
			replacement = fragment
		}
	}
	if insertion.Commit != commit || replacement.Commit != commit || insertion.OldStart == replacement.OldStart {
		t.Fatalf("fragment identities = %#v, %#v", insertion, replacement)
	}
	assignFragment(t, repository, binary, "insert", insertion)
	assignFragment(t, repository, binary, "replace", replacement)
	runCommand(t, repository, binary, "group", "accept", "--name", "insert", "--path", "example.txt")
	pending := readChanges(t, repository, binary)
	if len(pending) != 1 || pending[0].ID() != replacement.ID() || pending[0].OldStart != replacement.OldStart {
		t.Fatalf("pending fragments after line drift = %#v, want original replacement identity", pending)
	}
	runCommand(t, repository, binary, "group", "accept", "--name", "replace", "--path", "example.txt")
	if changes := readChanges(t, repository, binary); len(changes) != 0 {
		t.Fatalf("changes after both fragments = %#v, want none", changes)
	}
	if content := runCommand(t, repository, "git", "show", session.ReviewedRef("main")+":example.txt"); content != "zero\none\ntwo\nthree\nfour\nfive changed\nsix\n" {
		t.Fatalf("reviewed content = %q", content)
	}
}

func TestFragmentInspectAssignAndListUnassigned(t *testing.T) {
	repository := newRepository(t, map[string]string{"example.txt": "one\ntwo\nthree\nfour\nfive\n"})
	binary := buildBinary(t)
	runCommand(t, repository, binary, "init", "--base", "HEAD")
	writeFile(t, repository, "example.txt", "zero\none\ntwo\nthree changed\nfour\nfive changed\n")
	runCommand(t, repository, "git", "add", "example.txt")
	runCommand(t, repository, "git", "commit", "-m", "inspectable fragments")
	commit := strings.TrimSpace(runCommand(t, repository, "git", "rev-parse", "HEAD"))
	var inspected []fragmentView
	if err := json.Unmarshal([]byte(runCommand(t, repository, binary, "fragment", "inspect", "--commit", commit, "--json")), &inspected); err != nil {
		t.Fatal(err)
	}
	if len(inspected) != 3 {
		t.Fatalf("inspected fragments = %#v, want three", inspected)
	}
	for _, fragment := range inspected {
		if fragment.ID == "" || fragment.ShortID == "" || fragment.Assignment != "unassigned" || !fragment.Assignable || fragment.Patch == "" {
			t.Fatalf("inspected fragment = %#v, want assignable unassigned fragment", fragment)
		}
	}
	runCommand(t, repository, binary, "group", "assign", "--name", "first", "--fragment", inspected[0].ShortID)
	var unassigned []fragmentStatus
	if err := json.Unmarshal([]byte(runCommand(t, repository, binary, "fragment", "list", "--json", "--unassigned")), &unassigned); err != nil {
		t.Fatal(err)
	}
	if len(unassigned) != 2 {
		t.Fatalf("unassigned fragments after first assignment = %#v, want two", unassigned)
	}
	status := readStatus(t, repository, binary)
	if status.AssignedCount != 1 || status.UnassignedCount != 2 {
		t.Fatalf("status after first assignment = %#v", status)
	}
	runCommand(t, repository, binary, "group", "assign", "--name", "second", "--fragment", inspected[1].ID, "--fragment", inspected[2].ID)
	unassigned = nil
	if err := json.Unmarshal([]byte(runCommand(t, repository, binary, "fragment", "list", "--json", "--unassigned")), &unassigned); err != nil {
		t.Fatal(err)
	}
	if len(unassigned) != 0 {
		t.Fatalf("unassigned fragments after all assignments = %#v, want none", unassigned)
	}
	var refreshed []fragmentView
	if err := json.Unmarshal([]byte(runCommand(t, repository, binary, "fragment", "inspect", "--commit", commit, "--json")), &refreshed); err != nil {
		t.Fatal(err)
	}
	if refreshed[0].Group != "first" || refreshed[1].Group != "second" || refreshed[2].Group != "second" {
		t.Fatalf("refreshed assignments = %#v", refreshed)
	}
}

func TestFragmentMaterializationRejectsAmbiguousDrift(t *testing.T) {
	repository := newRepository(t, map[string]string{"example.txt": "a\nb\na\nb\na\nb\na\n"})
	binary := buildBinary(t)
	runCommand(t, repository, binary, "init", "--base", "HEAD")
	writeFile(t, repository, "example.txt", "top\na\nb\ntarget\na\nb\na\nb\na\n")
	runCommand(t, repository, "git", "add", "example.txt")
	runCommand(t, repository, "git", "commit", "-m", "ambiguous insertions")
	fragments := readChanges(t, repository, binary)
	if len(fragments) != 2 {
		t.Fatalf("fragments = %#v, want two insertions", fragments)
	}
	var top, target session.Fragment
	for _, fragment := range fragments {
		if fragment.NewLines != 1 || fragment.OldLines != 0 {
			t.Fatalf("fragment = %#v, want insertion", fragment)
		}
		if fragment.OldStart == 0 {
			top = fragment
		} else {
			target = fragment
		}
	}
	if top.Commit == "" || target.Commit == "" || top.OldStart == target.OldStart {
		t.Fatalf("insertion fragments = %#v, %#v", top, target)
	}
	assignFragment(t, repository, binary, "top", top)
	assignFragment(t, repository, binary, "target", target)
	runCommand(t, repository, binary, "group", "accept", "--name", "top", "--path", "example.txt")
	output, err := runCommandError(t, repository, binary, "group", "accept", "--name", "target", "--path", "example.txt")
	if err == nil || !strings.Contains(output, "ambiguous") {
		t.Fatalf("ambiguous materialization output = %q, err = %v", output, err)
	}
	status := readStatus(t, repository, binary)
	if status.AcceptedCount != 2 || status.MaterializedCount != 1 || status.PendingCount != 1 {
		t.Fatalf("status after ambiguous materialization = %#v", status)
	}
	var groups []groupStatus
	if err := json.Unmarshal([]byte(runCommand(t, repository, binary, "group", "list", "--json")), &groups); err != nil {
		t.Fatal(err)
	}
	if len(groups) != 2 || groups[1].Fragments[0].Status != "accepted-pending" {
		t.Fatalf("groups after ambiguous materialization = %#v", groups)
	}
}

func TestGroupAssignmentRejectsDependencyCycle(t *testing.T) {
	repository := newRepository(t, map[string]string{"a.txt": "a0\n", "b.txt": "b0\n"})
	binary := buildBinary(t)
	runCommand(t, repository, binary, "init", "--base", "HEAD")
	writeFile(t, repository, "a.txt", "a1\n")
	writeFile(t, repository, "b.txt", "b1\n")
	runCommand(t, repository, "git", "add", "a.txt", "b.txt")
	runCommand(t, repository, "git", "commit", "-m", "first")
	first := strings.TrimSpace(runCommand(t, repository, "git", "rev-parse", "HEAD"))
	writeFile(t, repository, "a.txt", "a2\n")
	writeFile(t, repository, "b.txt", "b2\n")
	runCommand(t, repository, "git", "add", "a.txt", "b.txt")
	runCommand(t, repository, "git", "commit", "-m", "second")
	second := strings.TrimSpace(runCommand(t, repository, "git", "rev-parse", "HEAD"))
	fragments := readChanges(t, repository, binary)
	firstA := fragmentBy(t, fragments, first, "a.txt")
	firstB := fragmentBy(t, fragments, first, "b.txt")
	secondA := fragmentBy(t, fragments, second, "a.txt")
	secondB := fragmentBy(t, fragments, second, "b.txt")
	assignFragment(t, repository, binary, "one", firstA)
	assignFragment(t, repository, binary, "one", secondB)
	assignFragment(t, repository, binary, "two", firstB)
	output, err := runCommandError(t, repository, binary, "group", "assign", "--name", "two", "--commit", secondA.Commit,
		"--path", secondA.Path, "--old-start", number(secondA.OldStart), "--old-lines", number(secondA.OldLines),
		"--new-start", number(secondA.NewStart), "--new-lines", number(secondA.NewLines))
	if err == nil || !strings.Contains(output, "dependency cycle") {
		t.Fatalf("cycle assignment output = %q, err = %v", output, err)
	}
}

func TestOutOfOrderAcceptanceMaterializesAfterDependencies(t *testing.T) {
	repository := newRepository(t, map[string]string{"a.txt": "a0\n", "b.txt": "b0\n"})
	binary := buildBinary(t)
	runCommand(t, repository, binary, "init", "--base", "HEAD")
	writeFile(t, repository, "a.txt", "a1\n")
	writeFile(t, repository, "b.txt", "b1\n")
	runCommand(t, repository, "git", "add", "a.txt", "b.txt")
	runCommand(t, repository, "git", "commit", "-m", "first")
	first := strings.TrimSpace(runCommand(t, repository, "git", "rev-parse", "HEAD"))
	writeFile(t, repository, "a.txt", "a2\n")
	writeFile(t, repository, "b.txt", "b2\n")
	runCommand(t, repository, "git", "add", "a.txt", "b.txt")
	runCommand(t, repository, "git", "commit", "-m", "second")
	second := strings.TrimSpace(runCommand(t, repository, "git", "rev-parse", "HEAD"))
	runCommand(t, repository, binary, "group", "add", "--name", "first", "--commit", first)
	runCommand(t, repository, binary, "group", "add", "--name", "second", "--commit", second)
	runCommand(t, repository, binary, "group", "accept", "--name", "second", "--path", "a.txt")
	status := readStatus(t, repository, binary)
	if status.AcceptedCount != 1 || status.MaterializedCount != 0 || status.PendingCount != 1 {
		t.Fatalf("status after out-of-order acceptance = %#v", status)
	}
	runCommand(t, repository, binary, "undo")
	status = readStatus(t, repository, binary)
	if status.AcceptedCount != 0 || status.MaterializedCount != 0 {
		t.Fatalf("status after undoing pending acceptance = %#v", status)
	}
	runCommand(t, repository, binary, "group", "accept", "--name", "second", "--path", "a.txt")
	runCommand(t, repository, binary, "group", "accept", "--name", "first", "--path", "a.txt")
	status = readStatus(t, repository, binary)
	if status.AcceptedCount != 2 || status.MaterializedCount != 2 || status.PendingCount != 0 {
		t.Fatalf("status after dependency materialization = %#v", status)
	}
	if content := runCommand(t, repository, "git", "show", session.ReviewedRef("main")+":a.txt"); content != "a2\n" {
		t.Fatalf("reviewed a.txt = %q, want a2", content)
	}
	if changes := readChanges(t, repository, binary); len(changes) != 2 {
		t.Fatalf("remaining changes = %#v, want b fragments", changes)
	}
}

func TestOutOfOrderAcceptanceSkipsSupersededFragment(t *testing.T) {
	repository := newRepository(t, map[string]string{"example.txt": "before\n"})
	binary := buildBinary(t)
	runCommand(t, repository, binary, "init", "--base", "HEAD")
	writeFile(t, repository, "example.txt", "changed\n")
	runCommand(t, repository, "git", "add", "example.txt")
	runCommand(t, repository, "git", "commit", "-m", "change example")
	first := strings.TrimSpace(runCommand(t, repository, "git", "rev-parse", "HEAD"))
	runCommand(t, repository, "git", "rm", "example.txt")
	runCommand(t, repository, "git", "commit", "-m", "remove example")
	second := strings.TrimSpace(runCommand(t, repository, "git", "rev-parse", "HEAD"))
	fragments := readChanges(t, repository, binary)
	firstFragment := fragmentBy(t, fragments, first, "example.txt")
	secondFragment := fragmentBy(t, fragments, second, "example.txt")
	assignFragment(t, repository, binary, "remove", secondFragment)
	assignFragment(t, repository, binary, "change", firstFragment)
	runCommand(t, repository, binary, "group", "accept", "--name", "remove", "--path", "example.txt")
	runCommand(t, repository, binary, "group", "accept", "--name", "change", "--path", "example.txt")
	status := readStatus(t, repository, binary)
	if status.AcceptedCount != 2 || status.MaterializedCount != 2 || status.PendingCount != 0 {
		t.Fatalf("status after superseded fragment = %#v, want fully materialized", status)
	}
	if _, err := runCommandError(t, repository, "git", "diff", "--quiet", session.ReviewedRef("main"), "HEAD", "--", "example.txt"); err != nil {
		t.Fatalf("reviewed superseded path still differs from HEAD: %v", err)
	}
}

func TestGroupListRetainsFragmentStatus(t *testing.T) {
	repository := newRepository(t, map[string]string{"example.txt": "before\n"})
	binary := buildBinary(t)
	runCommand(t, repository, binary, "init", "--base", "HEAD")
	writeFile(t, repository, "example.txt", "after\n")
	runCommand(t, repository, "git", "add", "example.txt")
	runCommand(t, repository, "git", "commit", "-m", "change")
	commit := strings.TrimSpace(runCommand(t, repository, "git", "rev-parse", "HEAD"))
	runCommand(t, repository, binary, "group", "add", "--name", "example", "--commit", commit)
	view := runCommand(t, repository, binary, "group", "view", "--name", "example")
	if !strings.Contains(view, "@@ -1,1 +1,1 @@") || !strings.Contains(view, "-before") || !strings.Contains(view, "+after") {
		t.Fatalf("group view = %q, want source fragment patch", view)
	}
	runCommand(t, repository, binary, "group", "accept", "--name", "example", "--path", "example.txt")
	var groups []groupStatus
	if err := json.Unmarshal([]byte(runCommand(t, repository, binary, "group", "list", "--json")), &groups); err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || len(groups[0].Fragments) != 1 || groups[0].Fragments[0].Status != "materialized" {
		t.Fatalf("groups = %#v, want retained materialized fragment", groups)
	}
}

func readChanges(t *testing.T, repository, binary string) []session.Fragment {
	t.Helper()
	var fragments []session.Fragment
	if err := json.Unmarshal([]byte(runCommand(t, repository, binary, "changes", "--json")), &fragments); err != nil {
		t.Fatal(err)
	}
	return fragments
}

func readStatus(t *testing.T, repository, binary string) session.Status {
	t.Helper()
	var status session.Status
	if err := json.Unmarshal([]byte(runCommand(t, repository, binary, "status", "--json")), &status); err != nil {
		t.Fatal(err)
	}
	return status
}

func fragmentBy(t *testing.T, fragments []session.Fragment, commit, path string) session.Fragment {
	t.Helper()
	for _, fragment := range fragments {
		if fragment.Commit == commit && fragment.Path == path {
			return fragment
		}
	}
	t.Fatalf("fragment %s:%s not found in %#v", commit, path, fragments)
	return session.Fragment{}
}

func assignFragment(t *testing.T, repository, binary, group string, fragment session.Fragment) {
	t.Helper()
	runCommand(t, repository, binary, "group", "assign", "--name", group, "--commit", fragment.Commit,
		"--path", fragment.Path, "--old-start", number(fragment.OldStart), "--old-lines", number(fragment.OldLines),
		"--new-start", number(fragment.NewStart), "--new-lines", number(fragment.NewLines))
}

func newRepository(t *testing.T, files map[string]string) string {
	t.Helper()
	repository := t.TempDir()
	runCommand(t, repository, "git", "init", "--initial-branch=main")
	runCommand(t, repository, "git", "config", "user.email", "test@example.com")
	runCommand(t, repository, "git", "config", "user.name", "Test User")
	for path, content := range files {
		writeFile(t, repository, path, content)
	}
	runCommand(t, repository, "git", "add", "-A")
	runCommand(t, repository, "git", "commit", "-m", "initial")
	return repository
}

func writeFile(t *testing.T, repository, path, content string) {
	t.Helper()
	fullPath := filepath.Join(repository, path)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func snapshotDirectory(t *testing.T, root string) map[string]string {
	t.Helper()
	snapshot := map[string]string{}
	err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if os.IsNotExist(err) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		snapshot[relative] = string(contents)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func buildBinary(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "git-review")
	runCommand(t, projectRoot(t), "go", "build", "-o", binary, ".")
	return binary
}

func number(value int) string {
	return fmt.Sprint(value)
}

func projectRoot(t *testing.T) string {
	t.Helper()
	directory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	return directory
}

func runCommand(t *testing.T, directory string, name string, args ...string) string {
	t.Helper()
	output, err := runCommandError(t, directory, name, args...)
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, output)
	}
	return output
}

func runCommandError(t *testing.T, directory string, name string, args ...string) (string, error) {
	t.Helper()
	command := exec.Command(name, args...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	return string(output), err
}
