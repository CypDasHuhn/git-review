package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"github.com/CypDasHuhn/git-review/internal/repository"
	"github.com/CypDasHuhn/git-review/internal/session"
)

func main() {
	if err := run(context.Background(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "git-review:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return usage()
	}

	switch args[0] {
	case "init":
		return initialize(ctx, args[1:])
	case "status":
		return showStatus(ctx, args[1:])
	case "accept":
		return accept(ctx, args[1:])
	case "undo":
		return undo(ctx, args[1:])
	case "history":
		return history(ctx)
	case "reset":
		return reset(ctx, args[1:])
	case "changes":
		return changes(ctx, args[1:])
	case "group":
		return group(ctx, args[1:])
	case "help", "--help", "-h":
		return usage()
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func initialize(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("init", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	base := flags.String("base", "", "comparison base ref")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("init does not accept positional arguments")
	}

	repo, err := repository.Open(ctx, "")
	if err != nil {
		return err
	}

	workBranch, err := repo.CurrentBranch(ctx)
	if err != nil {
		return err
	}
	comparisonRef := *base
	if comparisonRef == "" {
		comparisonRef, err = repo.DefaultRemoteHead(ctx)
		if err != nil {
			return fmt.Errorf("determine comparison base: pass --base explicitly: %w", err)
		}
	}

	comparisonCommit, err := repo.ResolveCommit(ctx, comparisonRef)
	if err != nil {
		return fmt.Errorf("resolve comparison base %q: %w", comparisonRef, err)
	}
	reviewedRef := session.ReviewedRef(workBranch)
	store := session.NewStore(repo.GitDir)
	if _, err := store.Load(workBranch); err == nil {
		return fmt.Errorf("a review session already exists for %q", workBranch)
	} else if !errors.Is(err, session.ErrNotFound) {
		return err
	}

	if err := repo.EnableRerere(ctx); err != nil {
		return err
	}
	if err := repo.CreateRef(ctx, reviewedRef, comparisonCommit); err != nil {
		return err
	}

	currentCommit, err := repo.ResolveCommit(ctx, "HEAD")
	if err != nil {
		return err
	}
	state := session.State{
		WorkBranch:             workBranch,
		ComparisonRef:          comparisonRef,
		PinnedComparisonCommit: comparisonCommit,
		ReviewedRef:            reviewedRef,
		StartedFromWorkCommit:  currentCommit,
	}
	if err := store.Save(state); err != nil {
		return err
	}

	fmt.Printf("Started review for %s against %s at %s\n", workBranch, comparisonRef, comparisonCommit)
	return nil
}

func showStatus(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("status", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	asJSON := flags.Bool("json", false, "write structured JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("status does not accept positional arguments")
	}

	repo, err := repository.Open(ctx, "")
	if err != nil {
		return err
	}
	workBranch, err := repo.CurrentBranch(ctx)
	if err != nil {
		return err
	}
	state, err := session.NewStore(repo.GitDir).Load(workBranch)
	if err != nil {
		if errors.Is(err, session.ErrNotFound) {
			return fmt.Errorf("no review session exists for %q", workBranch)
		}
		return err
	}

	head, err := repo.ResolveCommit(ctx, "HEAD")
	if err != nil {
		return err
	}
	reviewed, err := repo.ResolveCommit(ctx, state.ReviewedRef)
	if err != nil {
		return fmt.Errorf("resolve reviewed state: %w", err)
	}
	currentComparison, err := repo.ResolveCommit(ctx, state.ComparisonRef)
	if err != nil {
		return fmt.Errorf("resolve comparison base: %w", err)
	}

	status := session.Status{
		State:                   state,
		WorkCommit:              head,
		ReviewedCommit:          reviewed,
		CurrentComparisonCommit: currentComparison,
		ComparisonBaseAdvanced:  currentComparison != state.PinnedComparisonCommit,
	}
	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(status)
	}

	fmt.Printf("Work branch:      %s\n", status.WorkBranch)
	fmt.Printf("Work commit:      %s\n", status.WorkCommit)
	fmt.Printf("Reviewed state:   %s\n", status.ReviewedCommit)
	fmt.Printf("Comparison base:  %s\n", status.ComparisonRef)
	fmt.Printf("Pinned base:      %s\n", status.PinnedComparisonCommit)
	if status.ComparisonBaseAdvanced {
		fmt.Printf("Base update:      available (%s)\n", status.CurrentComparisonCommit)
	} else {
		fmt.Println("Base update:      not needed")
	}
	return nil
}

func accept(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("accept", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	var paths stringList
	flags.Var(&paths, "path", "path to accept")
	subject := flags.String("subject", "", "acceptance summary")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if len(paths) == 0 || flags.NArg() != 0 {
		return errors.New("accept requires one or more --path values")
	}
	repo, state, err := currentSession(ctx)
	if err != nil {
		return err
	}
	if *subject == "" {
		*subject = strings.Join(paths, ", ")
	}
	changed, err := repo.AcceptPaths(ctx, state.ReviewedRef, paths, *subject)
	if err != nil {
		return err
	}
	if changed {
		fmt.Printf("Accepted %s\n", *subject)
	} else {
		fmt.Printf("Already accepted %s\n", *subject)
	}
	return nil
}

func undo(ctx context.Context, args []string) error {
	if len(args) != 0 {
		return errors.New("undo does not accept arguments")
	}
	repo, state, err := currentSession(ctx)
	if err != nil {
		return err
	}
	undone, subject, err := repo.UndoAcceptance(ctx, state.ReviewedRef)
	if err != nil {
		return err
	}
	if undone {
		fmt.Printf("Un-accepted %s\n", subject)
	} else {
		fmt.Println(subject)
	}
	return nil
}

func history(ctx context.Context) error {
	repo, state, err := currentSession(ctx)
	if err != nil {
		return err
	}
	output, err := repo.Log(ctx, state.ReviewedRef)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(strings.FieldsFunc(output, func(r rune) bool { return r == '\n' }))
}

func reset(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("reset", flag.ContinueOnError)
	commit := flags.String("commit", "", "accepted commit")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *commit == "" {
		return errors.New("reset requires --commit")
	}
	repo, state, err := currentSession(ctx)
	if err != nil {
		return err
	}
	resolved, err := repo.ResolveCommit(ctx, *commit)
	if err != nil {
		return err
	}
	return repo.ResetRef(ctx, state.ReviewedRef, resolved)
}

func changes(ctx context.Context, args []string) error {
	if len(args) != 1 || args[0] != "--json" {
		return errors.New("changes requires --json")
	}
	repo, state, err := currentSession(ctx)
	if err != nil {
		return err
	}
	hunks, err := repo.DiffHunks(ctx, state.ReviewedRef, "HEAD")
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(hunks)
}

func group(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("group requires assign or list")
	}
	switch args[0] {
	case "assign":
		return assignGroup(ctx, args[1:])
	case "list":
		return listGroups(ctx, args[1:])
	case "view":
		return viewGroup(ctx, args[1:])
	case "accept":
		return acceptGroup(ctx, args[1:])
	default:
		return fmt.Errorf("unknown group command %q", args[0])
	}
}

func acceptGroup(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("group accept", flag.ContinueOnError)
	name := flags.String("name", "", "review group name")
	path := flags.String("path", "", "changed file path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *name == "" || *path == "" {
		return errors.New("group accept requires --name and --path")
	}
	repo, state, err := currentSession(ctx)
	if err != nil {
		return err
	}
	store := session.NewStore(repo.GitDir)
	groups, err := store.LoadGroups(state.WorkBranch)
	if err != nil {
		return err
	}
	for index := range groups {
		if groups[index].Name != *name {
			continue
		}
		var selected, remaining []session.Hunk
		for _, hunk := range groups[index].Hunks {
			if hunk.Path == *path {
				selected = append(selected, hunk)
			} else {
				remaining = append(remaining, hunk)
			}
		}
		if len(selected) == 0 {
			return fmt.Errorf("review group %q has no hunks for %q", *name, *path)
		}
		commit, err := repo.CreateGroupProjection(ctx, state.ReviewedRef, "HEAD", session.GroupRef(state.WorkBranch, "accept-"+*name+"-"+*path), "accept "+*name+": "+*path, selected)
		if err != nil {
			return err
		}
		if err := repo.ResetRef(ctx, state.ReviewedRef, commit); err != nil {
			return err
		}
		if len(remaining) == 0 {
			groups = append(groups[:index], groups[index+1:]...)
		} else {
			groups[index].Hunks = remaining
		}
		if err := store.SaveGroups(state.WorkBranch, groups); err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]string{"commit": commit})
	}
	return fmt.Errorf("review group %q does not exist", *name)
}

func viewGroup(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("group view", flag.ContinueOnError)
	name := flags.String("name", "", "review group name")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *name == "" {
		return errors.New("group view requires --name")
	}
	repo, state, err := currentSession(ctx)
	if err != nil {
		return err
	}
	groups, err := session.NewStore(repo.GitDir).LoadGroups(state.WorkBranch)
	if err != nil {
		return err
	}
	for _, group := range groups {
		if group.Name != *name {
			continue
		}
		ref := session.GroupRef(state.WorkBranch, group.Name)
		commit, err := repo.CreateGroupProjection(ctx, state.ReviewedRef, "HEAD", ref, group.Name, group.Hunks)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]string{"ref": ref, "commit": commit, "reviewedRef": state.ReviewedRef})
	}
	return fmt.Errorf("review group %q does not exist", *name)
}

