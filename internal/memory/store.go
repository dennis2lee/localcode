package memory

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// Scope is how far a note reaches.
type Scope string

const (
	// ScopeSession notes belong to one conversation. They live in its
	// event log, not here; see internal/agent.
	ScopeSession Scope = "session"
	// ScopeProject notes belong to one repository, shared by its
	// worktrees and subdirectories.
	ScopeProject Scope = "project"
	// ScopeUser notes belong to the person, in every project.
	ScopeUser Scope = "user"
)

// Scopes in the order a lookup by name tries them: the narrowest first.
var Scopes = []Scope{ScopeSession, ScopeProject, ScopeUser}

// ParseScope reads a scope name, case-insensitively.
func ParseScope(s string) (Scope, bool) {
	sc := Scope(strings.ToLower(strings.TrimSpace(s)))
	for _, known := range Scopes {
		if sc == known {
			return sc, true
		}
	}
	return "", false
}

// Limits bound one directory of notes.
type Limits struct {
	// IndexLines and IndexBytes bound what of the index reaches the
	// prompt.
	IndexLines int
	IndexBytes int
	// Entries bounds how many notes the directory holds. Past it a save
	// is refused until something is merged or forgotten: an index that
	// only grows ends up loading its oldest notes and dropping its newest.
	Entries int
}

var (
	// ProjectLimits keep the 200 lines / 25KB that project memory has
	// always loaded, which are also Claude Code's. 150 notes, so that
	// every note's line and its group heading fit in the 200.
	ProjectLimits = Limits{IndexLines: maxIndexLines, IndexBytes: maxIndexBytes, Entries: 150}
	// UserLimits are smaller: user notes reach every project, so every
	// project pays for them.
	UserLimits = Limits{IndexLines: 80, IndexBytes: 8 * 1024, Entries: 60}
)

// Retirement defaults: a project or reference note that nobody read,
// saved or named for this many days, and this many conversations in its
// directory, leaves the index for the archive.
const (
	DefaultRetireDays     = 90
	DefaultRetireSessions = 30
)

const (
	archiveDirName = "archive"
	lockFileName   = ".lock"
	// maxReadBytes is the most of one note file that is read for the
	// model. Notes saved through the tool are far smaller; this bounds a
	// hand-written topic file somebody pasted a log into.
	maxReadBytes = 64 * 1024
	// maxIndexFileBytes is the largest MEMORY.md the tool will rewrite.
	// Rewriting means keeping the person's part byte for byte, and a file
	// too big to read whole is refused rather than cut.
	maxIndexFileBytes = 4 << 20
	fileMode          = 0o600
	dirMode           = 0o755
)

// maxArchived bounds the archive. Past it the oldest copies go: the
// archive is a way back from a mistake, not a second memory. A variable
// so a test can fill it without writing three hundred files.
var maxArchived = 300

// lockWait bounds how long a writer waits for another process that holds
// the directory. Longer means that process is stuck, and a turn should
// hear about it rather than hang. A variable so a test can wait less.
var lockWait = 10 * time.Second

// Store is one directory of notes: a project's or the user's.
type Store struct {
	Dir    string
	Scope  Scope
	Limits Limits
}

// UserDir is the user-scope directory under home.
func UserDir(home string) string {
	return filepath.Join(home, ".localcode", "memory")
}

// ErrNotFound is returned for a note that is not in the directory.
var ErrNotFound = errors.New("no such note")

// ErrBusy is returned when another process held the directory for longer
// than a writer waits.
var ErrBusy = errors.New("memory is being written by another localcode process; try again")

// procLocks serializes writers within this process. The file lock below
// does the same across processes (two daemons for two projects share the
// user directory, and a /update hands one daemon over to the next), but
// taking it costs a file open, and parallel turns in one daemon are the
// common case.
var procLocks sync.Map // dir -> *sync.Mutex

