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

type Hunk struct {
	Path     string `json:"path"`
	OldPath  string `json:"oldPath,omitempty"`
	OldStart int    `json:"oldStart"`
	OldLines int    `json:"oldLines"`
	NewStart int    `json:"newStart"`
	NewLines int    `json:"newLines"`
}

type Group struct {
	Name  string `json:"name"`
	Hunks []Hunk `json:"hunks"`
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

func (s Store) LoadGroups(workBranch string) ([]Group, error) {
	contents, err := os.ReadFile(s.groupsPath(workBranch))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read review groups: %w", err)
	}
	var groups []Group
	if err := json.Unmarshal(contents, &groups); err != nil {
		return nil, fmt.Errorf("parse review groups: %w", err)
	}
	return groups, nil
}

func (s Store) SaveGroups(workBranch string, groups []Group) error {
	if err := os.MkdirAll(filepath.Join(s.directory, "groups"), 0o755); err != nil {
		return fmt.Errorf("create review group directory: %w", err)
	}
	contents, err := json.MarshalIndent(groups, "", "  ")
	if err != nil {
		return fmt.Errorf("encode review groups: %w", err)
	}
	if err := os.WriteFile(s.groupsPath(workBranch), append(contents, '\n'), 0o644); err != nil {
		return fmt.Errorf("write review groups: %w", err)
	}
	return nil
}

func ReviewedRef(workBranch string) string {
	return "refs/git-review/" + strings.ReplaceAll(workBranch, " ", "-") + "/reviewed"
}

func GroupRef(workBranch, name string) string {
	hash := sha256.Sum256([]byte(name))
	return "refs/git-review/" + strings.ReplaceAll(workBranch, " ", "-") + "/groups/" + hex.EncodeToString(hash[:8])
}

func (s Store) path(workBranch string) string {
	hash := sha256.Sum256([]byte(workBranch))
	return filepath.Join(s.directory, hex.EncodeToString(hash[:])+".json")
}

func (s Store) groupsPath(workBranch string) string {
	hash := sha256.Sum256([]byte(workBranch))
	return filepath.Join(s.directory, "groups", hex.EncodeToString(hash[:])+".json")
}
