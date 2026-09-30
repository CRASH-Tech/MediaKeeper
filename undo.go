package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

const journalDir = ".mediakeeper"

type Replaced struct {
	Path string
	Old  []byte
}

// Journal records everything one run changed on disk, so that the run can
// be reverted with -undo. It is rewritten after every file, which keeps it
// usable even if the program is interrupted.
type Journal struct {
	Time        time.Time
	Root        string
	Moves       []Move     // in the order they were made
	Created     []string   // new files: .nfo, images
	Replaced    []Replaced // files that existed and were overwritten
	MadeDirs    []string
	RemovedDirs []string
	Tagged      []string // files whose embedded tags were rewritten

	path string
}

func NewJournal(root string) *Journal {
	now := time.Now()
	return &Journal{Time: now, Root: root,
		path: filepath.Join(root, journalDir, now.Format("20060102-150405.000000000")+".json")}
}

func (j *Journal) empty() bool {
	return len(j.Moves)+len(j.Created)+len(j.Replaced)+len(j.MadeDirs)+len(j.RemovedDirs)+len(j.Tagged) == 0
}

func (j *Journal) Save() error {
	if j.empty() {
		return nil
	}
	data, err := json.MarshalIndent(j, "", " ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(j.path), 0o755); err != nil {
		return err
	}
	tmp := j.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, j.path)
}

// MkdirAll creates a directory and remembers which levels were new.
func (j *Journal) MkdirAll(dir string) error {
	var missing []string
	for d := dir; ; d = filepath.Dir(d) {
		if _, err := os.Stat(d); err == nil || d == filepath.Dir(d) {
			break
		}
		missing = append(missing, d)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	j.MadeDirs = append(j.MadeDirs, missing...)
	return nil
}

// WriteFile writes a generated file, keeping the previous content for undo.
// An identical file is left alone.
func (j *Journal) WriteFile(path string, data []byte) error {
	old, err := os.ReadFile(path)
	if err == nil && string(old) == string(data) {
		return nil
	}
	existed := err == nil
	if err := j.MkdirAll(filepath.Dir(path)); err != nil {
		return err
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return err
	}
	if existed {
		j.Replaced = append(j.Replaced, Replaced{path, old})
	} else {
		j.Created = append(j.Created, path)
	}
	return nil
}

func journals(root string) []string {
	list, _ := filepath.Glob(filepath.Join(root, journalDir, "*.json"))
	sort.Strings(list)
	return list
}

// Undo reverts the most recent run recorded in root.
func Undo(ui *UI, root string) error {
	list := journals(root)
	if len(list) == 0 {
		ui.Printf("No recorded runs in %s — nothing to undo.\n", root)
		return nil
	}
	path := list[len(list)-1]
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var j Journal
	if err := json.Unmarshal(data, &j); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	ui.Printf("Undoing the run of %s\n", j.Time.Format("2006-01-02 15:04:05"))

	problems := 0
	warn := func(err error) {
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			problems++
			ui.Printf("  %s\n", ui.Yellow("! "+err.Error()))
		}
	}
	for _, p := range j.Created {
		warn(os.Remove(p))
	}
	for _, r := range j.Replaced {
		warn(os.WriteFile(r.Path, r.Old, 0o644))
	}
	for _, d := range j.RemovedDirs {
		warn(os.MkdirAll(d, 0o755))
	}
	// Deepest first; a directory that got other files meanwhile stays.
	sort.Slice(j.MadeDirs, func(a, b int) bool { return len(j.MadeDirs[a]) > len(j.MadeDirs[b]) })
	restored := 0
	for i := len(j.Moves) - 1; i >= 0; i-- {
		m := j.Moves[i]
		st, err := os.Stat(m.Dst)
		isDir := err == nil && st.IsDir()
		if isDir {
			// A whole folder goes back: first drop the subfolders the run
			// made in it, while they can still be found by their new paths.
			for _, d := range j.MadeDirs {
				if within(m.Dst, d) {
					os.Remove(d)
				}
			}
		}
		if err := moveFile(nil, Move{Src: m.Dst, Dst: m.Src}); err != nil {
			problems++
			ui.Printf("  %s\n", ui.Red("✗ "+err.Error()))
			continue
		}
		if !isDir {
			restored++
		}
	}
	for _, d := range j.MadeDirs {
		os.Remove(d)
	}

	ui.Printf("Files moved back: %d, created files removed: %d\n", restored, len(j.Created))
	if len(j.Tagged) > 0 {
		ui.Printf("%s\n", ui.Yellow(fmt.Sprintf(
			"Tags written into files (%d) cannot be undone — use -no-tags while experimenting.", len(j.Tagged))))
	}
	if problems > 0 {
		return fmt.Errorf("not everything could be restored (%d problems); the journal is kept: %s", problems, path)
	}
	if err := os.Remove(path); err != nil {
		return err
	}
	os.Remove(filepath.Join(root, journalDir)) // only if it is empty now
	if left := len(list) - 1; left > 0 {
		ui.Printf("Earlier runs that can be undone too: %d\n", left)
	}
	return nil
}
