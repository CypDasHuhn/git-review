package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/CypDasHuhn/git-review/internal/repository"
	"github.com/CypDasHuhn/git-review/internal/session"
)

const version = "0.4.1"

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
	case "--version", "-v":
		return showVersion(args[1:])
	case "init":
		return initialize(ctx, args[1:])
	case "sync-base":
		return syncBase(ctx, args[1:])
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
	case "fragment":
		return fragment(ctx, args[1:])
	case "group":
		return group(ctx, args[1:])
	case "help", "--help", "-h":
		return usage()
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func showVersion(args []string) error {
	if len(args) != 0 {
		return errors.New("version does not accept arguments")
	}
	fmt.Printf("git-review %s\n", version)
	return nil
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
	store := session.NewStore(repo.GitDir)
	if _, err := store.Load(workBranch); err == nil {
		return fmt.Errorf("a review session already exists for %q", workBranch)
	} else if !errors.Is(err, session.ErrNotFound) {
		return err
	}
	if err := repo.EnableRerere(ctx); err != nil {
		return err
	}
	if err := repo.InstallPostMergeHook(); err != nil {
		return err
	}
	reviewedRef := session.ReviewedRef(workBranch)
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
		InitialReviewedCommit:  comparisonCommit,
		ReviewedRef:            reviewedRef,
		StartedFromWorkCommit:  currentCommit,
	}
	if err := store.Save(state); err != nil {
		return err
	}
	fmt.Printf("Started review for %s against %s at %s\n", workBranch, comparisonRef, comparisonCommit)
	return nil
}

func syncBase(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("sync-base", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	hook := flags.Bool("hook", false, "run from the post-merge hook")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("sync-base does not accept positional arguments")
	}
	repo, state, err := currentSession(ctx)
	if err != nil {
		if *hook && errors.Is(err, session.ErrNotFound) {
			return nil
		}
		return err
	}
	if err := repo.InstallPostMergeHook(); err != nil {
		if *hook {
			fmt.Fprintln(os.Stderr, "git-review:", err)
			return nil
		}
		return err
	}
	currentComparison, err := repo.ResolveCommit(ctx, state.ComparisonRef)
	if err != nil {
		if *hook {
			fmt.Fprintln(os.Stderr, "git-review:", err)
			return nil
		}
		return fmt.Errorf("resolve comparison base: %w", err)
	}
	if currentComparison == state.PinnedComparisonCommit {
		if !*hook {
			fmt.Println("Review base is already synchronized")
		}
		return nil
	}
	contained, err := repo.IsAncestor(ctx, currentComparison, "HEAD")
	if err != nil {
		if *hook {
			fmt.Fprintln(os.Stderr, "git-review:", err)
			return nil
		}
		return err
	}
	if !contained {
		if *hook {
			return nil
		}
		return fmt.Errorf("comparison base %s is not contained in the work branch; merge %s into the work branch first", currentComparison, state.ComparisonRef)
	}
	reviewedContainsBase, err := repo.IsAncestor(ctx, currentComparison, state.ReviewedRef)
	if err != nil {
		if *hook {
			fmt.Fprintln(os.Stderr, "git-review:", err)
			return nil
		}
		return err
	}
	if !reviewedContainsBase {
		if _, err := repo.MergeRef(ctx, state.ReviewedRef, currentComparison, "merge review base: "+state.ComparisonRef); err != nil {
			if *hook {
				fmt.Fprintln(os.Stderr, "git-review:", err)
				return nil
			}
			return err
		}
	}
	state.PinnedComparisonCommit = currentComparison
	if err := session.NewStore(repo.GitDir).Save(state); err != nil {
		if *hook {
			fmt.Fprintln(os.Stderr, "git-review:", err)
			return nil
		}
		return err
	}
	if !*hook {
		fmt.Printf("Synchronized review base to %s\n", currentComparison)
	}
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
	repo, state, err := currentSession(ctx)
	if err != nil {
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
	fragments, err := allFragments(ctx, repo, state)
	if err != nil {
		return err
	}
	groups, err := session.NewStore(repo.GitDir).LoadGroups(state.WorkBranch)
	if err != nil {
		return err
	}
	fragmentStatuses, err := fragmentStatuses(ctx, repo, state, groups)
	if err != nil {
		return err
	}
	assignedCount := 0
	for _, fragment := range fragmentStatuses {
		if fragment.Assignment == "assigned" {
			assignedCount++
		}
	}
	accepted := stringSet(state.AcceptedFragments)
	materialized := stringSet(state.MaterializedFragments)
	status := session.Status{
		State:                   state,
		WorkCommit:              head,
		ReviewedCommit:          reviewed,
		CurrentComparisonCommit: currentComparison,
		ComparisonBaseAdvanced:  currentComparison != state.PinnedComparisonCommit,
		ChangeCount:             len(fragments),
		AssignedCount:           assignedCount,
		UnassignedCount:         len(fragments) - assignedCount,
		AcceptedCount:           len(accepted),
		MaterializedCount:       len(materialized),
		PendingCount:            countPending(accepted, materialized),
	}
	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(status)
	}
	fmt.Printf("Work branch:      %s\n", status.WorkBranch)
	fmt.Printf("Work commit:      %s\n", status.WorkCommit)
	fmt.Printf("Reviewed state:   %s\n", status.ReviewedCommit)
	fmt.Printf("Comparison base:  %s\n", status.ComparisonRef)
	fmt.Printf("Pinned base:      %s\n", status.PinnedComparisonCommit)
	fmt.Printf("Change fragments: %d\n", status.ChangeCount)
	fmt.Printf("Assigned:         %d\n", status.AssignedCount)
	fmt.Printf("Unassigned:       %d\n", status.UnassignedCount)
	fmt.Printf("Accepted:         %d\n", status.AcceptedCount)
	fmt.Printf("Materialized:     %d\n", status.MaterializedCount)
	fmt.Printf("Pending apply:    %d\n", status.PendingCount)
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
	fragments, err := allFragments(ctx, repo, state)
	if err != nil {
		return err
	}
	accepted := stringSet(state.AcceptedFragments)
	var newlyAccepted []string
	for _, fragment := range fragments {
		for _, path := range paths {
			if fragment.TouchesPath(path) {
				if !accepted[fragment.ID()] {
					accepted[fragment.ID()] = true
					newlyAccepted = append(newlyAccepted, fragment.ID())
				}
			}
		}
	}
	state.AcceptedFragments = appendUnique(state.AcceptedFragments, newlyAccepted...)
	store := session.NewStore(repo.GitDir)
	if err := store.Save(state); err != nil {
		return err
	}
	materialized, err := materializeAccepted(ctx, repo, &state, store, fragments)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{
		"accepted":     state.AcceptedFragments,
		"materialized": materialized,
		"pending":      pendingFragmentIDs(state),
	})
}

