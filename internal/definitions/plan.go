package definitions

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// WriteMode says how a planned file lands on disk relative to what is already
// there. It is deliberately not the same type as the caller-facing Mode
// (create/upsert): a create request can still plan a "replace" write when the
// target file exists as a stale leftover, while an upsert plans "replace"
// because it merged the existing content.
type WriteMode string

const (
	// WriteCreate writes a file that must not exist yet.
	WriteCreate WriteMode = "create"
	// WriteReplace overwrites an existing file with merged content.
	WriteReplace WriteMode = "replace"
)

// WriteOp is one file in a plan.
//
// Path is config-root-RELATIVE and already Cleaned. Keeping it relative is what
// makes the plan replayable against the staging tree: the same plan applies to
// <root>/... and to <root>/.heron-staging-<n>/... without rewriting paths.
//
// Original holds the bytes read from the real tree during step 1 of the apply
// pipeline. It is captured at plan time rather than at commit time because it
// serves two jobs at once: it is the rollback image if a later file fails to
// commit, and it is the baseline the commit-time re-read compares against to
// detect a concurrent edit.
type WriteOp struct {
	Path       string
	Data       []byte
	Mode       WriteMode
	CreatesDir bool
	Original   []byte
	Existed    bool
}

// WritePlan is the ordered set of files one apply will touch.
//
// A plan is built before anything is written so the commit step has no
// decisions left to make: every byte, every path and the commit order are
// fixed up front. That is what makes a partial failure recoverable — the
// rollback path needs the original bytes, and by the time it runs it can no
// longer trust the tree to still hold them.
type WritePlan struct {
	Ops []WriteOp
}

// Add appends an operation. Duplicate paths are rejected rather than silently
// collapsed: two ops on one path would mean two different writers disagreeing
// about the same file, and whichever lands second would corrupt the rollback
// image of the first.
func (p *WritePlan) Add(op WriteOp) error {
	clean := filepath.Clean(op.Path)
	for _, existing := range p.Ops {
		if existing.Path == clean {
			return fmt.Errorf("write plan lists %q twice", clean)
		}
	}
	op.Path = clean
	if op.Mode == "" {
		op.Mode = WriteCreate
	}
	p.Ops = append(p.Ops, op)
	return nil
}

// InCommitOrder returns a copy of the ops ordered so that definitions which are
// REFERENCED by other definitions are written first: agents and teams before
// the flow that binds them.
//
// Multi-file commit is not one atomic syscall, so the ordering is the
// atomicity guarantee. If the process dies midway, every prefix of this order
// is still a loadable tree: a team referencing a missing agent fails
// validation, but an agent nobody references yet is merely an orphan file that
// the next `Define` can pick up. The reverse order would leave a flow pointing
// at a team that does not exist, which breaks the running engine's next reload.
func (p WritePlan) InCommitOrder() []WriteOp {
	ordered := append([]WriteOp(nil), p.Ops...)
	sort.SliceStable(ordered, func(i, j int) bool {
		return commitRank(ordered[i].Path) < commitRank(ordered[j].Path)
	})
	return ordered
}

// commitRank buckets a config-root-relative path by how late it must be
// written. Lower ranks commit first. Unknown locations sort last: a file we do
// not recognise is more likely to reference something than to be referenced,
// and writing it late keeps the referenced-definitions-first property intact.
func commitRank(path string) int {
	switch firstSegment(path) {
	case "agents":
		return 0
	case "teams":
		return 1
	case "flows":
		return 2
	default:
		return 3
	}
}

func firstSegment(path string) string {
	clean := filepath.ToSlash(filepath.Clean(path))
	if idx := strings.Index(clean, "/"); idx >= 0 {
		return clean[:idx]
	}
	return clean
}

// safeJoin resolves a config-root-relative path against root, rejecting
// anything that would escape.
//
// The config root is the boundary of what `Define` may write; a model-supplied
// name is untrusted input, and `../../evil` or an absolute path would otherwise
// let one tool call write anywhere the process can. internal/workspace has a
// similar-looking helper but it is rooted at the WORKSPACE root, a different
// concept — reusing it here would silently validate against the wrong tree.
func safeJoin(root, rel string) (string, error) {
	if strings.TrimSpace(rel) == "" {
		return "", errors.New("path must not be empty")
	}
	if filepath.IsAbs(rel) {
		return "", fmt.Errorf("path %q must be relative to the config root", rel)
	}

	clean := filepath.Clean(rel)
	if clean == "." || clean == ".." ||
		clean == string(filepath.Separator) ||
		strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes the config root", rel)
	}

	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", fmt.Errorf("resolve config root %q: %w", root, err)
	}
	joined := filepath.Join(rootAbs, clean)

	// Join already Cleans, so a textual prefix check is enough here; the Rel is
	// belt-and-braces for platforms where Clean and Join disagree about
	// separators.
	back, err := filepath.Rel(rootAbs, joined)
	if err != nil || back == ".." || strings.HasPrefix(back, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes the config root", rel)
	}
	return joined, nil
}

