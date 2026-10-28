package filter

import "testing"

func TestExcluded(t *testing.T) {
	f := New([]string{".venv/**", "__pycache__/**", "/data/**", "*.tmp", "/secret.txt"})
	cases := []struct {
		rel  string
		dir  bool
		excl bool
	}{
		{".venv", true, true},
		{"proj/.venv", true, true},
		{"proj/.venv/lib/x.py", false, true},
		{"proj/src/__pycache__/a.pyc", false, true},
		{"data", true, true},
		{"data/train.csv", false, true},
		{"proj/data/train.csv", false, false}, // /data is top-level only
		{"proj/data", true, false},
		{"notes.tmp", false, true},
		{"proj/x.tmp", false, true},
		{"secret.txt", false, true},
		{"proj/secret.txt", false, false},
		{"proj/main.py", false, false},
		{"checkpoints/model.pt", false, false},
		{".venvs/readme.md", false, false},
	}
	for _, c := range cases {
		if got := f.Excluded(c.rel, c.dir); got != c.excl {
			t.Errorf("Excluded(%q, dir=%v) = %v, want %v", c.rel, c.dir, got, c.excl)
		}
	}
}