func undo(ctx context.Context, args []string) error {
	if len(args) != 0 {
		return errors.New("undo does not accept arguments")
	}
	repo, state, err := currentSession(ctx)
	if err != nil {
		return err
	}
	accepted := stringSet(state.AcceptedFragments)
	if len(state.AcceptedFragments) > 0 {
		lastAccepted := state.AcceptedFragments[len(state.AcceptedFragments)-1]
		if !stringSet(state.MaterializedFragments)[lastAccepted] {
			state.AcceptedFragments = state.AcceptedFragments[:len(state.AcceptedFragments)-1]
			if err := session.NewStore(repo.GitDir).Save(state); err != nil {
				return err
			}
			fmt.Printf("Un-accepted pending fragment %s\n", lastAccepted)
			return nil
		}
	}
	if len(state.MaterializedFragments) == 0 {
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
	last := state.MaterializedFragments[len(state.MaterializedFragments)-1]
	undone, subject, err := repo.UndoAcceptance(ctx, state.ReviewedRef)
	if err != nil {
		return err
	}
	if !undone {
		return errors.New("reviewed state history does not contain a materialized fragment")
	}
	state.MaterializedFragments = state.MaterializedFragments[:len(state.MaterializedFragments)-1]
	if accepted[last] {
		state.AcceptedFragments = removeString(state.AcceptedFragments, last)
	}
	if err := session.NewStore(repo.GitDir).Save(state); err != nil {
		return err
	}
	fmt.Printf("Un-accepted %s\n", subject)
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
	commit := flags.String("commit", "", "reviewed state commit")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *commit == "" {
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
	if err := repo.ResetRef(ctx, state.ReviewedRef, resolved); err != nil {
		return err
	}
	state.AcceptedFragments = nil
	state.MaterializedFragments = nil
	if err := session.NewStore(repo.GitDir).Save(state); err != nil {
		return err
	}
	fmt.Printf("Reset reviewed state to %s\n", resolved)
	return nil
}

func changes(ctx context.Context, args []string) error {
	if len(args) != 1 || args[0] != "--json" {
		return errors.New("changes requires --json")
	}
	repo, state, err := currentSession(ctx)
	if err != nil {
		return err
	}
	fragments, err := allFragments(ctx, repo, state)
	if err != nil {
		return err
	}
	accepted := stringSet(state.AcceptedFragments)
	var pending []session.Fragment
	for _, fragment := range fragments {
		if !accepted[fragment.ID()] {
			pending = append(pending, fragment)
		}
	}
	return json.NewEncoder(os.Stdout).Encode(pending)
}

func fragment(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		return fragmentUsage()
	}
	switch args[0] {
	case "inspect":
		return inspectFragments(ctx, args[1:])
	case "list":
		return listFragments(ctx, args[1:])
	default:
		return fmt.Errorf("unknown fragment command %q", args[0])
	}
}

func inspectFragments(ctx context.Context, args []string) error {
	if helpRequested(args) {
		return fragmentInspectUsage()
	}
	flags := flag.NewFlagSet("fragment inspect", flag.ContinueOnError)
	commitRef := flags.String("commit", "", "commit hash")
	path := flags.String("path", "", "changed file path")
	asJSON := flags.Bool("json", false, "write structured JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *commitRef == "" {
		return errors.New("fragment inspect requires --commit")
	}
	repo, state, err := currentSession(ctx)
	if err != nil {
		return err
	}
	commit, err := repo.CommitInfo(ctx, *commitRef)
	if err != nil {
		return fmt.Errorf("resolve commit %q: %w", *commitRef, err)
	}
	patches, err := repo.CommitFragments(ctx, commit.Hash)
	if err != nil {
		return err
	}
	groups, err := session.NewStore(repo.GitDir).LoadGroups(state.WorkBranch)
	if err != nil {
		return err
	}
	statuses, err := fragmentStatuses(ctx, repo, state, groups)
	if err != nil {
		return err
	}
	byID := make(map[string]fragmentStatus, len(statuses))
	for _, status := range statuses {
		byID[status.ID] = status
	}
	var views []fragmentView
	for _, patch := range patches {
		if *path != "" && !patch.Fragment.TouchesPath(*path) {
			continue
		}
		status, found := byID[patch.Fragment.ID()]
		if !found {
			status = fragmentStatus{
				Fragment:   patch.Fragment,
				ID:         patch.Fragment.ID(),
				ShortID:    patch.Fragment.ShortID(),
				Status:     "outside-review",
				Assignment: "unassigned",
				Assignable: false,
			}
		}
		views = append(views, fragmentView{fragmentStatus: status, Patch: formatFragmentPatch(patch)})
	}
	if len(views) == 0 {
		return fmt.Errorf("commit %s has no matching fragments", commit.ShortHash)
	}
	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(views)
	}
	fmt.Printf("Commit: %s\n", commit.Hash)
	for _, view := range views {
		group := view.Group
		if group == "" {
			group = "-"
		}
		fmt.Printf("%s %s %s %s %s\n", view.ShortID, view.ID, view.Path, view.Status, group)
		fmt.Print(view.Patch)
	}
	return nil
}

