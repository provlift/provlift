// Package hcledit applies version constraint changes to Terraform files.
//
// Edits are byte splices over the exact range of the `version` literal, so
// comments, alignment and file ordering survive untouched. Nothing else in the
// file is re-rendered.
package hcledit

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/provlift/provlift/internal/discovery"
	"github.com/provlift/provlift/internal/model"
)

// Edit sets the version constraint of one provider requirement.
type Edit struct {
	Requirement model.ProviderRequirement
	New         string
}

// Result reports the outcome for a single file.
type Result struct {
	File    string
	Changed bool
}

// Apply writes all edits to disk, grouped by file.
func Apply(edits []Edit) ([]Result, error) {
	return applyWithWriter(edits, writeFileAtomic)
}

// Recover restores interrupted atomic replacements before a write-mode scan.
func Recover(root string, excludes []string) error {
	return filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if path != root && discovery.ShouldSkipDir(root, path, excludes) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(entry.Name(), ".tf.provlift-backup") {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing symlink backup %s", path)
		}
		return recoverBackup(strings.TrimSuffix(path, ".provlift-backup"))
	})
}

func applyWithWriter(edits []Edit, write func(string, []byte) error) ([]Result, error) {
	seen := map[string]bool{}
	for _, edit := range edits {
		path := edit.Requirement.FilePath
		if path != "" && !seen[path] {
			if err := recoverBackup(path); err != nil {
				return nil, fmt.Errorf("recovering %s: %w", path, err)
			}
			seen[path] = true
		}
	}
	rendered, err := Render(edits)
	if err != nil {
		return nil, err
	}

	originals := make(map[string][]byte, len(rendered))
	for _, r := range rendered {
		if !r.Changed {
			continue
		}
		originals[r.File] = r.original
	}

	results := make([]Result, 0, len(rendered))
	written := make([]string, 0, len(originals))
	for _, r := range rendered {
		if !r.Changed {
			results = append(results, Result{File: r.File, Changed: false})
			continue
		}
		current, err := os.ReadFile(r.File)
		if err != nil || !bytes.Equal(current, originals[r.File]) {
			rollbackErr := rollbackFiles(written, originals)
			if rollbackErr != nil {
				return nil, fmt.Errorf("%w; rollback failed: %v", staleError(r.File), rollbackErr)
			}
			return nil, staleError(r.File)
		}
		if err := write(r.File, r.Content); err != nil {
			rollbackErr := rollbackFiles(append(written, r.File), originals)
			if rollbackErr != nil {
				return nil, fmt.Errorf("applying %s: %w; rollback failed: %v", r.File, err, rollbackErr)
			}
			return nil, fmt.Errorf("applying %s: %w", r.File, err)
		}
		written = append(written, r.File)
		results = append(results, Result{File: r.File, Changed: true})
	}
	return results, nil
}

func rollbackFiles(files []string, originals map[string][]byte) error {
	var failures []error
	for i := len(files) - 1; i >= 0; i-- {
		path := files[i]
		if _, err := os.Lstat(path + ".provlift-backup"); err == nil {
			if err := recoverBackup(path); err != nil {
				failures = append(failures, fmt.Errorf("restoring backup for %s: %w", path, err))
			}
			continue
		} else if !os.IsNotExist(err) {
			failures = append(failures, fmt.Errorf("checking backup for %s: %w", path, err))
			continue
		}
		if current, err := os.ReadFile(path); err == nil && bytes.Equal(current, originals[path]) {
			continue
		}
		if err := writeFileAtomic(path, originals[path]); err != nil {
			failures = append(failures, fmt.Errorf("restoring %s: %w", path, err))
		}
	}
	return errors.Join(failures...)
}

// RenderedFile is the proposed new content of a file, used for dry runs.
type RenderedFile struct {
	File     string
	Content  []byte
	Changed  bool
	original []byte
}

// Render computes the new content for every affected file without writing it.
func Render(edits []Edit) ([]RenderedFile, error) {
	byFile := map[string][]Edit{}
	var order []string
	for _, e := range edits {
		path := e.Requirement.FilePath
		if path == "" {
			return nil, fmt.Errorf("provider %q has no source file recorded", e.Requirement.LocalName)
		}
		if _, seen := byFile[path]; !seen {
			order = append(order, path)
		}
		byFile[path] = append(byFile[path], e)
	}
	sort.Strings(order)

	out := make([]RenderedFile, 0, len(order))
	for _, path := range order {
		original, content, changed, err := renderFile(path, byFile[path])
		if err != nil {
			return nil, err
		}
		out = append(out, RenderedFile{File: path, Content: content, Changed: changed, original: original})
	}
	return out, nil
}

