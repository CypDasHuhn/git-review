package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/CypDasHuhn/git-review/internal/session"
)

func TestVersionFlags(t *testing.T) {
	binary := buildBinary(t)
	for _, flag := range []string{"--version", "-v"} {
		if output := runCommand(t, projectRoot(t), binary, flag); output != "git-review 0.4.1\n" {
			t.Fatalf("%s output = %q, want git-review 0.4.1", flag, output)
		}
	}
}

func TestHelpListsFragmentWorkflow(t *testing.T) {
	binary := buildBinary(t)
	rootHelp := runCommand(t, projectRoot(t), binary, "--help")
	for _, line := range []string{
		"git-review fragment inspect --commit <commit> [--path <path>] [--json]",
		"git-review fragment list --json [--unassigned]",
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