func listFragments(ctx context.Context, args []string) error {
	if helpRequested(args) {
		return fragmentListUsage()
	}
	flags := flag.NewFlagSet("fragment list", flag.ContinueOnError)
	asJSON := flags.Bool("json", false, "write structured JSON")
	unassigned := flags.Bool("unassigned", false, "list only unassigned fragments")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || !*asJSON {
		return errors.New("fragment list requires --json")
	}
	repo, state, err := currentSession(ctx)
	if err != nil {
		return err
	}
	groups, err := session.NewStore(repo.GitDir).LoadGroups(state.WorkBranch)
	if err != nil {
		return err
	}
	statuses, err := fragmentStatuses(ctx, repo, state, groups)
	if err != nil {
		return err
	}
	if *unassigned {
		filtered := statuses[:0]
		for _, status := range statuses {
			if status.Assignment == "unassigned" {
				filtered = append(filtered, status)
			}
		}
		statuses = filtered
	}
	return json.NewEncoder(os.Stdout).Encode(statuses)
}

func group(ctx context.Context, args []string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		return groupUsage()
	}
	switch args[0] {
	case "assign":
		return assignGroup(ctx, args[1:])
	case "add":
		return addGroupCommit(ctx, args[1:])
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
	if helpRequested(args) {
		return groupAcceptUsage()
	}
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
	var selected []session.Fragment
	for _, group := range groups {
		if group.Name != *name {
			continue
		}
		for _, fragment := range group.Fragments {
			if fragment.TouchesPath(*path) {
				selected = append(selected, fragment)
			}
		}
		if len(selected) == 0 {
			return fmt.Errorf("review group %q has no fragments for %q", *name, *path)
		}
		break
	}
	if len(selected) == 0 {
		return fmt.Errorf("review group %q does not exist", *name)
	}
	accepted := stringSet(state.AcceptedFragments)
	var newlyAccepted []string
	for _, fragment := range selected {
		if !accepted[fragment.ID()] {
			accepted[fragment.ID()] = true
			newlyAccepted = append(newlyAccepted, fragment.ID())
		}
	}
	state.AcceptedFragments = appendUnique(state.AcceptedFragments, newlyAccepted...)
	if err := store.Save(state); err != nil {
		return err
	}
	fragments, err := allFragments(ctx, repo, state)
	if err != nil {
		return err
	}
	materialized, err := materializeAccepted(ctx, repo, &state, store, fragments)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{
		"group":        *name,
		"accepted":     fragmentIDs(selected),
		"materialized": materialized,
		"pending":      pendingFragmentIDs(state),
	})
}