func renderFile(path string, edits []Edit) ([]byte, []byte, bool, error) {
	src, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, false, fmt.Errorf("reading %s: %w", path, err)
	}

	type splice struct {
		start, end int
		text       string
	}

	splices := make([]splice, 0, len(edits))
	for _, e := range edits {
		req := e.Requirement
		if sha256.Sum256(src) != req.SourceHash {
			return nil, nil, false, staleError(path)
		}

		switch {
		case req.VersionRange != nil:
			if err := verifyRange(path, src, *req.VersionRange, req.Constraint); err != nil {
				return nil, nil, false, err
			}
			splices = append(splices, splice{
				start: req.VersionRange.Start,
				end:   req.VersionRange.End,
				text:  strconv.Quote(e.New),
			})

		case req.IsObject && req.InsertPoint > 0:
			// No version argument exists yet, so add one after the last
			// argument in the object. Alignment is left to `terraform fmt`.
			if req.ObjectRange == nil || req.ObjectRange.Start < 0 || req.ObjectRange.Start > req.ObjectRange.End || req.ObjectText == "" || req.ObjectRange.End > len(src) ||
				string(src[req.ObjectRange.Start:req.ObjectRange.End]) != req.ObjectText {
				return nil, nil, false, staleError(path)
			}
			if req.InsertPoint < req.ObjectRange.Start || req.InsertPoint >= req.ObjectRange.End ||
				!bytes.HasSuffix(bytes.TrimSpace(src[req.InsertPoint:req.ObjectRange.End]), []byte("}")) {
				return nil, nil, false, staleError(path)
			}
			splices = append(splices, splice{
				start: req.InsertPoint,
				end:   req.InsertPoint,
				text:  "\nversion = " + strconv.Quote(e.New),
			})

		default:
			return nil, nil, false, fmt.Errorf(
				"cannot edit provider %q in %s: no version argument and no insertion point",
				req.LocalName, path)
		}
	}

	// Apply back to front so earlier offsets remain valid.
	sort.Slice(splices, func(i, j int) bool { return splices[i].start > splices[j].start })

	updated := src
	for _, s := range splices {
		if s.start < 0 || s.end > len(updated) || s.start > s.end {
			return nil, nil, false, staleError(path)
		}
		var buf bytes.Buffer
		buf.Grow(len(updated) + len(s.text))
		buf.Write(updated[:s.start])
		buf.WriteString(s.text)
		buf.Write(updated[s.end:])
		updated = buf.Bytes()
	}

	return src, updated, !bytes.Equal(src, updated), nil
}

// verifyRange guards against acting on a stale scan: the bytes at the recorded
// range must still be the quoted constraint we parsed.
func verifyRange(path string, src []byte, r model.ByteRange, expected string) error {
	if r.Start < 0 || r.End > len(src) || r.Start >= r.End {
		return staleError(path)
	}
	got := string(src[r.Start:r.End])
	unquoted, err := strconv.Unquote(got)
	if err != nil || unquoted != expected {
		return staleError(path)
	}
	return nil
}

func staleError(path string) error {
	return fmt.Errorf("%s changed since it was scanned; re-run the command", path)
}

func writeFileAtomic(path string, content []byte) error {
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		mode = info.Mode().Perm()
	}

	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".provlift-*.tmp")
	if err != nil {
		return fmt.Errorf("creating temp file in %s: %w", dir, err)
	}
	tmpName := tmp.Name()

	defer func() {
		tmp.Close()
		os.Remove(tmpName)
	}()

	if _, err := tmp.Write(content); err != nil {
		return fmt.Errorf("writing %s: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("syncing %s: %w", tmpName, err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("closing %s: %w", tmpName, err)
	}
	if err := os.Chmod(tmpName, mode); err != nil {
		return fmt.Errorf("setting mode on %s: %w", tmpName, err)
	}
	if err := replaceFile(tmpName, path); err != nil {
		return fmt.Errorf("replacing %s: %w", path, err)
	}
	return nil
}

func recoverBackup(path string) error {
	backup := path + ".provlift-backup"
	info, err := os.Lstat(backup)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("refusing non-regular backup %s", backup)
	}
	if _, err := os.Stat(path); err == nil {
		if err := os.Remove(path); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	return os.Rename(backup, path)
}

func replaceFile(tmpName, path string) error {
	if err := os.Rename(tmpName, path); err == nil {
		return nil
	}
	backup := path + ".provlift-backup"
	if err := os.Rename(path, backup); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Rename(backup, path)
		return err
	}
	return os.Remove(backup)
}