// withLock runs fn holding the directory's write lock, creating the
// directory first: a directory exists once something has been written to
// it, and not before.
func (s Store) withLock(fn func() error) error {
	mu, _ := procLocks.LoadOrStore(filepath.Clean(s.Dir), &sync.Mutex{})
	mu.(*sync.Mutex).Lock()
	defer mu.(*sync.Mutex).Unlock()
	if err := os.MkdirAll(s.Dir, dirMode); err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(s.Dir, lockFileName), os.O_CREATE|os.O_RDWR, fileMode)
	if err != nil {
		return err
	}
	defer f.Close()
	deadline := time.Now().Add(lockWait)
	for {
		ok, err := tryLockFile(f)
		if err != nil {
			return err
		}
		if ok {
			break
		}
		if time.Now().After(deadline) {
			return ErrBusy
		}
		time.Sleep(25 * time.Millisecond)
	}
	defer unlockFile(f)
	return fn()
}

// writeAtomic replaces path with data through a temporary file in the
// same directory, so a reader sees the old file or the new one and never
// half of either, and a crash leaves at worst a stray temporary file.
// Replacing also never writes through a symlink planted at path: the
// link itself is what gets replaced. CreateTemp makes the file 0600, which
// is what a note should be: it can record what the person said.
func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		os.Remove(name)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(name)
		return err
	}
	if err := os.Rename(name, path); err != nil {
		os.Remove(name)
		return err
	}
	return nil
}

// readRegular reads up to limit bytes of a regular file. A symlink, a
// directory or anything else is refused: a link planted in a memory
// directory would otherwise let the memory tools read a file the file
// tools would have asked about, and put it in every session's prompt.
func readRegular(path string, limit int64) (string, bool, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return "", false, err
	}
	if !info.Mode().IsRegular() {
		return "", false, fmt.Errorf("%s is not a regular file", filepath.Base(path))
	}
	f, err := os.Open(path)
	if err != nil {
		return "", false, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return "", false, err
	}
	cut := int64(len(data)) > limit
	if cut {
		data = data[:limit]
	}
	return string(data), cut, nil
}

// isNoteName reports whether a file name is a note's: a *.md file that is
// neither the index nor a dot file.
func isNoteName(n string) bool {
	return !strings.HasPrefix(n, ".") && strings.EqualFold(filepath.Ext(n), ".md") && !strings.EqualFold(n, indexFileName)
}

// noteFiles lists the note files in dir: regular *.md files other than
// the index, dot files excluded. A missing directory has none.
func noteFiles(dir string) ([]string, error) {
	list, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []string
	for _, d := range list {
		n := d.Name()
		if !d.Type().IsRegular() || !isNoteName(n) {
			continue
		}
		out = append(out, n)
	}
	sort.Strings(out)
	return out, nil
}

func stem(file string) string { return strings.TrimSuffix(file, filepath.Ext(file)) }

// Entries reads every note in the directory, legacy files included.
func (s Store) Entries() ([]Entry, error) {
	return readEntries(s.Dir)
}

func readEntries(dir string) ([]Entry, error) {
	files, err := noteFiles(dir)
	if err != nil {
		return nil, err
	}
	var out []Entry
	for _, f := range files {
		content, _, err := readRegular(filepath.Join(dir, f), maxReadBytes)
		if err != nil {
			continue
		}
		out = append(out, ParseEntry(stem(f), Sanitize(content)))
	}
	return out, nil
}

// archiveDir is the archive, refused when something other than a
// directory sits at its name: moving a note into a link would move it out
// of the store.
func (s Store) archiveDir(create bool) (string, error) {
	dir := filepath.Join(s.Dir, archiveDirName)
	info, err := os.Lstat(dir)
	switch {
	case err == nil && !info.IsDir():
		return "", fmt.Errorf("%s is not a directory", dir)
	case err == nil:
		return dir, nil
	case errors.Is(err, fs.ErrNotExist) && create:
		return dir, os.Mkdir(dir, dirMode)
	case errors.Is(err, fs.ErrNotExist):
		return "", nil
	default:
		return "", err
	}
}

// Archived is one copy in the archive: a note forgotten, retired, or
// replaced by a newer save.
type Archived struct {
	Entry Entry
	File  string
	At    time.Time
}

// archiveStamp is the time format in an archived copy's file name, which
// is the note's name, "~", and when it was archived: unique per copy, so
// archiving the same name twice keeps both, and sortable as text.
const archiveStamp = "20060102T150405.000000000Z"

