package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/CypDasHuhn/git-review/internal/session"
)

func TestInitCreatesReviewSession(t *testing.T) {
	repository := t.TempDir()
	runCommand(t, repository, "git", "init", "--initial-branch=main")
	runCommand(t, repository, "git", "config", "user.email", "test@example.com")
	runCommand(t, repository, "git", "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(repository, "README.md"), []byte("initial\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runCommand(t, repository, "git", "add", "README.md")
	runCommand(t, repository, "git", "commit", "-m", "initial")

	binary := filepath.Join(t.TempDir(), "git-review")
	runCommand(t, projectRoot(t), "go", "build", "-o", binary, ".")
	runCommand(t, repository, binary, "init", "--base", "HEAD")

	rawStatus := runCommand(t, repository, binary, "status", "--json")
	var status session.Status
	if err := json.Unmarshal([]byte(rawStatus), &status); err != nil {
		t.Fatal(err)
	}
	if status.WorkBranch != "main" {
		t.Fatalf("work branch = %q, want main", status.WorkBranch)
	}
	if status.ComparisonBaseAdvanced {
		t.Fatal("comparison base unexpectedly advanced")
	}
	if status.ReviewedCommit != status.PinnedComparisonCommit {
		t.Fatalf("reviewed commit = %q, want %q", status.ReviewedCommit, status.PinnedComparisonCommit)
	}
	if value := runCommand(t, repository, "git", "config", "--local", "rerere.enabled"); value != "true\n" {
		t.Fatalf("rerere.enabled = %q, want true", value)
	}
}

func TestChangesAndGroupsTrackCurrentHunks(t *testing.T) {
	repository := t.TempDir()
	runCommand(t, repository, "git", "init", "--initial-branch=main")
	runCommand(t, repository, "git", "config", "user.email", "test@example.com")
	runCommand(t, repository, "git", "config", "user.name", "Test User")
	if err := os.Mkdir(filepath.Join(repository, "folder"), 0o755); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(repository, "folder", "example.txt")
	if err := os.WriteFile(file, []byte("one\ntwo\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runCommand(t, repository, "git", "add", "folder/example.txt")
	runCommand(t, repository, "git", "commit", "-m", "initial")

	binary := filepath.Join(t.TempDir(), "git-review")
	runCommand(t, projectRoot(t), "go", "build", "-o", binary, ".")
	runCommand(t, repository, binary, "init", "--base", "HEAD")
	if err := os.WriteFile(file, []byte("one\ntwo changed\nthree\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runCommand(t, repository, "git", "add", "folder/example.txt")
	runCommand(t, repository, "git", "commit", "-m", "change example")

	rawChanges := runCommand(t, repository, binary, "changes", "--json")
	var hunks []session.Hunk
	if err := json.Unmarshal([]byte(rawChanges), &hunks); err != nil {
		t.Fatal(err)
	}
	if len(hunks) != 1 {
		t.Fatalf("hunks = %d, want 1", len(hunks))
	}
	hunk := hunks[0]
	runCommand(t, repository, binary, "group", "assign", "--name", "formatting", "--path", hunk.Path,
		"--old-start", number(hunk.OldStart), "--old-lines", number(hunk.OldLines),
		"--new-start", number(hunk.NewStart), "--new-lines", number(hunk.NewLines))

	rawGroups := runCommand(t, repository, binary, "group", "list", "--json")
	var groups []session.Group
	if err := json.Unmarshal([]byte(rawGroups), &groups); err != nil {
		t.Fatal(err)
	}
	if len(groups) != 1 || groups[0].Name != "formatting" || len(groups[0].Hunks) != 1 || groups[0].Hunks[0] != hunk {
		t.Fatalf("groups = %#v, want one group containing %#v", groups, hunk)
	}
	runCommand(t, repository, binary, "group", "accept", "--name", "formatting", "--path", "folder")
	groups = nil
	if err := json.Unmarshal([]byte(runCommand(t, repository, binary, "group", "list", "--json")), &groups); err != nil {
		t.Fatal(err)
	}
	if len(groups) != 0 {
		t.Fatalf("groups = %#v, want empty after acceptance", groups)
	}
	reviewed := session.ReviewedRef("main")
	if content := runCommand(t, repository, "git", "show", reviewed+":folder/example.txt"); content != "one\ntwo changed\nthree\n" {
		t.Fatalf("reviewed content = %q", content)
	}
}

func TestGroupsPruneAcceptedHunks(t *testing.T) {
	repository := t.TempDir()
	runCommand(t, repository, "git", "init", "--initial-branch=main")
	runCommand(t, repository, "git", "config", "user.email", "test@example.com")
	runCommand(t, repository, "git", "config", "user.name", "Test User")
	file := filepath.Join(repository, "example.txt")
	if err := os.WriteFile(file, []byte("before\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runCommand(t, repository, "git", "add", "example.txt")
	runCommand(t, repository, "git", "commit", "-m", "initial")

	binary := filepath.Join(t.TempDir(), "git-review")
	runCommand(t, projectRoot(t), "go", "build", "-o", binary, ".")
	runCommand(t, repository, binary, "init", "--base", "HEAD")
	if err := os.WriteFile(file, []byte("after\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runCommand(t, repository, "git", "add", "example.txt")
	runCommand(t, repository, "git", "commit", "-m", "change example")

	var hunks []session.Hunk
	if err := json.Unmarshal([]byte(runCommand(t, repository, binary, "changes", "--json")), &hunks); err != nil {
		t.Fatal(err)
	}
	hunk := hunks[0]
	runCommand(t, repository, binary, "group", "assign", "--name", "example", "--path", hunk.Path,
		"--old-start", number(hunk.OldStart), "--old-lines", number(hunk.OldLines),
		"--new-start", number(hunk.NewStart), "--new-lines", number(hunk.NewLines))

	runCommand(t, repository, "git", "update-ref", session.ReviewedRef("main"), "HEAD")
	runCommand(t, repository, binary, "group", "accept", "--name", "example", "--path", "example.txt")
	var groups []session.Group
	if err := json.Unmarshal([]byte(runCommand(t, repository, binary, "group", "list", "--json")), &groups); err != nil {
		t.Fatal(err)
	}
	if len(groups) != 0 {
		t.Fatalf("groups = %#v, want empty after stale group acceptance", groups)
	}
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
	command := exec.Command(name, args...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, args, err, output)
	}
	return string(output)
}