func viewGroup(ctx context.Context, args []string) error {
	if helpRequested(args) {
		return groupViewUsage()
	}
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
	statuses, err := groupStatuses(ctx, repo, state, groups)
	if err != nil {
		return err
	}
	for _, group := range statuses {
		if group.Name != *name {
			continue
		}
		views := make([]fragmentView, 0, len(group.Fragments))
		for _, fragment := range group.Fragments {
			patch, err := repo.FragmentPatch(ctx, fragment.Fragment)
			if err != nil {
				return err
			}
			views = append(views, fragmentView{fragmentStatus: fragment, Patch: formatFragmentPatch(patch)})
		}
		return json.NewEncoder(os.Stdout).Encode(map[string]any{
			"name":        group.Name,
			"reviewedRef": state.ReviewedRef,
			"fragments":   views,
		})
	}
	return fmt.Errorf("review group %q does not exist", *name)
}

func assignGroup(ctx context.Context, args []string) error {
	if helpRequested(args) {
		return groupAssignUsage()
	}
	flags := flag.NewFlagSet("group assign", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	name := flags.String("name", "", "review group name")
	commit := flags.String("commit", "", "commit hash")
	var fragmentIDs stringList
	flags.Var(&fragmentIDs, "fragment", "fragment ID")
	path := flags.String("path", "", "changed file path")
	oldStart := flags.Int("old-start", -1, "source hunk old start")
	oldLines := flags.Int("old-lines", -1, "source hunk old line count")
	newStart := flags.Int("new-start", -1, "source hunk new start")
	newLines := flags.Int("new-lines", -1, "source hunk new line count")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *name == "" {
		return errors.New("group assign requires --name and --commit")
	}
	if len(fragmentIDs) > 0 {
		if *commit != "" || *path != "" || *oldStart >= 0 || *oldLines >= 0 || *newStart >= 0 || *newLines >= 0 {
			return errors.New("group assign with --fragment requires --name and --fragment only")
		}
		return assignFragmentIDs(ctx, *name, fragmentIDs)
	}
	if *commit == "" {
		return errors.New("group assign requires --name and --commit or --fragment")
	}
	return assignFragments(ctx, *name, *commit, *path, *oldStart, *oldLines, *newStart, *newLines)
}

func addGroupCommit(ctx context.Context, args []string) error {
	if helpRequested(args) {
		return groupAddUsage()
	}
	flags := flag.NewFlagSet("group add", flag.ContinueOnError)
	name := flags.String("name", "", "review group name")
	commit := flags.String("commit", "", "commit hash")
	path := flags.String("path", "", "changed file path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	positional := flags.NArg()
	if *commit == "" && positional == 1 {
		*commit = flags.Arg(0)
		positional = 0
	}
	if positional != 0 || *name == "" || *commit == "" {
		return errors.New("group add requires --name and --commit")
	}
	return assignFragments(ctx, *name, *commit, *path, -1, -1, -1, -1)
}

func assignFragmentIDs(ctx context.Context, name string, references []string) error {
	repo, state, err := currentSession(ctx)
	if err != nil {
		return err
	}
	fragments, err := allFragments(ctx, repo, state)
	if err != nil {
		return err
	}
	var selected []session.Fragment
	seen := map[string]bool{}
	for _, reference := range references {
		fragment, err := resolveFragmentID(fragments, reference)
		if err != nil {
			return err
		}
		if !seen[fragment.ID()] {
			selected = append(selected, fragment)
			seen[fragment.ID()] = true
		}
	}
	store := session.NewStore(repo.GitDir)
	groups, err := store.LoadGroups(state.WorkBranch)
	if err != nil {
		return err
	}
	groupIndex := -1
	for index := range groups {
		if groups[index].Name == name {
			groupIndex = index
			break
		}
	}
	if groupIndex < 0 {
		groups = append(groups, session.Group{Name: name})
		groupIndex = len(groups) - 1
	}
	for _, candidate := range selected {
		for index, group := range groups {
			if index == groupIndex {
				continue
			}
			if containsFragment(group.Fragments, candidate) {
				return fmt.Errorf("fragment %s already belongs to review group %q", candidate.Label(), group.Name)
			}
		}
		if !containsFragment(groups[groupIndex].Fragments, candidate) {
			groups[groupIndex].Fragments = append(groups[groupIndex].Fragments, candidate)
		}
	}
	if err := validateGroupGraph(ctx, repo, state, groups); err != nil {
		return err
	}
	if err := store.SaveGroups(state.WorkBranch, groups); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{
		"group":     name,
		"fragments": fragmentIDs(selected),
	})
}

func resolveFragmentID(fragments []session.Fragment, reference string) (session.Fragment, error) {
	if reference == "" {
		return session.Fragment{}, errors.New("fragment ID cannot be empty")
	}
	var matches []session.Fragment
	for _, fragment := range fragments {
		if fragment.ID() == reference || strings.HasPrefix(fragment.ID(), reference) {
			matches = append(matches, fragment)
		}
	}
	if len(matches) == 0 {
		return session.Fragment{}, fmt.Errorf("unknown fragment ID %q", reference)
	}
	if len(matches) > 1 {
		ids := fragmentIDs(matches)
		return session.Fragment{}, fmt.Errorf("fragment ID %q is ambiguous; use one of %s", reference, strings.Join(ids, ", "))
	}
	return matches[0], nil
}

func assignFragments(ctx context.Context, name, ref, path string, oldStart, oldLines, newStart, newLines int) error {
	repo, state, err := currentSession(ctx)
	if err != nil {
		return err
	}
	commit, err := repo.CommitInfo(ctx, ref)
	if err != nil {
		return fmt.Errorf("resolve commit %q: %w", ref, err)
	}
	inReview, err := repo.IsAncestor(ctx, state.PinnedComparisonCommit, commit.Hash)
	if err != nil {
		return err
	}
	if !inReview {
		return fmt.Errorf("commit %s is before the review base", commit.ShortHash)
	}
	inWork, err := repo.IsAncestor(ctx, commit.Hash, "HEAD")
	if err != nil {
		return err
	}
	if !inWork {
		return fmt.Errorf("commit %s is not contained in the work branch", commit.ShortHash)
	}
	patches, err := repo.CommitFragments(ctx, commit.Hash)
	if err != nil {
		return fmt.Errorf("find fragments for commit %s: %w", commit.ShortHash, err)
	}
	specific := oldStart >= 0 || oldLines >= 0 || newStart >= 0 || newLines >= 0
	if specific && (path == "" || oldStart < 0 || oldLines < 0 || newStart < 0 || newLines < 0) {
		return errors.New("fragment selection requires --path and all source range flags")
	}
	var selected []session.Fragment
	for _, patch := range patches {
		fragment := patch.Fragment
		if path != "" && !fragment.TouchesPath(path) {
			continue
		}
		if specific && (fragment.OldStart != oldStart || fragment.OldLines != oldLines || fragment.NewStart != newStart || fragment.NewLines != newLines) {
			continue
		}
		selected = append(selected, fragment)
	}
	if len(selected) == 0 {
		return fmt.Errorf("commit %s has no matching review fragments", commit.ShortHash)
	}
	store := session.NewStore(repo.GitDir)
	groups, err := store.LoadGroups(state.WorkBranch)
	if err != nil {
		return err
	}
	groupIndex := -1
	for index := range groups {
		if groups[index].Name == name {
			groupIndex = index
			break
		}
	}
	if groupIndex < 0 {
		groups = append(groups, session.Group{Name: name})
		groupIndex = len(groups) - 1
	}
	for _, candidate := range selected {
		for index, group := range groups {
			for _, existing := range group.Fragments {
				if existing.ID() != candidate.ID() {
					continue
				}
				if index != groupIndex {
					return fmt.Errorf("fragment %s already belongs to review group %q", candidate.Label(), group.Name)
				}
			}
		}
		if !containsFragment(groups[groupIndex].Fragments, candidate) {
			groups[groupIndex].Fragments = append(groups[groupIndex].Fragments, candidate)
		}
	}
	if err := validateGroupGraph(ctx, repo, state, groups); err != nil {
		return err
	}
	if err := store.SaveGroups(state.WorkBranch, groups); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{
		"group":       name,
		"commit":      commit.Hash,
		"shortCommit": commit.ShortHash,
		"fragments":   fragmentIDs(selected),
	})
}

