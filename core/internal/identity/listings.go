package identity

import (
	"bufio"
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/DonMikone/CloudWire/core/internal/selection"
	sv "github.com/DonMikone/CloudWire/core/internal/supervisor"
)

// bisync listing format v1 (rclone cmd/bisync/listing.go, pinned by tests).
const listingHeader = "# bisync listing v1 from"

var listingLine = regexp.MustCompile(`^(\S) +(-?\d+) (\S+) (\S+) (\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{9}[+-]\d{4}) (".+")$`)

// listingFiles are the bisync listings of a working directory, including
// the backups --recover reverts to.
func listingFiles(workdir string) ([]string, error) {
	var out []string
	for _, pat := range []string{"*.path1.lst", "*.path2.lst", "*.path1.lst-old", "*.path2.lst-old"} {
		m, err := filepath.Glob(filepath.Join(workdir, pat))
		if err != nil {
			return nil, err
		}
		out = append(out, m...)
	}
	return out, nil
}

// RewriteListings applies renames to bisync's listings so the next run sees
// neither a deletion nor a new file: a move renames the listed paths, an
// upload or download forgets them (bisync then copies the new name as a new
// file). Root renames leave them alone.
func RewriteListings(workdir string, rs []sv.Rename) error {
	files, err := listingFiles(workdir)
	if err != nil {
		return err
	}
	for _, f := range files {
		if err := rewriteListing(f, rs); err != nil {
			return fmt.Errorf("%s: %w", filepath.Base(f), err)
		}
	}
	return nil
}

func rewriteListing(file string, rs []sv.Rename) error {
	b, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	var out bytes.Buffer
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	first := true
	for sc.Scan() {
		line := sc.Text()
		if first {
			first = false
			if !strings.HasPrefix(line, listingHeader) {
				return fmt.Errorf("unknown listing format %q", line)
			}
		}
		m := listingLine.FindStringSubmatchIndex(line)
		if m == nil {
			out.WriteString(line + "\n")
			continue
		}
		name, err := strconv.Unquote(line[m[12]:m[13]])
		if err != nil {
			out.WriteString(line + "\n")
			continue
		}
		name, keep := renamePath(name, rs)
		if keep {
			out.WriteString(line[:m[12]] + strconv.Quote(name) + "\n")
		}
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if first {
		return fmt.Errorf("empty listing")
	}
	return writeAtomic(file, out.Bytes(), 0o600)
}

// renamePath applies renames in order to an item-relative path; false if a
// rename forgets it.
func renamePath(p string, rs []sv.Rename) (string, bool) {
	for _, r := range rs {
		switch r.Result {
		case sv.RenameMoved:
			p, _ = selection.Rebase(p, r.From, r.To)
		case sv.RenameUpload, sv.RenameDownload:
			if _, under := selection.Rebase(p, r.From, ""); under {
				return "", false
			}
		}
	}
	return p, true
}

// listedPaths returns the paths of the current listings of one side
// ("path1" local, "path2" cloud).
func listedPaths(workdir, side string) (map[string]bool, error) {
	files, err := filepath.Glob(filepath.Join(workdir, "*."+side+".lst"))
	if err != nil {
		return nil, err
	}
	out := map[string]bool{}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			return nil, err
		}
		for _, line := range strings.Split(string(b), "\n") {
			if m := listingLine.FindStringSubmatch(line); m != nil {
				if name, err := strconv.Unquote(m[6]); err == nil {
					out[name] = true
				}
			}
		}
	}
	return out, nil
}

// WriteFiltersHash stores the hash bisync keeps of its filters file, so a
// Selection changed by renames does not demand a resync.
func WriteFiltersHash(filtersFile string) error {
	b, err := os.ReadFile(filtersFile)
	if err != nil {
		return err
	}
	sum := md5.Sum(b)
	return writeAtomic(filtersFile+".md5", []byte(hex.EncodeToString(sum[:])), 0o600)
}
