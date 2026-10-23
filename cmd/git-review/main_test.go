package main

import (
	"encoding/json"
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