func listGroups(ctx context.Context, args []string) error {
	if helpRequested(args) {
		return groupListUsage()
	}
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
	statuses, err := groupStatuses(ctx, repo, state, groups)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(statuses)
}

type fragmentStatus struct {
	session.Fragment
	ID           string   `json:"id"`
	ShortID      string   `json:"shortId"`
	Status       string   `json:"status"`
	Assignment   string   `json:"assignment"`
	Group        string   `json:"group,omitempty"`
	Assignable   bool     `json:"assignable"`
	Dependencies []string `json:"dependencies,omitempty"`
}

type fragmentView struct {
	fragmentStatus
	Patch string `json:"patch"`
}

type groupStatus struct {
	Name      string           `json:"name"`
	Fragments []fragmentStatus `json:"fragments"`
}

func formatFragmentPatch(patch repository.FragmentPatch) string {
	var builder strings.Builder
	fmt.Fprintf(&builder, "@@ -%d,%d +%d,%d @@\n", patch.Fragment.OldStart, patch.Fragment.OldLines, patch.Fragment.NewStart, patch.Fragment.NewLines)
	for _, line := range patch.OldLines {
		builder.WriteString("-")
		builder.WriteString(line)
		builder.WriteString("\n")
	}
	for _, line := range patch.NewLines {
		builder.WriteString("+")
		builder.WriteString(line)
		builder.WriteString("\n")
	}
	return builder.String()
}

