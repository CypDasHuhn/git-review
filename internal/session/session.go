package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

var ErrNotFound = errors.New("review session not found")

type State struct {
	WorkBranch             string `json:"workBranch"`
	ComparisonRef          string `json:"comparisonRef"`
	PinnedComparisonCommit string `json:"pinnedComparisonCommit"`
	ReviewedRef            string `json:"reviewedRef"`
	StartedFromWorkCommit  string `json:"startedFromWorkCommit"`
}

type Status struct {
	State
	WorkCommit              string `json:"workCommit"`
	ReviewedCommit          string `json:"reviewedCommit"`
	CurrentComparisonCommit string `json:"currentComparisonCommit"`
	ComparisonBaseAdvanced  bool   `json:"comparisonBaseAdvanced"`
}

type Store struct {
	directory string
}

func NewStore(gitDir string) Store {
	return Store{directory: filepath.Join(gitDir, "git-review", "sessions")}
}

func (s Store) Load(workBranch string) (State, error) {
	contents, err := os.ReadFile(s.path(workBranch))
	if errors.Is(err, os.ErrNotExist) {
		return State{}, ErrNotFound
	}
	if err != nil {
		return State{}, fmt.Errorf("read review session: %w", err)
	}
	var state State
	if err := json.Unmarshal(contents, &state); err != nil {
		return State{}, fmt.Errorf("parse review session: %w", err)
	}
	return state, nil
}

func (s Store) Save(state State) error {
	if err := os.MkdirAll(s.directory, 0o755); err != nil {
		return fmt.Errorf("create review session directory: %w", err)
	}
	contents, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("encode review session: %w", err)
	}
	if err := os.WriteFile(s.path(state.WorkBranch), append(contents, '\n'), 0o644); err != nil {
		return fmt.Errorf("write review session: %w", err)
	}
	return nil
}

func ReviewedRef(workBranch string) string {
	return "refs/git-review/" + strings.ReplaceAll(workBranch, " ", "-") + "/reviewed"
}

func (s Store) path(workBranch string) string {
	hash := sha256.Sum256([]byte(workBranch))
	return filepath.Join(s.directory, hex.EncodeToString(hash[:])+".json")
}
