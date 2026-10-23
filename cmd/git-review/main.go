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

func usage() error {
	_, err := fmt.Fprint(os.Stderr, strings.TrimSpace(`Usage:
  git-review init [--base <ref>]
  git-review status [--json]
`)+"\n")
	return err
}