func groupStatuses(ctx context.Context, repo repository.Repository, state session.State, groups []session.Group) ([]groupStatus, error) {
	fragments, err := fragmentStatuses(ctx, repo, state, groups)
	if err != nil {
		return nil, err
	}
	byID := make(map[string]fragmentStatus, len(fragments))
	for _, fragment := range fragments {
		byID[fragment.ID] = fragment
	}
	statuses := make([]groupStatus, 0, len(groups))
	for _, group := range groups {
		status := groupStatus{Name: group.Name}
		for _, fragment := range group.Fragments {
			if fragmentStatus, found := byID[fragment.ID()]; found {
				status.Fragments = append(status.Fragments, fragmentStatus)
			}
		}
		statuses = append(statuses, status)
	}
	return statuses, nil
}

func fragmentStatuses(ctx context.Context, repo repository.Repository, state session.State, groups []session.Group) ([]fragmentStatus, error) {
	fragments, err := allFragments(ctx, repo, state)
	if err != nil {
		return nil, err
	}
	dependencies := fragmentDependencies(fragments)
	groupOf := fragmentGroups(groups)
	accepted := stringSet(state.AcceptedFragments)
	materialized := stringSet(state.MaterializedFragments)
	statuses := make([]fragmentStatus, 0, len(fragments))
	for _, fragment := range fragments {
		id := fragment.ID()
		group := groupOf[id]
		fragmentState := fragmentStatus{
			Fragment:   fragment,
			ID:         id,
			ShortID:    fragment.ShortID(),
			Status:     "pending",
			Assignment: "unassigned",
			Assignable: true,
		}
		if group != "" {
			fragmentState.Assignment = "assigned"
			fragmentState.Group = group
		}
		seen := map[string]bool{}
		for _, dependency := range dependencies[id] {
			dependencyGroup := groupOf[dependency]
			if dependencyGroup == group && dependencyGroup != "" {
				continue
			}
			if dependencyGroup == "" {
				dependencyGroup = "unassigned:" + dependency[:12]
			}
			if !seen[dependencyGroup] {
				fragmentState.Dependencies = append(fragmentState.Dependencies, dependencyGroup)
				seen[dependencyGroup] = true
			}
		}
		switch {
		case materialized[id]:
			fragmentState.Status = "materialized"
		case !accepted[id]:
			fragmentState.Status = "pending"
		default:
			fragmentState.Status = "accepted-pending"
		}
		statuses = append(statuses, fragmentState)
	}
	return statuses, nil
}

func validateGroupGraph(ctx context.Context, repo repository.Repository, state session.State, groups []session.Group) error {
	fragments, err := allFragments(ctx, repo, state)
	if err != nil {
		return err
	}
	for _, group := range groups {
		for _, fragment := range group.Fragments {
			found := false
			for _, known := range fragments {
				if known.ID() == fragment.ID() {
					found = true
					break
				}
			}
			if !found {
				fragments = append(fragments, fragment)
			}
		}
	}
	dependencies := fragmentDependencies(fragments)
	groupOf := fragmentGroups(groups)
	edges := map[string]map[string]bool{}
	for current, prerequisites := range dependencies {
		currentGroup := groupOf[current]
		if currentGroup == "" {
			continue
		}
		for _, prerequisite := range prerequisites {
			prerequisiteGroup := groupOf[prerequisite]
			if prerequisiteGroup == "" || prerequisiteGroup == currentGroup {
				continue
			}
			if edges[prerequisiteGroup] == nil {
				edges[prerequisiteGroup] = map[string]bool{}
			}
			edges[prerequisiteGroup][currentGroup] = true
		}
	}
	if cycle := groupCycle(edges); len(cycle) > 0 {
		return fmt.Errorf("review group assignment creates dependency cycle: %s", strings.Join(cycle, " -> "))
	}
	return nil
}

