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

func TestMassDrop(t *testing.T) {
	cases := []struct {
		name          string
		active, found int
		want          bool
	}{
		{"first run", 0, 50, false},
		{"empty first run", 0, 0, false},
		{"empty result for a board with jobs", 5, 0, true},
		{"normal churn", 100, 90, false},
		{"exactly half is fine", 100, 50, false},
		{"more than half gone on a real board", 100, 49, true},
		{"small board may swing wildly", 10, 3, false},
		{"threshold board of 20 losing 11", 20, 9, true},
		{"growth", 20, 80, false},
	}
	for _, c := range cases {
		got, why := massDrop(c.active, c.found)
		if got != c.want {
			t.Errorf("%s: massDrop(%d,%d) = %v (%q), want %v", c.name, c.active, c.found, got, why, c.want)
		}
		if got && why == "" {
			t.Errorf("%s: a guard hit must explain itself", c.name)
		}
	}
}