func assignGroup(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("group assign", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	name := flags.String("name", "", "review group name")
	path := flags.String("path", "", "changed file path")
	oldStart := flags.Int("old-start", -1, "old hunk start")
	oldLines := flags.Int("old-lines", -1, "old hunk line count")
	newStart := flags.Int("new-start", -1, "new hunk start")
	newLines := flags.Int("new-lines", -1, "new hunk line count")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *name == "" || *path == "" || *oldStart < 0 || *oldLines < 0 || *newStart < 0 || *newLines < 0 {
		return errors.New("group assign requires --name, --path, --old-start, --old-lines, --new-start, and --new-lines")
	}
	repo, state, err := currentSession(ctx)
	if err != nil {
		return err
	}
	hunk := session.Hunk{Path: *path, OldStart: *oldStart, OldLines: *oldLines, NewStart: *newStart, NewLines: *newLines}
	hunks, err := repo.DiffHunks(ctx, state.ReviewedRef, "HEAD")
	if err != nil {
		return err
	}
	if !containsHunk(hunks, hunk) {
		return errors.New("the requested hunk is not in the current review diff")
	}
	store := session.NewStore(repo.GitDir)
	groups, err := store.LoadGroups(state.WorkBranch)
	if err != nil {
		return err
	}
	for index := range groups {
		if groups[index].Name == *name {
			if !containsHunk(groups[index].Hunks, hunk) {
				groups[index].Hunks = append(groups[index].Hunks, hunk)
			}
			return store.SaveGroups(state.WorkBranch, groups)
		}
	}
	return store.SaveGroups(state.WorkBranch, append(groups, session.Group{Name: *name, Hunks: []session.Hunk{hunk}}))
}

func listGroups(ctx context.Context, args []string) error {
	if len(args) != 1 || args[0] != "--json" {
		return errors.New("group list requires --json")
	}
	repo, state, err := currentSession(ctx)
	if err != nil {
		return err
	}
	groups, err := session.NewStore(repo.GitDir).LoadGroups(state.WorkBranch)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(groups)
}

func containsHunk(hunks []session.Hunk, candidate session.Hunk) bool {
	for _, hunk := range hunks {
		if hunk == candidate {
			return true
		}
	}
	return false
}

func currentSession(ctx context.Context) (repository.Repository, session.State, error) {
	repo, err := repository.Open(ctx, "")
	if err != nil {
		return repository.Repository{}, session.State{}, err
	}
	workBranch, err := repo.CurrentBranch(ctx)
	if err != nil {
		return repository.Repository{}, session.State{}, err
	}
	state, err := session.NewStore(repo.GitDir).Load(workBranch)
	if errors.Is(err, session.ErrNotFound) {
		return repository.Repository{}, session.State{}, fmt.Errorf("no review session exists for %q", workBranch)
	}
	return repo, state, err
}

type stringList []string

func (s *stringList) String() string {
	return strings.Join(*s, ",")
}

func (s *stringList) Set(value string) error {
	*s = append(*s, value)
	return nil
}

func usage() error {
	_, err := fmt.Fprint(os.Stderr, strings.TrimSpace(`Usage:
  git-review init [--base <ref>]
  git-review status [--json]
	git-review accept --path <path> [--path <path>...] [--subject <summary>]
	git-review undo
	git-review history
	git-review reset --commit <commit>
`)+"\n")
	return err
}