func materializeAccepted(ctx context.Context, repo repository.Repository, state *session.State, store session.Store, fragments []session.Fragment) ([]string, error) {
	dependencies := fragmentDependencies(fragments)
	accepted := stringSet(state.AcceptedFragments)
	materialized := stringSet(state.MaterializedFragments)
	var applied []string
	for {
		progress := false
		for _, fragment := range fragments {
			id := fragment.ID()
			if !accepted[id] || materialized[id] || hasUnmaterializedDependency(id, dependencies, materialized) {
				continue
			}
			patch, err := repo.FragmentPatch(ctx, fragment)
			if err != nil {
				return applied, err
			}
			if _, err := repo.ApplyFragment(ctx, state.ReviewedRef, patch); err != nil {
				return applied, err
			}
			materialized[id] = true
			state.MaterializedFragments = append(state.MaterializedFragments, id)
			applied = append(applied, id)
			if err := store.Save(*state); err != nil {
				return applied, err
			}
			progress = true
		}
		if !progress {
			break
		}
	}
	return applied, nil
}

func allFragments(ctx context.Context, repo repository.Repository, state session.State) ([]session.Fragment, error) {
	fragments, err := repo.ReviewFragments(ctx, state.PinnedComparisonCommit, "HEAD")
	if err != nil {
		return nil, err
	}
	groups, err := session.NewStore(repo.GitDir).LoadGroups(state.WorkBranch)
	if err != nil {
		return nil, err
	}
	known := map[string]bool{}
	for _, fragment := range fragments {
		known[fragment.ID()] = true
	}
	for _, group := range groups {
		for _, fragment := range group.Fragments {
			if !known[fragment.ID()] {
				fragments = append(fragments, fragment)
				known[fragment.ID()] = true
			}
		}
	}
	return fragments, nil
}

func fragmentDependencies(fragments []session.Fragment) map[string][]string {
	dependencies := map[string][]string{}
	previous := map[string][]session.Fragment{}
	for _, current := range fragments {
		seen := map[string]bool{}
		for _, path := range current.Paths() {
			for _, prior := range previous[path] {
				if prior.Commit == current.Commit || seen[prior.ID()] {
					continue
				}
				dependencies[current.ID()] = append(dependencies[current.ID()], prior.ID())
				seen[prior.ID()] = true
			}
		}
		for _, path := range current.Paths() {
			previous[path] = append(previous[path], current)
		}
	}
	return dependencies
}

func fragmentGroups(groups []session.Group) map[string]string {
	groupOf := map[string]string{}
	for _, group := range groups {
		for _, fragment := range group.Fragments {
			groupOf[fragment.ID()] = group.Name
		}
	}
	return groupOf
}

func groupCycle(edges map[string]map[string]bool) []string {
	state := map[string]int{}
	stack := []string{}
	var visit func(string) []string
	visit = func(node string) []string {
		if state[node] == 1 {
			for index, value := range stack {
				if value == node {
					return append(append([]string{}, stack[index:]...), node)
				}
			}
			return []string{node, node}
		}
		if state[node] == 2 {
			return nil
		}
		state[node] = 1
		stack = append(stack, node)
		nexts := make([]string, 0, len(edges[node]))
		for next := range edges[node] {
			nexts = append(nexts, next)
		}
		sort.Strings(nexts)
		for _, next := range nexts {
			if cycle := visit(next); len(cycle) > 0 {
				return cycle
			}
		}
		stack = stack[:len(stack)-1]
		state[node] = 2
		return nil
	}
	nodes := make([]string, 0, len(edges))
	for node := range edges {
		nodes = append(nodes, node)
	}
	sort.Strings(nodes)
	for _, node := range nodes {
		if cycle := visit(node); len(cycle) > 0 {
			return cycle
		}
	}
	return nil
}

func hasUnmaterializedDependency(id string, dependencies map[string][]string, materialized map[string]bool) bool {
	for _, dependency := range dependencies[id] {
		if !materialized[dependency] {
			return true
		}
	}
	return false
}