// archivedName splits an archived file name into the note's name and
// when it was archived. A file put there by hand, without a stamp, is
// its own name, archived at its modification time.
func archivedName(file string) (string, time.Time, bool) {
	st := stem(file)
	i := strings.LastIndex(st, "~")
	if i < 0 {
		return st, time.Time{}, false
	}
	at, err := time.Parse(archiveStamp, st[i+1:])
	if err != nil {
		return st, time.Time{}, false
	}
	return st[:i], at, true
}

// ArchivedNotes lists the archive, newest first.
func (s Store) ArchivedNotes() ([]Archived, error) {
	dir, err := s.archiveDir(false)
	if err != nil || dir == "" {
		return nil, err
	}
	files, err := noteFiles(dir)
	if err != nil {
		return nil, err
	}
	var out []Archived
	for _, f := range files {
		content, _, err := readRegular(filepath.Join(dir, f), maxReadBytes)
		if err != nil {
			continue
		}
		name, at, ok := archivedName(f)
		if !ok {
			if info, err := os.Lstat(filepath.Join(dir, f)); err == nil {
				at = info.ModTime()
			}
		}
		out = append(out, Archived{Entry: ParseEntry(name, Sanitize(content)), File: f, At: at})
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.After(out[j].At) })
	return out, nil
}

// find resolves a name to a note file in dir: the normalized name first,
// then a case-insensitive match on a legacy file's own name, which may
// not be normalized ("Build_Notes.md").
func find(dir, name string) (string, error) {
	norm, err := NormalizeName(name)
	if err == nil {
		if info, serr := os.Lstat(filepath.Join(dir, norm+".md")); serr == nil && info.Mode().IsRegular() {
			return norm + ".md", nil
		}
	}
	files, ferr := noteFiles(dir)
	if ferr != nil {
		return "", ferr
	}
	want := strings.TrimSuffix(strings.TrimSpace(name), ".md")
	for _, f := range files {
		base := stem(f)
		if strings.EqualFold(base, want) {
			return f, nil
		}
		if n, nerr := NormalizeName(base); nerr == nil && err == nil && n == norm {
			return f, nil
		}
	}
	return "", ErrNotFound
}

// Read returns one note, counting the read as a use. cut reports that the
// file was longer than what was read.
func (s Store) Read(name string, now time.Time) (e Entry, cut bool, err error) {
	file, err := find(s.Dir, name)
	if err != nil {
		return Entry{}, false, err
	}
	content, cut, err := readRegular(filepath.Join(s.Dir, file), maxReadBytes)
	if err != nil {
		return Entry{}, false, err
	}
	e = ParseEntry(stem(file), Sanitize(content))
	s.NoteUse([]string{file}, now)
	return e, cut, nil
}

// Has reports whether a note file of this name is in the directory. The
// index and the usage record are files there too, and are not notes.
func (s Store) Has(file string) bool {
	if !isNoteName(file) {
		return false
	}
	info, err := os.Lstat(filepath.Join(s.Dir, file))
	return err == nil && info.Mode().IsRegular()
}

// NoteUse records that notes were used: read by the memory tool, read by
// a file tool opening the note's file, or named in the model's answer.
// Best effort: a use that cannot be recorded costs a note at most an
// earlier retirement, never an error in the turn that used it. A
// directory that does not exist is left that way.
func (s Store) NoteUse(files []string, now time.Time) {
	if len(files) == 0 {
		return
	}
	if _, err := os.Stat(s.Dir); err != nil {
		return
	}
	_ = s.withLock(func() error {
		u := readUsage(s.Dir)
		for _, f := range files {
			if s.Has(f) {
				u.use(f, now)
			}
		}
		return u.write(s.Dir)
	})
}

// SaveResult is what a save did, for the tool to tell the model.
type SaveResult struct {
	Name string
	// Replaced is the note this save overwrote, if there was one. Its
	// previous text is in the archive.
	Replaced *Entry
	// Similar are other notes whose name and description share most of
	// their words with this one: likely the same fact, which the model
	// should merge rather than keep twice.
	Similar []Entry
	// Count is how many notes the directory holds now.
	Count int
	Stats IndexStats
}

