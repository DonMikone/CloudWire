package identity

import (
	"path"
	"slices"
	"strconv"
	"strings"
)

// DetectLocal returns the local renames between two scans as from -> to:
// paths of prev that vanished and whose identity reappeared at exactly one
// new path. With persistent inodes the identity is the inode; otherwise
// (ADR 0011, exFAT and network volumes) a folder is identified by the exact
// relative paths, sizes and modification times of every file below it and
// a file by its unique size and modification time. marker names excluded
// targets (Conflict Copies). Only outermost renames are returned.
func DetectLocal(prev, cur map[string]LocalEntry, persistent bool, marker string) map[string]string {
	gone, fresh := vanished(prev, cur)
	raw := map[string]string{}
	if persistent {
		byIno := map[uint64][]string{}
		for p, e := range cur {
			if e.Ino != 0 {
				byIno[e.Ino] = append(byIno[e.Ino], p)
			}
		}
		for _, p := range gone {
			e := prev[p]
			at := byIno[e.Ino]
			// An inode at several paths is a hard link: ambiguous.
			if e.Ino == 0 || len(at) != 1 || cur[at[0]].Dir != e.Dir {
				continue
			}
			if _, known := prev[at[0]]; !known {
				raw[p] = at[0]
			}
		}
	} else {
		matchUnique(raw, signatures(prev, gone), signatures(cur, fresh))
	}
	return outermost(dropMarked(raw, marker))
}

// DetectCloud returns the cloud renames between two scans by file id, like
// DetectLocal. Entries without an id are never matched.
func DetectCloud(prev, cur map[string]CloudEntry, marker string) map[string]string {
	gone, _ := vanished(prev, cur)
	byID := map[string][]string{}
	for p, e := range cur {
		if e.ID != "" {
			byID[e.ID] = append(byID[e.ID], p)
		}
	}
	raw := map[string]string{}
	for _, p := range gone {
		e := prev[p]
		at := byID[e.ID]
		if e.ID == "" || len(at) != 1 || cur[at[0]].Dir != e.Dir {
			continue
		}
		if _, known := prev[at[0]]; !known {
			raw[p] = at[0]
		}
	}
	return outermost(dropMarked(raw, marker))
}

// vanished returns the sorted paths of prev missing in cur and of cur missing in prev.
func vanished[E any](prev, cur map[string]E) (gone, fresh []string) {
	for p := range prev {
		if _, ok := cur[p]; !ok {
			gone = append(gone, p)
		}
	}
	for q := range cur {
		if _, ok := prev[q]; !ok {
			fresh = append(fresh, q)
		}
	}
	slices.Sort(gone)
	slices.Sort(fresh)
	return gone, fresh
}

// matchUnique pairs gone and fresh paths whose signatures are equal and
// occur exactly once on each side.
func matchUnique(raw map[string]string, gone, fresh map[string]string) {
	index := func(sigs map[string]string) map[string][]string {
		m := map[string][]string{}
		for p, s := range sigs {
			m[s] = append(m[s], p)
		}
		return m
	}
	g, f := index(gone), index(fresh)
	for s, ps := range g {
		if qs := f[s]; len(ps) == 1 && len(qs) == 1 {
			raw[ps[0]] = qs[0]
		}
	}
}

// signatures identifies the listed paths of m without inodes: a file by its
// size and modification time, a folder by the relative path, size and
// modification time of every file below it. Folders without files get none.
func signatures(m map[string]LocalEntry, list []string) map[string]string {
	out := map[string]string{}
	lines := map[string][]string{}
	for _, p := range list {
		if m[p].Dir {
			lines[p] = nil
		} else {
			out[p] = fileSignature(m[p])
		}
	}
	for p, e := range m {
		if e.Dir {
			continue
		}
		for a := path.Dir(p); a != "." && a != "/"; a = path.Dir(a) {
			if l, ok := lines[a]; ok {
				lines[a] = append(l, p[len(a)+1:]+"\x00"+fileSignature(e))
			}
		}
	}
	for d, l := range lines {
		if len(l) > 0 {
			slices.Sort(l)
			out[d] = "d " + strings.Join(l, "\x01")
		}
	}
	return out
}

// fileSignature identifies a file by its size and modification time.
func fileSignature(e LocalEntry) string {
	return "f " + strconv.FormatInt(e.Size, 10) + " " + strconv.FormatInt(e.MTime, 10)
}

// dropMarked removes renames to a Conflict Copy name: bisync renames a
// conflict loser itself, and a run that failed afterwards must not make that
// look like a user's rename.
func dropMarked(raw map[string]string, marker string) map[string]string {
	if marker == "" {
		return raw
	}
	for p, q := range raw {
		if strings.Contains(path.Base(q), marker) {
			delete(raw, p)
		}
	}
	return raw
}

// outermost drops the renames that a rename of an ancestor explains (the
// entry moved along with its folder).
func outermost(raw map[string]string) map[string]string {
	out := map[string]string{}
	for p, q := range raw {
		explained := false
		for a := path.Dir(p); a != "." && a != "/"; a = path.Dir(a) {
			if aq, ok := raw[a]; ok {
				explained = q == aq+p[len(a):]
				break
			}
		}
		if !explained {
			out[p] = q
		}
	}
	return out
}