func fragmentIDs(fragments []session.Fragment) []string {
	ids := make([]string, 0, len(fragments))
	for _, fragment := range fragments {
		ids = append(ids, fragment.ID())
	}
	return ids
}

func pendingFragmentIDs(state session.State) []string {
	materialized := stringSet(state.MaterializedFragments)
	var pending []string
	for _, id := range state.AcceptedFragments {
		if !materialized[id] {
			pending = append(pending, id)
		}
	}
	return pending
}

func countPending(accepted, materialized map[string]bool) int {
	count := 0
	for id := range accepted {
		if !materialized[id] {
			count++
		}
	}
	return count
}

func containsFragment(fragments []session.Fragment, candidate session.Fragment) bool {
	for _, fragment := range fragments {
		if fragment.ID() == candidate.ID() {
			return true
		}
	}
	return false
}

func stringSet(values []string) map[string]bool {
	set := make(map[string]bool, len(values))
	for _, value := range values {
		set[value] = true
	}
	return set
}

func appendUnique(values []string, additions ...string) []string {
	seen := stringSet(values)
	for _, addition := range additions {
		if !seen[addition] {
			values = append(values, addition)
			seen[addition] = true
		}
	}
	return values
}

func removeString(values []string, target string) []string {
	result := values[:0]
	for _, value := range values {
		if value != target {
			result = append(result, value)
		}
	}
	return result
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
		return repository.Repository{}, session.State{}, fmt.Errorf("no review session exists for %q: %w", workBranch, session.ErrNotFound)
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

func helpRequested(args []string) bool {
	for _, arg := range args {
		if arg == "help" || arg == "--help" || arg == "-h" {
			return true
		}
	}
	return false
}

func printUsage(value string) error {
	_, err := fmt.Fprintln(os.Stderr, strings.TrimSpace(value))
	return err
}

func usage() error {
	return printUsage(`Usage:
  git-review --version
  git-review -v
  git-review init [--base <ref>]
  git-review sync-base
  git-review status [--json]
  git-review accept --path <path> [--path <path>...]
  git-review undo
  git-review history
  git-review reset --commit <commit>
  git-review changes --json
  git-review fragment inspect --commit <commit> [--path <path>] [--json]
  git-review fragment list --json [--unassigned]
  git-review group list --json
  git-review group add --name <name> --commit <commit> [--path <path>]
  git-review group assign --name <name> --fragment <id> [--fragment <id>...]
  git-review group assign --name <name> --commit <commit> [--path <path> --old-start <n> --old-lines <n> --new-start <n> --new-lines <n>]
  git-review group view --name <name>
  git-review group accept --name <name> --path <path>`)
}

func fragmentUsage() error {
	return printUsage(`Usage:
  git-review fragment inspect --commit <commit> [--path <path>] [--json]
  git-review fragment list --json [--unassigned]`)
}

func fragmentInspectUsage() error {
	return printUsage(`Usage:
  git-review fragment inspect --commit <commit> [--path <path>] [--json]

Inspect the source fragments produced by a commit.`)
}

func fragmentListUsage() error {
	return printUsage(`Usage:
  git-review fragment list --json [--unassigned]

List review fragments and their assignment status.`)
}

func groupUsage() error {
	return printUsage(`Usage:
  git-review group list --json
  git-review group add --name <name> --commit <commit> [--path <path>]
  git-review group assign --name <name> --fragment <id> [--fragment <id>...]
  git-review group assign --name <name> --commit <commit> [--path <path> --old-start <n> --old-lines <n> --new-start <n> --new-lines <n>]
  git-review group view --name <name>
  git-review group accept --name <name> --path <path>`)
}

func groupListUsage() error {
	return printUsage(`Usage:
  git-review group list --json

List review groups and fragment statuses.`)
}

func groupAddUsage() error {
	return printUsage(`Usage:
  git-review group add --name <name> --commit <commit> [--path <path>]

Assign all matching fragments from a commit.`)
}

func groupAssignUsage() error {
	return printUsage(`Usage:
  git-review group assign --name <name> --fragment <id> [--fragment <id>...]
  git-review group assign --name <name> --commit <commit> [--path <path> --old-start <n> --old-lines <n> --new-start <n> --new-lines <n>]

Assign selected source fragments to a review group.`)
}

func groupViewUsage() error {
	return printUsage(`Usage:
  git-review group view --name <name>

Show a group's source fragments and patches.`)
}

func groupAcceptUsage() error {
	return printUsage(`Usage:
  git-review group accept --name <name> --path <path>

Accept a group's fragments for a path.`)
}
