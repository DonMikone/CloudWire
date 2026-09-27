package identity

import (
	"errors"
	"io/fs"
	"os"
	"path"
	"slices"
	"strings"

	"github.com/DonMikone/CloudWire/core/internal/selection"
	sv "github.com/DonMikone/CloudWire/core/internal/supervisor"
)

// Op sides.
const (
	OpLocal = "local" // absolute paths (item-relative out of Reconcile)
	OpCloud = "cloud" // item-relative
	OpConn  = "conn"  // Connection-relative folder
)

// Op is one rename Apply performs.
type Op struct {
	Side     string
	From, To string
	Dir      bool
}

// Inputs are the scans and detected renames Reconcile decides on.
type Inputs struct {
	PrevLocal, CurLocal map[string]LocalEntry
	// CurCloud nil means the cloud has no ids: its side is looked up with
	// CloudStat and never renamed by itself.
	PrevCloud, CurCloud map[string]CloudEntry
	Local, Cloud        map[string]string // DetectLocal, DetectCloud
	LocalStat           func(rel string) (fs.FileInfo, error)
	CloudStat           func(rel string) (exists, dir bool, err error)
}

type move struct{ from, to string }

func through(p string, ms []move) string {
	for _, m := range ms {
		p, _ = selection.Rebase(p, m.from, m.to)
	}
	return p
}

func back(p string, ms []move) string {
	for i := len(ms) - 1; i >= 0; i-- {
		p, _ = selection.Rebase(p, ms[i].to, ms[i].from)
	}
	return p
}

// viaAncestor returns where p is now if the nearest renamed ancestor took it along.
func viaAncestor(p string, renames map[string]string) string {
	for a := path.Dir(p); a != "." && a != "/"; a = path.Dir(a) {
		if to, ok := renames[a]; ok {
			return to + p[len(a):]
		}
	}
	return p
}

// Reconcile turns the renames of both sides into moves on the other side
// (ADR 0011), outermost first:
//
//	local     cloud      action                          result
//	Q         same       cloud move P -> Q               moved
//	Q         gone       none, bisync uploads Q          upload
//	same      R          local rename P -> R             moved
//	gone      R          none, bisync downloads R        download
//	Q         R == Q     none                            moved
//	Q         R != Q     cloud move R -> Q (local wins)  moved
//
// A target that exists on the other side with another identity is a
// collision: nothing is applied and the colliding path is returned.
// Renames are expressed in order, each on the paths the previous ones left.
func Reconcile(in Inputs) (ops []Op, renames []sv.Rename, collision string, err error) {
	keys := slices.Collect(func(yield func(string) bool) {
		for p := range in.Local {
			if !yield(p) {
				return
			}
		}
		for p := range in.Cloud {
			if _, dup := in.Local[p]; !dup && !yield(p) {
				return
			}
		}
	})
	slices.Sort(keys) // an ancestor sorts before its descendants

	var lops, cops, done []move
	occupiedLocal := func(src, to string) (bool, error) {
		fi, err := in.LocalStat(back(to, lops))
		if errors.Is(err, fs.ErrNotExist) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		// A case-only rename on a case-insensitive volume finds the source itself.
		sfi, err := in.LocalStat(back(src, lops))
		return err != nil || !os.SameFile(fi, sfi), nil
	}
	occupiedCloud := func(src, to string) (bool, error) {
		pre, preSrc := back(to, cops), back(src, cops)
		if e, ok := in.CurCloud[pre]; ok {
			s := in.CurCloud[preSrc]
			return s.ID == "" || s.ID != e.ID, nil
		}
		exists, _, err := in.CloudStat(pre)
		if err != nil || !exists {
			return false, err
		}
		return !strings.EqualFold(pre, preSrc), nil
	}

	for _, p := range keys {
		lq, lRen := in.Local[p]
		lOK := lRen
		if !lRen {
			lq = viaAncestor(p, in.Local)
			_, lOK = in.CurLocal[lq]
		}
		cr, cRen := in.Cloud[p]
		cOK := cRen
		if !cRen {
			cr = viaAncestor(p, in.Cloud)
			if in.CurCloud != nil {
				_, cOK = in.CurCloud[cr]
			} else if cOK, _, err = in.CloudStat(cr); err != nil {
				return nil, nil, "", err
			}
		}
		dir := in.PrevLocal[p].Dir
		if _, ok := in.PrevLocal[p]; !ok {
			dir = in.PrevCloud[p].Dir
		}
		lNow, cNow := through(lq, lops), through(cr, cops)
		r := sv.Rename{From: through(p, done), Dir: dir}
		switch {
		case lRen && (cRen || cOK):
			r.To, r.Result = lNow, sv.RenameMoved
			if cNow != lNow {
				busy, err := occupiedCloud(cNow, lNow)
				if err != nil {
					return nil, nil, "", err
				}
				if busy {
					return nil, nil, lNow, nil
				}
				ops = append(ops, Op{Side: OpCloud, From: cNow, To: lNow, Dir: dir})
				cops = append(cops, move{cNow, lNow})
			}
		case lRen:
			r.To, r.Result = lNow, sv.RenameUpload
		case cRen && lOK:
			r.To, r.Result = cNow, sv.RenameMoved
			if lNow != cNow {
				busy, err := occupiedLocal(lNow, cNow)
				if err != nil {
					return nil, nil, "", err
				}
				if busy {
					return nil, nil, cNow, nil
				}
				ops = append(ops, Op{Side: OpLocal, From: lNow, To: cNow, Dir: dir})
				lops = append(lops, move{lNow, cNow})
			}
		case cRen:
			r.To, r.Result = cNow, sv.RenameDownload
		default:
			continue
		}
		done = append(done, move{r.From, r.To})
		renames = append(renames, r)
	}
	return ops, renames, "", nil
}
