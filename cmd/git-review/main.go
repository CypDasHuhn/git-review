package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	pathpkg "path"
	"sort"
	"strings"

	"github.com/CypDasHuhn/git-review/internal/repository"
	"github.com/CypDasHuhn/git-review/internal/session"
)

const version = "0.4.10"

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
	if err := repo.InstallPostMergeHook(ctx); err != nil {
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
	if err := repo.InstallPostMergeHook(ctx); err != nil {
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
	if helpRequested(args) {
		return statusUsage()
	}
	flags := flag.NewFlagSet("status", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	asJSON := flags.Bool("json", false, "write structured JSON")
	noInit := flags.Bool("no-init", false, "inspect without creating or updating a review session")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("status does not accept positional arguments")
	}
	repo, workBranch, state, err := loadCurrentSession(ctx)
	if err != nil {
		if *noInit && errors.Is(err, session.ErrNotFound) {
			return json.NewEncoder(os.Stdout).Encode(map[string]any{
				"sessionExists": false,
				"workBranch":    workBranch,
			})
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
	if *noInit {
		return json.NewEncoder(os.Stdout).Encode(struct {
			SessionExists bool `json:"sessionExists"`
			session.Status
		}{SessionExists: true, Status: status})
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
	selected := map[string]bool{}
	for _, fragment := range fragments {
		for _, path := range paths {
			if fragment.TouchesPath(path) {
				selected[fragment.ID()] = true
			}
		}
	}
	includeFragmentDependencies(fragments, selected)
	var newlyAccepted []string
	for _, fragment := range fragments {
		if selected[fragment.ID()] && !accepted[fragment.ID()] {
			accepted[fragment.ID()] = true
			newlyAccepted = append(newlyAccepted, fragment.ID())
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
	pending := pendingFragmentIDs(state)
	return json.NewEncoder(os.Stdout).Encode(map[string]any{
		"accepted":     nonNilStrings(state.AcceptedFragments),
		"materialized": nonNilStrings(materialized),
		"pending":      nonNilStrings(pending),
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
	summary := flags.Bool("summary", false, "show compact review statistics")
	group := flags.String("group", "", "limit summary statistics to a review group")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || (!*asJSON && !*summary) {
		return errors.New("fragment list requires --json or --summary")
	}
	if !*summary && *group != "" {
		return errors.New("fragment list --group requires --summary")
	}
	if *summary && *unassigned && *group != "" {
		return errors.New("fragment list cannot combine --unassigned and --group")
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
	if *summary {
		value, err := summaryForGroup(statuses, groups, *group)
		if err != nil {
			return err
		}
		if *asJSON {
			return json.NewEncoder(os.Stdout).Encode(value)
		}
		printReviewSummary(value)
		return nil
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
	case "project":
		return projectGroup(ctx, args[1:])
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
	var paths stringList
	flags.Var(&paths, "path", "changed file path")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *name == "" || len(paths) == 0 {
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
			for _, path := range paths {
				if fragment.TouchesPath(path) {
					selected = append(selected, fragment)
					break
				}
			}
		}
		if len(selected) == 0 {
			return fmt.Errorf("review group %q has no fragments for %q", *name, strings.Join(paths, ", "))
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
	pending := pendingFragmentIDs(state)
	return json.NewEncoder(os.Stdout).Encode(map[string]any{
		"group":        *name,
		"accepted":     nonNilStrings(fragmentIDs(selected)),
		"materialized": nonNilStrings(materialized),
		"pending":      nonNilStrings(pending),
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

func projectGroup(ctx context.Context, args []string) error {
	if helpRequested(args) {
		return groupProjectUsage()
	}
	flags := flag.NewFlagSet("group project", flag.ContinueOnError)
	name := flags.String("name", "", "review group name")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *name == "" {
		return errors.New("group project requires --name")
	}
	repo, state, err := currentSession(ctx)
	if err != nil {
		return err
	}
	groups, err := session.NewStore(repo.GitDir).LoadGroups(state.WorkBranch)
	if err != nil {
		return err
	}
	groupIndex := groupIndex(groups, *name)
	if groupIndex < 0 {
		return fmt.Errorf("review group %q does not exist", *name)
	}
	fragments, err := allFragments(ctx, repo, state)
	if err != nil {
		return err
	}
	dependencies := fragmentDependencies(fragments)
	selected := map[string]bool{}
	for _, fragment := range groups[groupIndex].Fragments {
		selected[fragment.ID()] = true
	}
	materialized := stringSet(state.MaterializedFragments)
	projectionRef := session.ProjectionRef(state.WorkBranch, *name)
	if err := repo.CreateRef(ctx, projectionRef, state.ReviewedRef); err != nil {
		return err
	}
	applied := map[string]bool{}
	appliedIDs := make([]string, 0)
	for {
		progress := false
		for _, fragment := range fragments {
			id := fragment.ID()
			if !selected[id] || materialized[id] || applied[id] {
				continue
			}
			ready := true
			for _, dependency := range dependencies[id] {
				if !materialized[dependency] && !applied[dependency] {
					ready = false
					break
				}
			}
			if !ready {
				continue
			}
			patch, err := repo.FragmentPatch(ctx, fragment)
			if err != nil {
				return err
			}
			if _, err := repo.ApplyFragment(ctx, projectionRef, patch); err != nil {
				return fmt.Errorf("project fragment %s: %w", fragment.Label(), err)
			}
			applied[id] = true
			appliedIDs = append(appliedIDs, id)
			progress = true
		}
		if !progress {
			break
		}
	}
	blockedIDs := make([]string, 0)
	for _, fragment := range fragments {
		id := fragment.ID()
		if selected[id] && !materialized[id] && !applied[id] {
			blockedIDs = append(blockedIDs, id)
		}
	}
	commit, err := repo.ResolveCommit(ctx, projectionRef)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{
		"name":        *name,
		"ref":         projectionRef,
		"commit":      commit,
		"reviewedRef": state.ReviewedRef,
		"fragments":   appliedIDs,
		"blocked":     blockedIDs,
		"complete":    len(blockedIDs) == 0 && len(appliedIDs) == 0,
	})
}

func assignGroup(ctx context.Context, args []string) error {
	if helpRequested(args) {
		return groupAssignUsage()
	}
	flags := flag.NewFlagSet("group assign", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	name := flags.String("name", "", "review group name")
	var commits stringList
	flags.Var(&commits, "commit", "commit hash")
	var fragmentIDs stringList
	flags.Var(&fragmentIDs, "fragment", "fragment ID")
	var paths stringList
	flags.Var(&paths, "path", "changed file path")
	var pathGlobs stringList
	flags.Var(&pathGlobs, "path-glob", "changed file path glob")
	oldStart := flags.Int("old-start", -1, "source hunk old start")
	oldLines := flags.Int("old-lines", -1, "source hunk old line count")
	newStart := flags.Int("new-start", -1, "source hunk new start")
	newLines := flags.Int("new-lines", -1, "source hunk new line count")
	reassign := flags.Bool("reassign", false, "move matching fragments from another group")
	dryRun := flags.Bool("dry-run", false, "report selector matches without changing review state")
	asJSON := flags.Bool("json", false, "write structured JSON")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *name == "" {
		return errors.New("group assign requires --name and a selector")
	}
	if len(fragmentIDs) > 0 {
		if len(commits) > 0 || len(paths) > 0 || len(pathGlobs) > 0 || *oldStart >= 0 || *oldLines >= 0 || *newStart >= 0 || *newLines >= 0 || *reassign || *dryRun {
			return errors.New("group assign with --fragment requires --name and --fragment only")
		}
		return assignFragmentIDs(ctx, *name, fragmentIDs)
	}
	specific := *oldStart >= 0 || *oldLines >= 0 || *newStart >= 0 || *newLines >= 0
	if specific {
		if len(commits) != 1 || len(paths) != 1 || len(pathGlobs) > 0 || *reassign || *dryRun {
			return errors.New("fragment selection requires one --commit, one --path, and all source range flags")
		}
		return assignFragments(ctx, *name, commits[0], paths[0], *oldStart, *oldLines, *newStart, *newLines)
	}
	if len(commits) == 0 && len(paths) == 0 && len(pathGlobs) == 0 {
		return errors.New("group assign requires at least one selector")
	}
	return assignSelectedFragments(ctx, *name, commits, paths, pathGlobs, *reassign, *dryRun, *asJSON)
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
	reviewFragments, err := repo.ReviewFragments(ctx, state.PinnedComparisonCommit, "HEAD")
	if err != nil {
		return fmt.Errorf("find current review fragments: %w", err)
	}
	specific := oldStart >= 0 || oldLines >= 0 || newStart >= 0 || newLines >= 0
	if specific && (path == "" || oldStart < 0 || oldLines < 0 || newStart < 0 || newLines < 0) {
		return errors.New("fragment selection requires --path and all source range flags")
	}
	var selected []session.Fragment
	for _, fragment := range reviewFragments {
		if fragment.Commit != commit.Hash {
			continue
		}
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

type assignmentReport struct {
	Group           string   `json:"group"`
	Commit          string   `json:"commit,omitempty"`
	ShortCommit     string   `json:"shortCommit,omitempty"`
	Fragments       []string `json:"fragments,omitempty"`
	DryRun          bool     `json:"dryRun"`
	Matched         int      `json:"matched"`
	Assigned        int      `json:"assigned"`
	AlreadyAssigned int      `json:"alreadyAssigned"`
	Skipped         int      `json:"skipped"`
	Unmatched       int      `json:"unmatched"`
}

func assignSelectedFragments(ctx context.Context, name string, commitRefs, paths, pathGlobs []string, reassign, dryRun, asJSON bool) error {
	repo, state, err := currentSession(ctx)
	if err != nil {
		return err
	}
	normalizedPaths := make([]string, 0, len(paths))
	for _, value := range paths {
		value = strings.TrimPrefix(value, "./")
		if value == "" || strings.HasPrefix(value, "/") {
			return fmt.Errorf("path selector %q must be repository-relative", value)
		}
		normalizedPaths = append(normalizedPaths, value)
	}
	paths = normalizedPaths
	selectorCommits := map[string]bool{}
	var selectedCommit string
	var selectedShortCommit string
	for _, ref := range commitRefs {
		commit, err := repo.CommitInfo(ctx, ref)
		if err != nil {
			return fmt.Errorf("resolve commit %q: %w", ref, err)
		}
		selectorCommits[commit.Hash] = true
		if len(commitRefs) == 1 {
			selectedCommit = commit.Hash
			selectedShortCommit = commit.ShortHash
		}
	}
	for _, pattern := range pathGlobs {
		if err := validatePathGlob(pattern); err != nil {
			return fmt.Errorf("invalid path glob %q: %w", pattern, err)
		}
	}
	reviewFragments, err := repo.ReviewFragments(ctx, state.PinnedComparisonCommit, "HEAD")
	if err != nil {
		return fmt.Errorf("find current review fragments: %w", err)
	}
	groups, err := session.NewStore(repo.GitDir).LoadGroups(state.WorkBranch)
	if err != nil {
		return err
	}
	statuses, err := fragmentStatuses(ctx, repo, state, groups)
	if err != nil {
		return err
	}
	statusByID := make(map[string]fragmentStatus, len(statuses))
	for _, status := range statuses {
		statusByID[status.ID] = status
	}
	var matched []session.Fragment
	var selected []session.Fragment
	alreadyAssigned := 0
	skipped := 0
	for _, fragment := range reviewFragments {
		if !matchesFragmentSelector(fragment, selectorCommits, paths, pathGlobs) {
			continue
		}
		matched = append(matched, fragment)
		status, found := statusByID[fragment.ID()]
		if !found || !status.Assignable {
			skipped++
			continue
		}
		if status.Group != "" {
			if status.Group == name || !reassign {
				alreadyAssigned++
				continue
			}
		}
		selected = append(selected, fragment)
	}
	report := assignmentReport{
		Group:           name,
		Commit:          selectedCommit,
		ShortCommit:     selectedShortCommit,
		DryRun:          dryRun,
		Matched:         len(matched),
		AlreadyAssigned: alreadyAssigned,
		Skipped:         skipped,
		Fragments:       fragmentIDs(selected),
	}
	if report.Matched == 0 {
		report.Unmatched = 1
	}
	report.Assigned = len(selected)
	if len(selected) > 0 {
		prospective := cloneGroups(groups)
		groupIndex := groupIndex(prospective, name)
		if groupIndex < 0 {
			prospective = append(prospective, session.Group{Name: name})
			groupIndex = len(prospective) - 1
		}
		if reassign {
			for index := range prospective {
				if index == groupIndex {
					continue
				}
				prospective[index].Fragments = removeFragments(prospective[index].Fragments, selected)
			}
		}
		for _, fragment := range selected {
			if !containsFragment(prospective[groupIndex].Fragments, fragment) {
				prospective[groupIndex].Fragments = append(prospective[groupIndex].Fragments, fragment)
			}
		}
		if err := validateGroupGraph(ctx, repo, state, prospective); err != nil {
			return err
		}
		if !dryRun {
			if err := session.NewStore(repo.GitDir).SaveGroups(state.WorkBranch, prospective); err != nil {
				return err
			}
		}
	}
	if !dryRun || asJSON {
		return json.NewEncoder(os.Stdout).Encode(report)
	}
	fmt.Printf("Matched: %d fragments\n", report.Matched)
	fmt.Printf("Assigned: %d\n", report.Assigned)
	fmt.Printf("Already assigned: %d\n", report.AlreadyAssigned)
	fmt.Printf("Skipped: %d\n", report.Skipped)
	fmt.Printf("Unmatched selectors: %d\n", report.Unmatched)
	if dryRun {
		fmt.Printf("Would assign to: %s\n", name)
	} else {
		fmt.Printf("Assigned to: %s\n", name)
	}
	return nil
}

func matchesFragmentSelector(fragment session.Fragment, commits map[string]bool, paths, pathGlobs []string) bool {
	if len(commits) > 0 && !commits[fragment.Commit] {
		return false
	}
	if len(paths) == 0 && len(pathGlobs) == 0 {
		return true
	}
	for _, path := range paths {
		if fragment.TouchesPath(path) {
			return true
		}
	}
	for _, pattern := range pathGlobs {
		for _, path := range fragment.Paths() {
			matched, err := matchPathGlob(pattern, path)
			if err == nil && matched {
				return true
			}
		}
	}
	return false
}

func validatePathGlob(pattern string) error {
	pattern = strings.TrimPrefix(pattern, "./")
	if strings.HasPrefix(pattern, "/") || pattern == "" {
		return errors.New("path glob must be a repository-relative path")
	}
	for _, part := range strings.Split(strings.Trim(pattern, "/"), "/") {
		if part == "**" {
			continue
		}
		if _, err := pathpkg.Match(part, ""); err != nil {
			return err
		}
	}
	return nil
}

func matchPathGlob(pattern, candidate string) (bool, error) {
	pattern = strings.TrimPrefix(pattern, "./")
	candidate = strings.TrimPrefix(candidate, "./")
	pattern = strings.Trim(pattern, "/")
	candidate = strings.Trim(candidate, "/")
	if pattern == "" {
		return false, errors.New("path glob cannot be empty")
	}
	patternParts := strings.Split(pattern, "/")
	candidateParts := strings.Split(candidate, "/")
	type state struct {
		patternIndex   int
		candidateIndex int
	}
	memo := map[state]bool{}
	seen := map[state]bool{}
	var match func(int, int) (bool, error)
	match = func(patternIndex, candidateIndex int) (bool, error) {
		current := state{patternIndex: patternIndex, candidateIndex: candidateIndex}
		if seen[current] {
			return memo[current], nil
		}
		seen[current] = true
		if patternIndex == len(patternParts) {
			memo[current] = candidateIndex == len(candidateParts)
			return memo[current], nil
		}
		if patternParts[patternIndex] == "**" {
			matched, err := match(patternIndex+1, candidateIndex)
			if err != nil {
				return false, err
			}
			if matched {
				memo[current] = true
				return true, nil
			}
			if candidateIndex < len(candidateParts) {
				matched, err = match(patternIndex, candidateIndex+1)
				if err != nil {
					return false, err
				}
				memo[current] = matched
				return matched, nil
			}
			return false, nil
		}
		if candidateIndex == len(candidateParts) {
			return false, nil
		}
		matched, err := pathpkg.Match(patternParts[patternIndex], candidateParts[candidateIndex])
		if err != nil {
			return false, err
		}
		if !matched {
			return false, nil
		}
		matched, err = match(patternIndex+1, candidateIndex+1)
		memo[current] = matched
		return matched, err
	}
	return match(0, 0)
}

func cloneGroups(groups []session.Group) []session.Group {
	cloned := make([]session.Group, len(groups))
	for index, group := range groups {
		cloned[index] = session.Group{Name: group.Name, Fragments: append([]session.Fragment{}, group.Fragments...)}
	}
	return cloned
}

func groupIndex(groups []session.Group, name string) int {
	for index, group := range groups {
		if group.Name == name {
			return index
		}
	}
	return -1
}

func removeFragments(fragments []session.Fragment, selected []session.Fragment) []session.Fragment {
	selectedIDs := stringSet(fragmentIDs(selected))
	remaining := make([]session.Fragment, 0, len(fragments))
	for _, fragment := range fragments {
		if !selectedIDs[fragment.ID()] {
			remaining = append(remaining, fragment)
		}
	}
	return remaining
}

func listGroups(ctx context.Context, args []string) error {
	if helpRequested(args) {
		return groupListUsage()
	}
	flags := flag.NewFlagSet("group list", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	asJSON := flags.Bool("json", false, "write structured JSON")
	summary := flags.Bool("summary", false, "show compact review statistics")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || (!*asJSON && !*summary) {
		return errors.New("group list requires --json or --summary")
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
	if *summary {
		allStatuses, err := fragmentStatuses(ctx, repo, state, groups)
		if err != nil {
			return err
		}
		value := summarizeFragments(allStatuses, groups, "")
		if *asJSON {
			return json.NewEncoder(os.Stdout).Encode(value)
		}
		printReviewSummary(value)
		return nil
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

type reviewSummary struct {
	Total      int            `json:"total"`
	Assigned   int            `json:"assigned"`
	Unassigned int            `json:"unassigned"`
	Statuses   map[string]int `json:"statuses"`
	Groups     []groupSummary `json:"groups"`
}

type groupSummary struct {
	Name      string         `json:"name"`
	Fragments int            `json:"fragments"`
	Commits   int            `json:"commits"`
	Paths     int            `json:"paths"`
	Statuses  map[string]int `json:"statuses"`
}

func summaryForGroup(statuses []fragmentStatus, groups []session.Group, name string) (reviewSummary, error) {
	if name != "" && groupIndex(groups, name) < 0 {
		return reviewSummary{}, fmt.Errorf("review group %q does not exist", name)
	}
	return summarizeFragments(statuses, groups, name), nil
}

func summarizeFragments(statuses []fragmentStatus, groups []session.Group, groupFilter string) reviewSummary {
	filtered := statuses
	if groupFilter != "" {
		filtered = make([]fragmentStatus, 0)
		for _, status := range statuses {
			if status.Group == groupFilter {
				filtered = append(filtered, status)
			}
		}
	}
	summary := reviewSummary{
		Total:    len(filtered),
		Statuses: map[string]int{},
		Groups:   make([]groupSummary, 0, len(groups)),
	}
	for _, status := range filtered {
		if status.Assignment == "assigned" {
			summary.Assigned++
		} else {
			summary.Unassigned++
		}
		summary.Statuses[status.Status]++
	}
	for _, group := range groups {
		if groupFilter != "" && group.Name != groupFilter {
			continue
		}
		groupSummary := groupSummary{Name: group.Name, Statuses: map[string]int{}}
		commits := map[string]bool{}
		paths := map[string]bool{}
		for _, status := range filtered {
			if status.Group != group.Name {
				continue
			}
			groupSummary.Fragments++
			commits[status.Commit] = true
			for _, path := range status.Paths() {
				paths[path] = true
			}
			groupSummary.Statuses[status.Status]++
		}
		groupSummary.Commits = len(commits)
		groupSummary.Paths = len(paths)
		summary.Groups = append(summary.Groups, groupSummary)
	}
	return summary
}

func printReviewSummary(summary reviewSummary) {
	fmt.Printf("Total fragments: %d\n", summary.Total)
	fmt.Printf("Assigned: %d\n", summary.Assigned)
	fmt.Printf("Unassigned: %d\n", summary.Unassigned)
	fmt.Println("Statuses:")
	printCounts(summary.Statuses)
	for _, group := range summary.Groups {
		fmt.Printf("%s: %d fragments, %d commits, %d paths\n", group.Name, group.Fragments, group.Commits, group.Paths)
		printCounts(group.Statuses)
	}
}

func printCounts(counts map[string]int) {
	keys := make([]string, 0, len(counts))
	for key := range counts {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fmt.Printf("  %s: %d\n", key, counts[key])
	}
}

func formatFragmentPatch(patch repository.FragmentPatch) string {
	if patch.Fragment.OldPath != "" && patch.Fragment.OldPath != patch.Fragment.Path && len(patch.OldLines) == 0 && len(patch.NewLines) == 0 {
		return fmt.Sprintf("rename %s -> %s\n", patch.Fragment.OldPath, patch.Fragment.Path)
	}
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

func includeFragmentDependencies(fragments []session.Fragment, selected map[string]bool) {
	dependencies := fragmentDependencies(fragments)
	for {
		changed := false
		for _, fragment := range fragments {
			if !selected[fragment.ID()] {
				continue
			}
			for _, dependency := range dependencies[fragment.ID()] {
				if !selected[dependency] {
					selected[dependency] = true
					changed = true
				}
			}
		}
		if !changed {
			return
		}
	}
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

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
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
	repo, _, state, err := loadCurrentSession(ctx)
	return repo, state, err
}

func loadCurrentSession(ctx context.Context) (repository.Repository, string, session.State, error) {
	repo, err := repository.Open(ctx, "")
	if err != nil {
		return repository.Repository{}, "", session.State{}, err
	}
	workBranch, err := repo.CurrentBranch(ctx)
	if err != nil {
		return repository.Repository{}, "", session.State{}, err
	}
	state, err := session.NewStore(repo.GitDir).Load(workBranch)
	if errors.Is(err, session.ErrNotFound) {
		return repo, workBranch, session.State{}, fmt.Errorf("no review session exists for %q: %w", workBranch, session.ErrNotFound)
	}
	return repo, workBranch, state, err
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
  git-review status [--json] [--no-init]
  git-review accept --path <path> [--path <path>...]
  git-review undo
  git-review history
  git-review reset --commit <commit>
  git-review changes --json
  git-review fragment inspect --commit <commit> [--path <path>] [--json]
  git-review fragment list --json [--unassigned]
  git-review fragment list --summary [--json] [--group <name>]
  git-review group list --json
  git-review group list --summary [--json]
  git-review group add --name <name> --commit <commit> [--path <path>]
  git-review group assign --name <name> --fragment <id> [--fragment <id>...]
  git-review group assign --name <name> [--commit <commit>...] [--path <path>...] [--path-glob <glob>...] [--reassign] [--dry-run] [--json]
  git-review group assign --name <name> --commit <commit> --path <path> --old-start <n> --old-lines <n> --new-start <n> --new-lines <n>
  git-review group view --name <name>
  git-review group project --name <name>
  git-review group accept --name <name> --path <path> [--path <path>...]`)
}

func statusUsage() error {
	return printUsage(`Usage:
  git-review status [--json] [--no-init]

Show review state. --no-init returns structured output and never creates or updates a review session.`)
}

func fragmentUsage() error {
	return printUsage(`Usage:
  git-review fragment inspect --commit <commit> [--path <path>] [--json]
  git-review fragment list --json [--unassigned]
  git-review fragment list --summary [--json] [--group <name>]`)
}

func fragmentInspectUsage() error {
	return printUsage(`Usage:
  git-review fragment inspect --commit <commit> [--path <path>] [--json]

Inspect the source fragments produced by a commit.`)
}

func fragmentListUsage() error {
	return printUsage(`Usage:
  git-review fragment list --json [--unassigned]
  git-review fragment list --summary [--json] [--group <name>]

List review fragments and their assignment status.`)
}

func groupUsage() error {
	return printUsage(`Usage:
  git-review group list --json
  git-review group list --summary [--json]
  git-review group add --name <name> --commit <commit> [--path <path>]
  git-review group assign --name <name> --fragment <id> [--fragment <id>...]
  git-review group assign --name <name> [--commit <commit>...] [--path <path>...] [--path-glob <glob>...] [--reassign] [--dry-run] [--json]
  git-review group assign --name <name> --commit <commit> --path <path> --old-start <n> --old-lines <n> --new-start <n> --new-lines <n>
  git-review group view --name <name>
  git-review group project --name <name>
  git-review group accept --name <name> --path <path> [--path <path>...]`)
}

func groupListUsage() error {
	return printUsage(`Usage:
  git-review group list --json
  git-review group list --summary [--json]

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
  git-review group assign --name <name> [--commit <commit>...] [--path <path>...] [--path-glob <glob>...] [--reassign] [--dry-run] [--json]
  git-review group assign --name <name> --commit <commit> --path <path> --old-start <n> --old-lines <n> --new-start <n> --new-lines <n>

Assign selected source fragments to a review group. Selector mode uses only unassigned fragments unless --reassign is supplied; --dry-run reports matches without changing review state.`)
}

func groupViewUsage() error {
	return printUsage(`Usage:
  git-review group view --name <name>

Show a group's source fragments and patches.`)
}

func groupProjectUsage() error {
	return printUsage(`Usage:
  git-review group project --name <name>

Create a display-only projection ref for a group's displayable fragments.`)
}

func groupAcceptUsage() error {
	return printUsage(`Usage:
	git-review group accept --name <name> --path <path> [--path <path>...]

Accept a group's fragments for one or more paths.`)
}