// Save writes a note, replacing one of the same name. e.Name must already
// be normalized and e.Type one of Types. keep, when not nil, sets the
// note's exemption from retirement; nil keeps what a replaced note had,
// so a model updating a note it was asked to keep does not have to
// remember to say so again.
func (s Store) Save(e Entry, keep *bool, now time.Time) (SaveResult, error) {
	if _, ok := ParseType(string(e.Type)); !ok {
		return SaveResult{}, fmt.Errorf("type must be one of user, feedback, project, reference")
	}
	// The name becomes a path; only a normalized one is allowed to. The
	// tools normalize before calling, and this is the check that keeps
	// any other caller inside the directory.
	if n, err := NormalizeName(e.Name); err != nil || n != e.Name {
		return SaveResult{}, fmt.Errorf("%q is not a normalized note name", e.Name)
	}
	e.Description = oneLine(e.Description)
	e.Body = strings.TrimSpace(Sanitize(e.Body))
	if e.Body == "" {
		return SaveResult{}, errors.New("content is empty")
	}
	if len(e.Body) > MaxBodyBytes {
		return SaveResult{}, fmt.Errorf("content is %d bytes; a note holds at most %d. Keep the fact and its reason, and leave the rest in the repository", len(e.Body), MaxBodyBytes)
	}
	if e.Description == "" {
		e.Description = DescribeFrom(e.Body)
	}
	var res SaveResult
	err := s.withLock(func() error {
		existing, err := readEntries(s.Dir)
		if err != nil {
			return err
		}
		// Every file that is this note: the one with its exact name,
		// and any hand-written one whose name differs only in case or
		// spelling ("Build.md", "Build_Notes.md" for build-notes). Those
		// are adopted: archived whole and replaced by the saved note,
		// rather than left listed beside it. Case matters most: on a
		// disk that ignores it, "build.md" is "Build.md", and writing
		// the one would overwrite the other without archiving it.
		var exact *Entry
		var others []Entry
		for i := range existing {
			// The normalized name covers case as well as spelling: e.Name
			// is lower case, so "Build" normalizes to it.
			if existing[i].Name == e.Name {
				exact = &existing[i]
			} else if n, err := NormalizeName(existing[i].Name); err == nil && n == e.Name {
				others = append(others, existing[i])
			}
		}
		replaced := exact
		if replaced == nil && len(others) > 0 {
			replaced = &others[0]
		}
		if replaced == nil && len(existing) >= s.Limits.Entries {
			return fmt.Errorf("%s memory already holds %d notes, its limit. Merge notes that say the same thing, or forget ones that no longer help, then save again", s.Scope, len(existing))
		}
		e.Created, e.Modified = now, now
		if keep != nil {
			e.Keep = *keep
		}
		if replaced != nil {
			if !replaced.Created.IsZero() {
				e.Created = replaced.Created
			}
			if keep == nil {
				e.Keep = replaced.Keep
			}
		}
		if exact != nil && (exact.Body != e.Body || exact.Description != e.Description || exact.Type != e.Type) {
			// What the note said before goes to the archive: a save
			// that merges two notes into one, done badly, is the way a
			// memory loses a fact, and it should be a fact that can be
			// found again.
			if err := s.archiveCopy(e.File(), now); err != nil {
				return err
			}
		}
		// Moved before the new file is written, never after: on a disk
		// that ignores case the new file is the old one's name.
		for _, o := range others {
			if err := s.archiveFile(o.File(), now, true); err != nil {
				return err
			}
		}
		if err := writeAtomic(filepath.Join(s.Dir, e.File()), []byte(e.Format())); err != nil {
			return err
		}
		u := readUsage(s.Dir)
		for _, o := range others {
			delete(u.Entries, o.File())
		}
		u.use(e.File(), now)
		if err := u.write(s.Dir); err != nil {
			return err
		}
		// The hand-written lines that only pointed at an adopted file,
		// or at this one while it was untyped, would list it twice.
		for _, o := range others {
			if err := dropLegacyLine(s.Dir, o.File()); err != nil {
				return err
			}
		}
		if exact != nil && exact.Type == TypeLegacy {
			if err := dropLegacyLine(s.Dir, e.File()); err != nil {
				return err
			}
		}
		res.Name = e.Name
		res.Replaced = replaced
		res.Similar = similar(e, existing)
		res.Count = len(existing) - len(others)
		if exact == nil {
			res.Count++
		}
		res.Stats, err = s.regenerate(u)
		return err
	})
	return res, err
}

