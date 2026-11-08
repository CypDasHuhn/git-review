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
	WorkBranch             string   `json:"workBranch"`
	ComparisonRef          string   `json:"comparisonRef"`
	PinnedComparisonCommit string   `json:"pinnedComparisonCommit"`
	InitialReviewedCommit  string   `json:"initialReviewedCommit"`
	ReviewedRef            string   `json:"reviewedRef"`
	StartedFromWorkCommit  string   `json:"startedFromWorkCommit"`
	AcceptedFragments      []string `json:"acceptedFragments,omitempty"`
	MaterializedFragments  []string `json:"materializedFragments,omitempty"`
}

type Status struct {
	State
	WorkCommit              string `json:"workCommit"`
	ReviewedCommit          string `json:"reviewedCommit"`
	CurrentComparisonCommit string `json:"currentComparisonCommit"`
	ComparisonBaseAdvanced  bool   `json:"comparisonBaseAdvanced"`
	ChangeCount             int    `json:"changeCount"`
	AssignedCount           int    `json:"assignedCount"`
	UnassignedCount         int    `json:"unassignedCount"`
	AcceptedCount           int    `json:"acceptedCount"`
	MaterializedCount       int    `json:"materializedCount"`
	PendingCount            int    `json:"pendingCount"`
}

type Store struct {
	directory string
}

type Commit struct {
	Hash      string `json:"hash"`
	ShortHash string `json:"shortHash"`
}

type Fragment struct {
	Commit      string `json:"commit"`
	ShortCommit string `json:"shortCommit"`
	Parent      string `json:"parent"`
	Path        string `json:"path"`
	OldPath     string `json:"oldPath,omitempty"`
	OldStart    int    `json:"oldStart"`
	OldLines    int    `json:"oldLines"`
	NewStart    int    `json:"newStart"`
	NewLines    int    `json:"newLines"`
}

type Group struct {
	Name      string     `json:"name"`
	Fragments []Fragment `json:"fragments"`
}

func (f Fragment) ID() string {
	value := strings.Join([]string{
		f.Commit,
		f.Parent,
		f.Path,
		f.OldPath,
		fmt.Sprint(f.OldStart),
		fmt.Sprint(f.OldLines),
		fmt.Sprint(f.NewStart),
		fmt.Sprint(f.NewLines),
	}, "\x00")
	hash := sha256.Sum256([]byte(value))
	return hex.EncodeToString(hash[:])
}

func (f Fragment) ShortID() string {
	id := f.ID()
	if len(id) <= 12 {
		return id
	}
	return id[:12]
}

func (f Fragment) Label() string {
	short := f.ShortCommit
	if short == "" && len(f.Commit) >= 12 {
		short = f.Commit[:12]
	}
	return fmt.Sprintf("%s:%s@-%d,%d+%d,%d", short, f.Path, f.OldStart, f.OldLines, f.NewStart, f.NewLines)
}

func (f Fragment) TouchesPath(path string) bool {
	path = strings.TrimSuffix(path, "/...")
	path = strings.TrimSuffix(path, "/")
	return f.Path == path || f.OldPath == path || strings.HasPrefix(f.Path, path+"/") || strings.HasPrefix(f.OldPath, path+"/")
}

func (f Fragment) Paths() []string {
	paths := []string{f.Path}
	if f.OldPath != "" && f.OldPath != f.Path {
		paths = append(paths, f.OldPath)
	}
	return paths
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