// applyPlanTo writes every op into treeRoot, which must be either the real
// config root or a staging copy of it, in commit order.
//
// before can be nil. When set it runs after the REFERENCED files (agents,
// teams) have landed and before the first REFERENCING file (the flow) is
// touched; tests use it to simulate a crash between the two halves and assert
// the half-written tree still loads. A non-nil error from before aborts the
// remaining writes so the caller's rollback path exercises the same state a
// real failure would leave.
func (p WritePlan) applyPlanTo(treeRoot string, before func() error) error {
	for _, op := range p.InCommitOrder() {
		target, err := safeJoin(treeRoot, op.Path)
		if err != nil {
			return err
		}

		if op.CreatesDir {
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return fmt.Errorf("create directory for %s: %w", op.Path, err)
			}
		}

		if err := writeFileAtomic(target, op.Data); err != nil {
			return fmt.Errorf("write %s: %w", op.Path, err)
		}
	}
	if before != nil {
		if err := before(); err != nil {
			return err
		}
	}
	return nil
}

// writeFileAtomic publishes data at path via a temp file in the same directory
// plus rename.
//
// Same-directory temp file matters: rename is only atomic within one
// filesystem, and a temp file in /tmp could silently degrade into a copy. The
// fsync before the rename is what makes the swap survive a power loss rather
// than leaving a zero-length file where a valid definition used to be.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)

	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".heron-tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()

	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
	}

	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	// CreateTemp makes the file 0600; definitions are read by humans and
	// committed to git, so give them the same mode a plain os.WriteFile would.
	if err := os.Chmod(tmpName, 0o644); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}

// rollbackResult reports what a rollback managed to undo. Unexported: the only
// consumer is apply, which turns a non-OK result into error text.
type rollbackResult struct {
	// Failed lists files whose restore attempt returned an error.
	Failed []string
	// Errors carries the per-file failures, index-aligned with Failed.
	Errors []string
	// Surviving lists files that were committed but could not be restored; they
	// are the ones a human has to look at.
	Surviving []string
}

// OK reports whether every committed file was put back the way it was.
func (r rollbackResult) OK() bool { return len(r.Surviving) == 0 }

// Error renders the surviving-file list, or "" when the rollback was clean.
func (r rollbackResult) Error() string {
	if r.OK() {
		return ""
	}
	return fmt.Sprintf("rollback failed for %s", strings.Join(r.Surviving, ", "))
}

// rollback undoes the ops that were already committed, newest write first.
//
// Reverse order is not cosmetic: it is the inverse of the commit order, so
// undoing the flow before the team it binds means the flow never references a
// definition that has already been removed. Restoring forward would leave a
// window where the tree on disk does not load — exactly the state the ordering
// in InCommitOrder exists to avoid.
//
// Op selection is by path, so the caller can hand over the ops it actually
// finished (with Original/Existed filled from plan time) and skip the rest.
func rollback(treeRoot string, ops []WriteOp) rollbackResult {
	var result rollbackResult

	for i := len(ops) - 1; i >= 0; i-- {
		op := ops[i]
		target, err := safeJoin(treeRoot, op.Path)
		if err != nil {
			result.Failed = append(result.Failed, op.Path)
			result.Errors = append(result.Errors, err.Error())
			result.Surviving = append(result.Surviving, op.Path)
			continue
		}

		if !op.Existed {
			// The file was created by this apply, so undoing it means removing
			// it. A missing file is a success: either we never got that far or
			// a concurrent reader already cleaned it up.
			if err := os.Remove(target); err != nil && !os.IsNotExist(err) {
				result.Failed = append(result.Failed, op.Path)
				result.Errors = append(result.Errors, err.Error())
				result.Surviving = append(result.Surviving, op.Path)
			}
			continue
		}

		if err := writeFileAtomic(target, op.Original); err != nil {
			result.Failed = append(result.Failed, op.Path)
			result.Errors = append(result.Errors, err.Error())
			result.Surviving = append(result.Surviving, op.Path)
		}
	}

	return result
}