// archiveCopy copies a note's current file into the archive under a
// unique name, and keeps the archive within maxArchived.
func (s Store) archiveCopy(file string, now time.Time) error {
	return s.archiveFile(file, now, false)
}

// archiveFile puts file into the archive, moving it or copying it.
func (s Store) archiveFile(file string, now time.Time, move bool) error {
	dir, err := s.archiveDir(true)
	if err != nil {
		return err
	}
	dst := filepath.Join(dir, stem(file)+"~"+now.UTC().Format(archiveStamp)+".md")
	src := filepath.Join(s.Dir, file)
	if move {
		if err := os.Rename(src, dst); err != nil {
			return err
		}
	} else {
		content, cut, err := readRegular(src, maxIndexFileBytes)
		if err != nil {
			return err
		}
		if cut {
			return fmt.Errorf("%s is too large to archive", file)
		}
		if err := writeAtomic(dst, []byte(content)); err != nil {
			return err
		}
	}
	return s.pruneArchive(dir)
}

func (s Store) pruneArchive(dir string) error {
	files, err := noteFiles(dir)
	if err != nil || len(files) <= maxArchived {
		return err
	}
	type aged struct {
		file string
		at   time.Time
	}
	var list []aged
	for _, f := range files {
		_, at, ok := archivedName(f)
		if !ok {
			if info, err := os.Lstat(filepath.Join(dir, f)); err == nil {
				at = info.ModTime()
			}
		}
		list = append(list, aged{f, at})
	}
	sort.SliceStable(list, func(i, j int) bool { return list[i].at.Before(list[j].at) })
	for _, a := range list[:len(list)-maxArchived] {
		if err := os.Remove(filepath.Join(dir, a.file)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return nil
}

// Forget moves a note to the archive. Never a delete: a note forgotten by
// mistake, or retired too early, comes back with Restore.
func (s Store) Forget(name string, now time.Time) (string, error) {
	var file string
	err := s.withLock(func() error {
		var err error
		file, err = find(s.Dir, name)
		if err != nil {
			return err
		}
		if err := s.archiveFile(file, now, true); err != nil {
			return err
		}
		u := readUsage(s.Dir)
		delete(u.Entries, file)
		if err := u.write(s.Dir); err != nil {
			return err
		}
		if err := dropLegacyLine(s.Dir, file); err != nil {
			return err
		}
		_, err = s.regenerate(u)
		return err
	})
	return stem(file), err
}

// Restore brings the most recently archived copy of a note back.
func (s Store) Restore(name string, now time.Time) (string, error) {
	var restored string
	err := s.withLock(func() error {
		list, err := s.ArchivedNotes()
		if err != nil {
			return err
		}
		norm, nerr := NormalizeName(name)
		var pick *Archived
		for i := range list {
			n := list[i].Entry.Name
			if strings.EqualFold(n, strings.TrimSuffix(strings.TrimSpace(name), ".md")) || (nerr == nil && n == norm) {
				pick = &list[i]
				break
			}
		}
		if pick == nil {
			return ErrNotFound
		}
		file := pick.Entry.Name + ".md"
		// A name the store cannot hold as a note: "memory.md" is the
		// index itself on a disk that ignores case, and a Windows device
		// name cannot be created. Put into the archive by hand, never
		// by the store.
		if !isNoteName(file) || reservedNames[strings.ToLower(pick.Entry.Name)] || strings.ContainsAny(pick.Entry.Name, `/\`) {
			return fmt.Errorf("an archived copy named %q cannot be restored under that name; read it with memory_search and save its text under another name", pick.Entry.Name)
		}
		if _, err := os.Lstat(filepath.Join(s.Dir, file)); err == nil {
			return fmt.Errorf("a note named %s exists; forget it first, or save the archived text under another name", pick.Entry.Name)
		}
		existing, err := noteFiles(s.Dir)
		if err != nil {
			return err
		}
		if len(existing) >= s.Limits.Entries {
			return fmt.Errorf("%s memory already holds %d notes, its limit", s.Scope, len(existing))
		}
		if err := os.Rename(filepath.Join(s.Dir, archiveDirName, pick.File), filepath.Join(s.Dir, file)); err != nil {
			return err
		}
		u := readUsage(s.Dir)
		u.use(file, now)
		if err := u.write(s.Dir); err != nil {
			return err
		}
		restored = pick.Entry.Name
		_, err = s.regenerate(u)
		return err
	})
	return restored, err
}

// BeginSession is a conversation loading this directory's index for the
// first time. It counts the conversation, once, and retires what has gone
// unused: project and reference notes not marked keep, not read, saved
// or named for retireDays days and DefaultRetireSessions conversations.
// retireDays 0 retires nothing. A directory that does not exist yet is
// left that way.
func (s Store) BeginSession(sessionID string, now time.Time, retireDays int) ([]string, error) {
	if _, err := os.Stat(s.Dir); err != nil {
		return nil, nil
	}
	var retired []string
	err := s.withLock(func() error {
		u := readUsage(s.Dir)
		if !u.countSession(sessionID) {
			return nil
		}
		entries, err := readEntries(s.Dir)
		if err != nil {
			return err
		}
		live := map[string]bool{}
		for _, e := range entries {
			live[e.File()] = true
			m := u.mark(e.File(), now)
			if retireDays <= 0 || !e.Type.retires() || e.Keep {
				continue
			}
			// A clock set back makes every note look recently used,
			// which retires nothing; the reverse is the case to guard.
			idle := now.Sub(m.LastUsed)
			if idle < time.Duration(retireDays)*24*time.Hour || u.Sessions-m.LastSession < DefaultRetireSessions {
				continue
			}
			if err := s.archiveFile(e.File(), now, true); err != nil {
				continue
			}
			delete(live, e.File())
			u.noteRetired(e.Name, now)
			retired = append(retired, e.Name)
		}
		// Records of notes that are gone (deleted by hand, or by a file
		// tool) would otherwise accumulate forever.
		for f := range u.Entries {
			if !live[f] {
				delete(u.Entries, f)
			}
		}
		if err := u.write(s.Dir); err != nil {
			return err
		}
		s.removeStaleTemps()
		// Every time, not only after a retirement: a crash between a
		// note's write and the index's leaves MEMORY.md describing what
		// was there before, and this is where it is put right. A
		// regeneration that changes nothing writes nothing.
		_, err = s.regenerate(u)
		return err
	})
	return retired, err
}

// staleTemp is how old a temporary file has to be before it is taken for
// one a crash left behind rather than one another writer is filling.
const staleTemp = time.Hour

// removeStaleTemps deletes the temporary files a crashed write left, in
// the directory and its archive. Called holding the lock. Against the
// wall clock, not the caller's: a file's modification time is the
// wall clock's, and a clock passed in for retirement's sake (a test's, a
// skewed one) would take a writer's fresh file for a stale one.
func (s Store) removeStaleTemps() {
	now := time.Now()
	dirs := []string{s.Dir}
	if archive, err := s.archiveDir(false); err == nil && archive != "" {
		dirs = append(dirs, archive)
	}
	for _, dir := range dirs {
		list, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		for _, d := range list {
			if !strings.HasPrefix(d.Name(), ".tmp-") || !d.Type().IsRegular() {
				continue
			}
			if info, err := d.Info(); err == nil && now.Sub(info.ModTime()) > staleTemp {
				os.Remove(filepath.Join(dir, d.Name()))
			}
		}
	}
}

// Hit is one search result: a note, an archived copy of one, or a line of
// the person's own part of MEMORY.md.
type Hit struct {
	Entry    Entry
	Archived bool
	// IndexLine is set for a hit in MEMORY.md's own text, which is
	// searched because a long hand-written index is exactly what the
	// prompt's budget leaves out.
	IndexLine string
	Score     int
}

// Search finds what contains every word of query, case-insensitively:
// notes by name, description or text; then archived copies, marked as
// such, so a note retired too early can still be found; then lines of
// MEMORY.md outside the generated block.
func (s Store) Search(query string) ([]Hit, error) {
	words := strings.Fields(strings.ToLower(query))
	if len(words) == 0 {
		return nil, errors.New("query is empty")
	}
	score := func(hay, key string) int {
		hay, key = strings.ToLower(hay), strings.ToLower(key)
		total := 0
		for _, w := range words {
			n := strings.Count(hay, w)
			if n == 0 {
				return 0
			}
			total += n
			if strings.Contains(key, w) {
				total += 5
			}
		}
		return total
	}
	var hits []Hit
	live, err := s.Entries()
	if err != nil {
		return nil, err
	}
	for _, e := range live {
		if sc := score(e.Name+"\n"+e.Description+"\n"+e.Body, e.Name+" "+e.Description); sc > 0 {
			hits = append(hits, Hit{Entry: e, Score: sc})
		}
	}
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Score > hits[j].Score })
	archived, err := s.ArchivedNotes()
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var old []Hit
	for _, a := range archived {
		// The newest archived copy of each name only: older copies of a
		// note that was saved over many times say the same thing.
		if seen[a.Entry.Name] {
			continue
		}
		e := a.Entry
		if sc := score(e.Name+"\n"+e.Description+"\n"+e.Body, e.Name+" "+e.Description); sc > 0 {
			seen[e.Name] = true
			old = append(old, Hit{Entry: e, Archived: true, Score: sc})
		}
	}
	sort.SliceStable(old, func(i, j int) bool { return old[i].Score > old[j].Score })
	hits = append(hits, old...)
	_, rest := splitIndex(readIndexFile(s.Dir))
	for _, line := range strings.Split(rest, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if sc := score(line, ""); sc > 0 {
			hits = append(hits, Hit{IndexLine: Neutralize(strings.TrimSpace(line)), Score: sc})
		}
	}
	return hits, nil
}

// Usage is what /memory reports about one note.
type Usage struct {
	Uses           int
	LastUsed       time.Time
	SessionsUnused int
}

// UsageOf reports every note's use, the retirements on record (newest
// last), and how many conversations the directory has counted.
func (s Store) UsageOf() (map[string]Usage, []string, int) {
	u := readUsage(s.Dir)
	out := map[string]Usage{}
	for f, m := range u.Entries {
		out[stem(f)] = Usage{Uses: m.Uses, LastUsed: m.LastUsed, SessionsUnused: u.Sessions - m.LastSession}
	}
	var retired []string
	for _, r := range u.Retired {
		retired = append(retired, r.Name+" ("+r.At.Format("2006-01-02")+")")
	}
	return out, retired, u.Sessions
}

// minCitedName is the shortest note name that counts as cited when it
// appears in an answer. A one- or two-letter name is a word in every
// answer; a three-letter one (api, sql) can be one too, and a false
// match costs only a later retirement.
const minCitedName = 3

// Cited returns the names in names that text mentions as a whole word:
// the note's name with no letter, digit or hyphen on either side. This is
// how a note whose index line is all that is ever read of it shows that
// it was used: the policy asks the model to name a note it relied on.
// A false match only delays a retirement.
func Cited(text string, names []string) []string {
	if text == "" {
		return nil
	}
	lower := strings.ToLower(text)
	var out []string
	for _, n := range names {
		if len(n) < minCitedName || !strings.Contains(lower, n) {
			continue
		}
		re := regexp.MustCompile(`(^|[^a-z0-9-])` + regexp.QuoteMeta(n) + `($|[^a-z0-9-])`)
		if re.MatchString(lower) {
			out = append(out, n)
		}
	}
	return out
}

// similar picks the notes most like e by the words of their names and
// descriptions. Words, not meaning: it is a prompt to look, and the model
// decides.
func similar(e Entry, others []Entry) []Entry {
	mine := wordSet(e.Name + " " + e.Description)
	if len(mine) == 0 {
		return nil
	}
	type scored struct {
		e Entry
		j float64
	}
	var out []scored
	for _, o := range others {
		if o.Name == e.Name {
			continue
		}
		theirs := wordSet(o.Name + " " + o.Description)
		inter := 0
		for w := range mine {
			if theirs[w] {
				inter++
			}
		}
		union := len(mine) + len(theirs) - inter
		if union == 0 {
			continue
		}
		if j := float64(inter) / float64(union); j >= 0.5 {
			out = append(out, scored{o, j})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].j > out[j].j })
	var res []Entry
	for i := 0; i < len(out) && i < 3; i++ {
		res = append(res, out[i].e)
	}
	return res
}

var stopWords = map[string]bool{"the": true, "and": true, "for": true, "with": true, "that": true, "this": true, "not": true, "are": true, "use": true, "from": true}

func wordSet(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r > 127)
	}) {
		if len([]rune(w)) >= 3 && !stopWords[w] {
			out[w] = true
		}
	}
	return out
}
