package repository

import "testing"

func TestParseFragmentPatchesIncludesRenameOnlyChanges(t *testing.T) {
	output := "diff --git a/Brandbook/tokens.json b/planning/brandbook/tokens.json\n" +
		"similarity index 100%\n" +
		"rename from Brandbook/tokens.json\n" +
		"rename to planning/brandbook/tokens.json\n"
	patches := parseFragmentPatches(output, "commit", "parent", "short")
	if len(patches) != 1 {
		t.Fatalf("patches = %#v, want one rename", patches)
	}
	fragment := patches[0].Fragment
	if fragment.Path != "planning/brandbook/tokens.json" || fragment.OldPath != "Brandbook/tokens.json" || fragment.OldLines != 0 || fragment.NewLines != 0 {
		t.Fatalf("fragment = %#v, want rename-only fragment", fragment)
	}
}

func TestLocateEditFollowsShiftedSourceLines(t *testing.T) {
	source := []string{"one", "two", "three", "four"}
	current := []string{"zero", "one", "two", "three", "four"}
	position, _, err := locateEdit(source, current, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	if position != 2 {
		t.Fatalf("position = %d, want 2", position)
	}
}

func TestLocateEditRejectsAmbiguousInsertion(t *testing.T) {
	source := []string{"a", "b", "a", "b", "a", "b", "a"}
	current := []string{"shift", "a", "b", "a", "b", "a", "b", "a"}
	if _, _, err := locateEdit(source, current, 2, 0); err == nil {
		t.Fatal("locateEdit unexpectedly chose an ambiguous insertion")
	}
}

func TestLocateEditRejectsMissingSourceLines(t *testing.T) {
	source := []string{"one", "two", "three"}
	current := []string{"one", "changed", "three"}
	if _, _, err := locateEdit(source, current, 2, 1); err == nil {
		t.Fatal("locateEdit unexpectedly located missing source lines")
	}
}

func TestLocateEditFollowsWhitespaceLineDrift(t *testing.T) {
	source := []string{"before", "if (value)", "{", "return value;", "}", "after"}
	current := []string{"before", "if (value) {", "return value;", "}", "after"}
	position, matchedLines, err := locateEdit(source, current, 2, 4)
	if err != nil {
		t.Fatal(err)
	}
	if position != 1 || matchedLines != 3 {
		t.Fatalf("location = (%d, %d), want (1, 3)", position, matchedLines)
	}
}

func TestLocateEditMatchesDifferentLineEndings(t *testing.T) {
	source := []string{"before\r", "remove\r", "after\r"}
	current := []string{"before", "remove", "after"}
	position, matchedLines, err := locateEdit(source, current, 2, 1)
	if err != nil {
		t.Fatal(err)
	}
	if position != 1 || matchedLines != 1 {
		t.Fatalf("location = (%d, %d), want (1, 1)", position, matchedLines)
	}
}

func TestLocateEditFollowsLineBoundaryDrift(t *testing.T) {
	source := []string{"if (value) return value;", "after"}
	current := []string{"if (value)", "return value;", "after"}
	position, matchedLines, err := locateEdit(source, current, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if position != 0 || matchedLines != 2 {
		t.Fatalf("location = (%d, %d), want (0, 2)", position, matchedLines)
	}
}

func TestFragmentSupersededByHead(t *testing.T) {
	if !fragmentSupersededByHead([]byte("reviewed"), true, nil, false) {
		t.Fatal("deleted HEAD path was not treated as superseded")
	}
	if !fragmentSupersededByHead([]byte("same"), true, []byte("same"), true) {
		t.Fatal("matching HEAD content was not treated as superseded")
	}
	if fragmentSupersededByHead([]byte("reviewed"), true, []byte("different"), true) {
		t.Fatal("different HEAD content was treated as superseded")
	}
}

func TestLocateEditFollowsInsertedContextBeforeInsertion(t *testing.T) {
	source := []string{"start", "end"}
	current := []string{"start", "context", "end"}
	position, matchedLines, err := locateEdit(source, current, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if position != 2 || matchedLines != 0 {
		t.Fatalf("location = (%d, %d), want (2, 0)", position, matchedLines)
	}
}

func TestLocateEditUsesUniqueBeforeContextWhenAfterContextDrifted(t *testing.T) {
	source := []string{"first", "target", "after", "second", "target", "after"}
	current := []string{"first", "target", "after", "second", "target", "changed"}
	position, matchedLines, err := locateEdit(source, current, 5, 1)
	if err != nil {
		t.Fatal(err)
	}
	if position != 4 || matchedLines != 1 {
		t.Fatalf("location = (%d, %d), want (4, 1)", position, matchedLines)
	}
}
