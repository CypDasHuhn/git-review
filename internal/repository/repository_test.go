package repository

import "testing"

func TestLocateEditFollowsShiftedSourceLines(t *testing.T) {
	source := []string{"one", "two", "three", "four"}
	current := []string{"zero", "one", "two", "three", "four"}
	position, err := locateEdit(source, current, 2, 1)
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
	if _, err := locateEdit(source, current, 2, 0); err == nil {
		t.Fatal("locateEdit unexpectedly chose an ambiguous insertion")
	}
}

func TestLocateEditRejectsMissingSourceLines(t *testing.T) {
	source := []string{"one", "two", "three"}
	current := []string{"one", "changed", "three"}
	if _, err := locateEdit(source, current, 2, 1); err == nil {
		t.Fatal("locateEdit unexpectedly located missing source lines")
	}
}
