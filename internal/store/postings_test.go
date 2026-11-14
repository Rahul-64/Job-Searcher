package store

import "testing"

func pi(id, hash string) PostingInput { return PostingInput{ExternalID: id, ContentHash: hash} }

func TestDedupeKeepsLastPreservesOrder(t *testing.T) {
	in := []PostingInput{pi("a", "1"), pi("b", "1"), pi("a", "2")}
	out := dedupe(in)
	if len(out) != 2 || out[0].ExternalID != "a" || out[1].ExternalID != "b" {
		t.Fatalf("unexpected order/len: %+v", out)
	}
	if out[0].ContentHash != "2" {
		t.Errorf("last occurrence should win, got hash %q", out[0].ContentHash)
	}
}

func TestClassify(t *testing.T) {
	jobID := int64(7)
	existing := map[string]existingPosting{
		"same":    {ID: 1, Hash: "h1", JobID: &jobID},
		"changed": {ID: 2, Hash: "old", JobID: &jobID},
		"gone":    {ID: 3, Hash: "h3"},
	}
	in := []PostingInput{pi("same", "h1"), pi("changed", "new"), pi("fresh", "h9")}

	fresh, changed := classify(existing, in)

	if len(fresh) != 1 || fresh[0].ExternalID != "fresh" {
		t.Errorf("fresh = %+v", fresh)
	}
	if len(changed) != 1 || changed[0].Input.ExternalID != "changed" || changed[0].Existing.ID != 2 {
		t.Errorf("changed = %+v", changed)
	}
}

func TestClassifyEmpty(t *testing.T) {
	fresh, changed := classify(map[string]existingPosting{}, nil)
	if len(fresh) != 0 || len(changed) != 0 {
		t.Fatal("expected nothing")
	}
}
