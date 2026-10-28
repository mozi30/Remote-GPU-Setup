// Package filter applies the rclone-style exclude list on the PC side
// (local uploads/downloads), matching what rclone does on the box for the
// patterns this tool uses:
//
//	"name/**"   directory "name" at any depth
//	"/name/**"  directory "name" at the top level only
//	"*.ckpt"    file name glob at any depth
//	"/a/b.txt"  exact path from the top
package filter

import (
	"path"
	"strings"
)

type Filter struct{ pats []string }

func New(patterns []string) *Filter { return &Filter{pats: patterns} }

// Excluded reports whether rel (slash-separated, relative to the workspace
// root) is excluded. For directories it returns true when the whole tree is.
func (f *Filter) Excluded(rel string, isDir bool) bool {
	rel = strings.Trim(rel, "/")
	parts := strings.Split(rel, "/")
	for _, p := range f.pats {
		anchored := strings.HasPrefix(p, "/")
		core := strings.TrimPrefix(strings.TrimPrefix(p, "/"), "**/")
		if dir, ok := strings.CutSuffix(core, "/**"); ok {
			if matchDir(dir, parts, anchored, isDir) {
				return true
			}
			continue
		}
		if isDir {
			continue
		}
		if anchored || strings.Contains(core, "/") {
			if ok, _ := path.Match(core, rel); ok {
				return true
			}
		} else if ok, _ := path.Match(core, parts[len(parts)-1]); ok {
			return true
		}
	}
	return false
}

// matchDir: does the directory pattern (one or more segments) cover rel?
func matchDir(dir string, parts []string, anchored, isDir bool) bool {
	segs := strings.Split(dir, "/")
	// a file must sit *inside* the dir; a dir may be the dir itself
	limit := len(parts) - 1
	if isDir {
		limit = len(parts)
	}
	for start := 0; start+len(segs) <= limit; start++ {
		ok := true
		for i, s := range segs {
			if m, _ := path.Match(s, parts[start+i]); !m {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
		if anchored {
			break
		}
	}
	return false
}
