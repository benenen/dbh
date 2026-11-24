package erd

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/benenen/dbh/internal/dbml"
)

const (
	DirName        = ".dbh"
	DefaultModel   = "schema"
	SourceDeclared = "declared"
	SourceInferred = "inferred"
)

var modelNamePattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.-]*$`)

func ValidModelName(name string) error {
	if !modelNamePattern.MatchString(name) || strings.HasSuffix(name, ".refs") {
		return fmt.Errorf("invalid model name %q", name)
	}
	return nil
}

// FindDir walks up from start to the first .dbh directory, stopping at the
// Git repository root. When none exists, create is where a new one belongs.
func FindDir(start string) (dir string, create string, err error) {
	start, err = filepath.Abs(start)
	if err != nil {
		return "", "", err
	}
	for d := start; ; {
		candidate := filepath.Join(d, DirName)
		if info, err := os.Stat(candidate); err == nil && info.IsDir() {
			return candidate, candidate, nil
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", "", err
		}
		if root, err := isGitRoot(d); err != nil {
			return "", "", err
		} else if root {
			return "", candidate, nil
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", filepath.Join(start, DirName), nil
		}
		d = parent
	}
}

const maxScanDepth = 8

var skipScanDirs = map[string]bool{
	"node_modules": true,
	"vendor":       true,
	"target":       true,
	"dist":         true,
	"build":        true,
	"out":          true,
	"bin":          true,
	"obj":          true,
	"logs":         true,
	"tmp":          true,
	"coverage":     true,
	"__pycache__":  true,
	"Pods":         true,
	"DerivedData":  true,
	"third_party":  true,
	"testdata":     true,
}

type ignoreRule struct {
	base     string // directory where the .gitignore lives
	pattern  string
	anchored bool
	negated  bool
}

func loadGitignore(dir string) []ignoreRule {
	data, err := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if err != nil {
		return nil
	}
	var rules []ignoreRule
	for _, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		negated := false
		if strings.HasPrefix(line, "!") {
			negated = true
			line = strings.TrimSpace(line[1:])
			if line == "" {
				continue
			}
		}
		line = strings.TrimSuffix(line, "/")
		if line == "" {
			continue
		}
		anchored := strings.HasPrefix(line, "/")
		line = strings.TrimPrefix(line, "/")
		line = strings.TrimPrefix(line, "**/")
		if strings.Contains(line, "/") {
			anchored = true
		}
		rules = append(rules, ignoreRule{
			base:     dir,
			pattern:  line,
			anchored: anchored,
			negated:  negated,
		})
	}
	return rules
}

func isIgnoredDir(rules []ignoreRule, fullPath, name string) bool {
	ignored := false
	for _, r := range rules {
		var matched bool
		if r.anchored {
			rel, err := filepath.Rel(r.base, fullPath)
			if err != nil {
				continue
			}
			rel = filepath.ToSlash(rel)
			matched, _ = path.Match(r.pattern, rel)
		} else {
			matched, _ = path.Match(r.pattern, name)
		}
		if matched {
			ignored = !r.negated
		}
	}
	return ignored
}

// ProjectRoot returns the nearest ancestor directory containing .git, or start
// itself when not inside a Git repository.
func ProjectRoot(start string) (string, error) {
	start, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for d := start; ; {
		if root, err := isGitRoot(d); err != nil {
			return "", err
		} else if root {
			return d, nil
		}
		parent := filepath.Dir(d)
		if parent == d {
			return start, nil
		}
		d = parent
	}
}

// isGitRoot reports whether dir is a Git work tree root: .git is a file (a
// worktree or submodule gitdir pointer) or a directory with HEAD. A stray .git
// directory without HEAD, which Git itself ignores, does not count.
func isGitRoot(dir string) (bool, error) {
	info, err := os.Stat(filepath.Join(dir, ".git"))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !info.IsDir() {
		return true, nil
	}
	if _, err := os.Stat(filepath.Join(dir, ".git", "HEAD")); err == nil {
		return true, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	return false, nil
}

// FindAllDirs scans the project root (the Git repository root when available,
// otherwise start) in parallel for every .dbh directory, pruning hidden
// directories, common build/dependency folders, .gitignore-matched directories,
// and trees deeper than maxScanDepth. Returns sorted absolute paths.
func FindAllDirs(start string) ([]string, error) {
	root, err := ProjectRoot(start)
	if err != nil {
		return nil, err
	}
	if _, err := os.ReadDir(root); err != nil {
		return nil, err
	}

	workers := runtime.NumCPU()
	if workers > 8 {
		workers = 8
	}
	if workers < 1 {
		workers = 1
	}

	var (
		mu       sync.Mutex
		wg       sync.WaitGroup
		dirs     []string
		firstErr error
		sem      = make(chan struct{}, workers)
	)

	var scan func(dir string, depth int, inherited []ignoreRule)
	scan = func(dir string, depth int, inherited []ignoreRule) {
		entries, err := os.ReadDir(dir)
		if err != nil {
			if errors.Is(err, os.ErrPermission) || errors.Is(err, os.ErrNotExist) {
				return
			}
			mu.Lock()
			if firstErr == nil {
				firstErr = err
			}
			mu.Unlock()
			return
		}
		rules := inherited
		for _, entry := range entries {
			if !entry.IsDir() && entry.Name() == ".gitignore" {
				if local := loadGitignore(dir); len(local) > 0 {
					combined := make([]ignoreRule, len(inherited)+len(local))
					copy(combined, inherited)
					copy(combined[len(inherited):], local)
					rules = combined
				}
				break
			}
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			name := entry.Name()
			if name == DirName {
				mu.Lock()
				dirs = append(dirs, filepath.Join(dir, name))
				mu.Unlock()
				continue
			}
			if depth >= maxScanDepth {
				continue
			}
			if strings.HasPrefix(name, ".") || skipScanDirs[name] {
				continue
			}
			child := filepath.Join(dir, name)
			if len(rules) > 0 && isIgnoredDir(rules, child, name) {
				continue
			}
			select {
			case sem <- struct{}{}:
				wg.Add(1)
				go func(p string, d int, r []ignoreRule) {
					defer func() {
						<-sem
						wg.Done()
					}()
					scan(p, d, r)
				}(child, depth+1, rules)
			default:
				scan(child, depth+1, rules)
			}
		}
	}

	scan(root, 0, nil)
	wg.Wait()
	if firstErr != nil {
		return nil, firstErr
	}
	sort.Strings(dirs)
	return dirs, nil
}

func ModelFiles(dir, model string) (main, refs string) {
	return filepath.Join(dir, model+".dbml"), filepath.Join(dir, model+".refs.dbml")
}

type Issue struct {
	File    string
	Line    int // 1-based; 0 when unknown
	Column  int
	Message string
}

func (i Issue) String() string {
	if i.File == "" {
		return i.Message
	}
	if i.Line == 0 {
		return fmt.Sprintf("%s: %s", DisplayPath(i.File), i.Message)
	}
	return fmt.Sprintf("%s:%d:%d: %s", DisplayPath(i.File), i.Line, i.Column, i.Message)
}

// DisplayPath shortens paths below the working directory.
func DisplayPath(path string) string {
	if wd, err := os.Getwd(); err == nil {
		if rel, err := filepath.Rel(wd, path); err == nil && len(rel) < len(path) {
			return rel
		}
	}
	return path
}

type Table struct {
	Schema  string // empty for the default schema
	Name    string
	Alias   string
	Note    string
	Columns []dbml.Column
	Indexes []dbml.Index
	Models  []string
	Dirs    []string // .dbh directories that define the table
	// Conflict is set when models define this table with different columns,
	// so the merged columns may not match any single database.
	Conflict bool
}

func (t Table) Display() string {
	if t.Schema == "" {
		return t.Name
	}
	return t.Schema + "." + t.Name
}

func (t Table) column(name string) (string, bool) {
	for _, c := range t.Columns {
		if c.Name == name {
			return c.Name, true
		}
	}
	for _, c := range t.Columns {
		if strings.EqualFold(c.Name, name) {
			return c.Name, true
		}
	}
	return "", false
}

// Relation is normalized so that "<" never appears: From holds the
// referencing (many) side of a ">" relation.
type Relation struct {
	Name        string
	From        string // table display name
	FromColumns []string
	Op          string // ">", "-" or "<>"
	To          string
	ToColumns   []string
	Condition   string
	Evidence    string // non-directive comments next to the Ref, such as source locations
	OnDelete    string
	OnUpdate    string
	Source      string
	File        string
	Line        int
}

func (r Relation) key() string {
	a := r.From + "(" + strings.Join(sortedCopy(r.FromColumns), ",") + ")"
	b := r.To + "(" + strings.Join(sortedCopy(r.ToColumns), ",") + ")"
	if a > b {
		a, b = b, a
	}
	if r.Condition != "" {
		return a + "|" + b + "|" + r.Condition
	}
	return a + "|" + b
}

func sortedCopy(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}

type Model struct {
	Name         string
	File         string
	RefsFile     string // empty when the model has no refs file
	Tables       []Table
	Relations    []Relation
	Errors       []Issue
	Warnings     []Issue
	refsFiles    map[string]bool
	tableByKey   map[string]int
	tableByAlias map[string]int
}

// ParseDBML parses a single DBML source string in memory into a Model.
func ParseDBML(name, file, source string) *Model {
	m := &Model{Name: name, File: file, refsFiles: map[string]bool{}}
	if db := m.parse(file, source, false); db != nil {
		m.addTables(db, name, "")
		m.addRelations(db.Refs, SourceDeclared, file, source)
	}
	return m
}

// FilterSource returns a shallow view of m containing only relations matching
// source (declared or inferred). When source is empty, m is returned as-is.
func (m *Model) FilterSource(source string) *Model {
	if source == "" {
		return m
	}
	filtered := make([]Relation, 0, len(m.Relations))
	for _, r := range m.Relations {
		if r.Source == source {
			filtered = append(filtered, r)
		}
	}
	cp := *m
	cp.Relations = filtered
	return &cp
}

func (m *Model) isRefsFile(file string) bool {
	if file == "" {
		return false
	}
	if file == m.RefsFile {
		return true
	}
	return m.refsFiles[file]
}

func (m *Model) Count(source string) int {
	n := 0
	for _, r := range m.Relations {
		if r.Source == source {
			n++
		}
	}
	return n
}

// Load reads .dbh/<model>.dbml and the optional .refs.dbml. Problems in
// the generated file are errors; inferred relations that duplicate a declared
// one are warnings, and ones that point at missing tables or columns are
// errors that queries may skip (see usable).
func Load(dir, name string) (*Model, error) {
	mainFile, refsFile := ModelFiles(dir, name)
	mainSource, err := os.ReadFile(mainFile)
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("no ERD model %q (looked for %s); run dbh erd pull NAME --save", name, DisplayPath(mainFile))
	}
	if err != nil {
		return nil, err
	}
	m := &Model{Name: name, File: mainFile, refsFiles: map[string]bool{}}
	mainDB := m.parse(mainFile, string(mainSource), false)
	if mainDB != nil {
		m.addTables(mainDB, modelLabel(dir, name), dir)
		m.addRelations(mainDB.Refs, SourceDeclared, mainFile, string(mainSource))
	}
	refsSource, err := os.ReadFile(refsFile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err == nil {
		m.RefsFile = refsFile
		m.refsFiles[refsFile] = true
		if refsDB := m.parse(refsFile, string(refsSource), true); refsDB != nil {
			m.addRelations(refsDB.Refs, SourceInferred, refsFile, string(refsSource))
		}
	}
	return m, nil
}

func modelLabel(dir, name string) string {
	parent := filepath.Base(filepath.Dir(dir))
	if parent == "" || parent == "." || parent == string(filepath.Separator) {
		return name
	}
	return parent + "/" + name
}

type modelEntry struct {
	dir  string
	name string
}

// LoadAll reads and merges models from dirs. When modelFilter is empty, every
// model (including standalone .refs.dbml files) in every directory is loaded;
// otherwise only models with that name are loaded. Tables with the same display
// name are merged across models, and inferred relations are resolved after all
// tables are loaded so cross-.dbh relations work.
func LoadAll(dirs []string, modelFilter string) (*Model, error) {
	if modelFilter != "" {
		if err := ValidModelName(modelFilter); err != nil {
			return nil, err
		}
	}
	var entries []modelEntry
	var standaloneRefs []modelEntry
	for _, dir := range dirs {
		if modelFilter != "" {
			mainFile, refsFile := ModelFiles(dir, modelFilter)
			if _, err := os.Stat(mainFile); err == nil {
				entries = append(entries, modelEntry{dir: dir, name: modelFilter})
			} else if !errors.Is(err, os.ErrNotExist) {
				return nil, err
			} else if _, err := os.Stat(refsFile); err == nil {
				standaloneRefs = append(standaloneRefs, modelEntry{dir: dir, name: modelFilter})
			} else if !errors.Is(err, os.ErrNotExist) {
				return nil, err
			}
			continue
		}
		names, err := List(dir)
		if err != nil {
			return nil, err
		}
		for _, name := range names {
			entries = append(entries, modelEntry{dir: dir, name: name})
		}
		refsOnly, err := ListRefsOnly(dir)
		if err != nil {
			return nil, err
		}
		for _, name := range refsOnly {
			standaloneRefs = append(standaloneRefs, modelEntry{dir: dir, name: name})
		}
	}
	if len(entries) == 0 && len(standaloneRefs) == 0 {
		if modelFilter != "" {
			if len(dirs) == 1 {
				mainFile, _ := ModelFiles(dirs[0], modelFilter)
				return nil, fmt.Errorf("no ERD model %q (looked for %s); run dbh erd pull NAME --save", modelFilter, DisplayPath(mainFile))
			}
			return nil, fmt.Errorf("no ERD model %q found in %d .dbh directories; run dbh erd pull NAME --save", modelFilter, len(dirs))
		}
		return nil, errors.New("no ERD models found in .dbh directories; run dbh erd pull NAME --save")
	}
	if len(entries) == 1 && len(standaloneRefs) == 0 {
		return Load(entries[0].dir, entries[0].name)
	}

	mergedName := modelFilter
	if mergedName == "" {
		if len(entries) > 0 {
			mergedName = entries[0].name
			for _, e := range entries[1:] {
				if e.name != mergedName {
					mergedName = "all"
					break
				}
			}
			if len(standaloneRefs) > 0 {
				for _, e := range standaloneRefs {
					if e.name != mergedName {
						mergedName = "all"
						break
					}
				}
			}
		} else {
			mergedName = standaloneRefs[0].name
			for _, e := range standaloneRefs[1:] {
				if e.name != mergedName {
					mergedName = "all"
					break
				}
			}
		}
	}
	m := &Model{Name: mergedName, refsFiles: map[string]bool{}}

	type parsedMain struct {
		file   string
		source string
		db     *dbml.Database
	}
	var mains []parsedMain
	var refsFiles []string

	// Phase 1: parse all main .dbml files and merge tables first so relations
	// (including cross-.dbh declared or inferred refs) can resolve all tables.
	for _, e := range entries {
		mainFile, refsFile := ModelFiles(e.dir, e.name)
		mainSource, err := os.ReadFile(mainFile)
		if err != nil {
			return nil, err
		}
		if m.File == "" {
			m.File = mainFile
		}
		if mainDB := m.parse(mainFile, string(mainSource), false); mainDB != nil {
			m.addTables(mainDB, modelLabel(e.dir, e.name), e.dir)
			mains = append(mains, parsedMain{file: mainFile, source: string(mainSource), db: mainDB})
		}
		if _, err := os.Stat(refsFile); err == nil {
			refsFiles = append(refsFiles, refsFile)
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	for _, e := range standaloneRefs {
		_, refsFile := ModelFiles(e.dir, e.name)
		refsFiles = append(refsFiles, refsFile)
	}
	for _, t := range m.Tables {
		if t.Conflict {
			msg, _ := m.TableConflict(t.Display())
			m.Warnings = append(m.Warnings, Issue{Message: msg})
		}
	}
	for _, pm := range mains {
		m.addRelations(pm.db.Refs, SourceDeclared, pm.file, pm.source)
	}

	// Phase 2: parse and resolve all .refs.dbml files against the merged tables.
	for _, refsFile := range refsFiles {
		refsSource, err := os.ReadFile(refsFile)
		if err != nil {
			return nil, err
		}
		if m.RefsFile == "" {
			m.RefsFile = refsFile
		}
		m.refsFiles[refsFile] = true
		if refsDB := m.parse(refsFile, string(refsSource), true); refsDB != nil {
			m.addRelations(refsDB.Refs, SourceInferred, refsFile, string(refsSource))
		}
	}
	return m, nil
}

// ValidateModelRefs validates dir/<name>.dbml and its optional .refs.dbml using
// the combined table set across allDirs so cross-.dbh inferred relations do not
// produce false unknown-table errors after pull --save.
func ValidateModelRefs(dir, name string, allDirs []string) (*Model, error) {
	if len(allDirs) <= 1 {
		return Load(dir, name)
	}
	mainFile, refsFile := ModelFiles(dir, name)
	mainSource, err := os.ReadFile(mainFile)
	if err != nil {
		return nil, err
	}
	target := &Model{Name: name, File: mainFile, refsFiles: map[string]bool{}}
	if mainDB := target.parse(mainFile, string(mainSource), false); mainDB != nil {
		target.addTables(mainDB, modelLabel(dir, name), dir)
		target.addRelations(mainDB.Refs, SourceDeclared, mainFile, string(mainSource))
	}
	refsSource, err := os.ReadFile(refsFile)
	if errors.Is(err, os.ErrNotExist) {
		return target, nil
	}
	if err != nil {
		return nil, err
	}

	merged := &Model{Name: "all", refsFiles: map[string]bool{}}
	type parsedMain struct {
		file   string
		source string
		db     *dbml.Database
	}
	var mains []parsedMain
	for _, d := range allDirs {
		names, err := List(d)
		if err != nil {
			return nil, err
		}
		for _, n := range names {
			mf, _ := ModelFiles(d, n)
			src, err := os.ReadFile(mf)
			if err != nil {
				continue
			}
			if db := merged.parse(mf, string(src), false); db != nil {
				merged.addTables(db, modelLabel(d, n), d)
				mains = append(mains, parsedMain{file: mf, source: string(src), db: db})
			}
		}
	}
	for _, pm := range mains {
		merged.addRelations(pm.db.Refs, SourceDeclared, pm.file, pm.source)
	}
	probe := &Model{
		Name:      name,
		File:      mainFile,
		RefsFile:  refsFile,
		Tables:    merged.Tables,
		Relations: append([]Relation(nil), merged.Relations...),
		refsFiles: map[string]bool{refsFile: true},
	}
	probe.rebuildTableIndex()
	if refsDB := probe.parse(refsFile, string(refsSource), true); refsDB != nil {
		probe.addRelations(refsDB.Refs, SourceInferred, refsFile, string(refsSource))
	}
	target.RefsFile = refsFile
	target.refsFiles[refsFile] = true
	target.Errors = append(target.Errors, probe.Errors...)
	target.Warnings = append(target.Warnings, probe.Warnings...)
	return target, nil
}

// StatusRows returns one status row per model file (including standalone
// .refs.dbml files) across dirs. When multiple .dbh directories exist, each
// model's .refs.dbml is validated against the combined table set so cross-.dbh
// inferred relations are counted and marked ok.
func StatusRows(dirs []string, modelFilter string) (Rows, error) {
	if modelFilter != "" {
		if err := ValidModelName(modelFilter); err != nil {
			return Rows{}, err
		}
	}
	type item struct {
		name     string
		mainFile string
		refsFile string
		hasMain  bool
		hasRefs  bool
		tables   int
		declared int
		inferred int
		hasError bool
		hasWarn  bool
	}
	var items []item
	merged := &Model{Name: "all", refsFiles: map[string]bool{}}
	type parsedMain struct {
		file   string
		source string
		db     *dbml.Database
	}
	var mains []parsedMain

	for _, dir := range dirs {
		names, err := List(dir)
		if err != nil {
			return Rows{}, err
		}
		for _, name := range names {
			if modelFilter != "" && name != modelFilter {
				continue
			}
			mainFile, refsFile := ModelFiles(dir, name)
			it := item{name: name, mainFile: mainFile, refsFile: refsFile, hasMain: true}
			if _, err := os.Stat(refsFile); err == nil {
				it.hasRefs = true
			}
			mainSource, err := os.ReadFile(mainFile)
			if err != nil {
				it.hasError = true
				items = append(items, it)
				continue
			}
			local := &Model{Name: name, File: mainFile, refsFiles: map[string]bool{}}
			if mainDB := local.parse(mainFile, string(mainSource), false); mainDB != nil {
				label := modelLabel(dir, name)
				local.addTables(mainDB, label, dir)
				local.addRelations(mainDB.Refs, SourceDeclared, mainFile, string(mainSource))
				merged.addTables(mainDB, label, dir)
				mains = append(mains, parsedMain{file: mainFile, source: string(mainSource), db: mainDB})
			}
			it.tables = len(local.Tables)
			it.declared = local.Count(SourceDeclared)
			it.hasError = len(local.Errors) > 0
			it.hasWarn = len(local.Warnings) > 0
			items = append(items, it)
		}
		refsOnly, err := ListRefsOnly(dir)
		if err != nil {
			return Rows{}, err
		}
		for _, name := range refsOnly {
			if modelFilter != "" && name != modelFilter {
				continue
			}
			_, refsFile := ModelFiles(dir, name)
			items = append(items, item{name: name, refsFile: refsFile, hasRefs: true})
		}
	}
	for _, pm := range mains {
		merged.addRelations(pm.db.Refs, SourceDeclared, pm.file, pm.source)
	}

	rows := Rows{Columns: []string{"model", "file", "refs_file", "tables", "declared", "inferred", "state"}}
	for i := range items {
		it := &items[i]
		mainDisplay := ""
		if it.hasMain {
			mainDisplay = DisplayPath(it.mainFile)
		}
		refsDisplay := ""
		if it.hasRefs {
			refsDisplay = DisplayPath(it.refsFile)
			refsSource, err := os.ReadFile(it.refsFile)
			if err != nil {
				it.hasError = true
			} else {
				probe := &Model{
					Name:      it.name,
					RefsFile:  it.refsFile,
					Tables:    merged.Tables,
					Relations: append([]Relation(nil), merged.Relations...),
					refsFiles: map[string]bool{it.refsFile: true},
				}
				probe.rebuildTableIndex()
				if refsDB := probe.parse(it.refsFile, string(refsSource), true); refsDB != nil {
					probe.addRelations(refsDB.Refs, SourceInferred, it.refsFile, string(refsSource))
				}
				it.inferred = probe.Count(SourceInferred)
				if len(probe.Errors) > 0 {
					it.hasError = true
				}
				if len(probe.Warnings) > 0 {
					it.hasWarn = true
				}
			}
		}
		state := "ok"
		switch {
		case it.hasError:
			state = "invalid"
		case it.hasWarn:
			state = "warning"
		}
		rows.Values = append(rows.Values, []any{
			it.name, mainDisplay, refsDisplay,
			it.tables, it.declared, it.inferred, state,
		})
	}
	return rows, nil
}

// Usable reports whether queries can run: only invalid inferred relations,
// which were already dropped, may remain as errors.
func (m *Model) Usable() error {
	for _, issue := range m.Errors {
		if !m.isRefsFile(issue.File) || !strings.HasPrefix(issue.Message, "relation ") {
			return fmt.Errorf("invalid ERD model %q:\n%s", m.Name, issue)
		}
	}
	return nil
}

func (m *Model) parse(file, source string, refsOnly bool) *dbml.Database {
	before := len(m.Errors)
	db, errs := dbml.Parse(source, dbml.ParseOptions{RefsOnly: refsOnly})
	for _, e := range errs {
		m.Errors = append(m.Errors, issueAt(file, e.Position, e.Message))
	}
	if len(m.Errors) > before {
		return nil
	}
	return db
}

func issueAt(file string, pos dbml.Position, message string) Issue {
	return Issue{File: file, Line: pos.Line, Column: pos.Column, Message: message}
}

func (m *Model) rebuildTableIndex() {
	m.tableByKey = make(map[string]int, len(m.Tables))
	m.tableByAlias = make(map[string]int, len(m.Tables))
	for i, t := range m.Tables {
		m.tableByKey[t.Schema+"\x00"+t.Name] = i
		if t.Schema == "" && t.Alias != "" {
			if _, exists := m.tableByAlias[t.Alias]; !exists {
				m.tableByAlias[t.Alias] = i
			}
		}
	}
}

func indexKey(idx dbml.Index) string {
	cols := make([]string, len(idx.Columns))
	for i, c := range idx.Columns {
		cols[i] = c.Type + ":" + c.Value
	}
	name := ""
	if idx.Name != nil {
		name = *idx.Name
	}
	return fmt.Sprintf("%s|%t|%t|%s", name, idx.PK, idx.Unique, strings.Join(cols, ","))
}

func appendUniqueString(list []string, val string) []string {
	if val == "" {
		return list
	}
	for _, existing := range list {
		if existing == val {
			return list
		}
	}
	return append(list, val)
}

func (m *Model) addTables(db *dbml.Database, sourceLabel, dir string) {
	if m.tableByKey == nil {
		m.rebuildTableIndex()
	}
	for _, t := range db.Tables {
		table := Table{
			Schema:  schemaName(t.SchemaName),
			Name:    t.Name,
			Columns: append([]dbml.Column(nil), t.Fields...),
			Indexes: append([]dbml.Index(nil), t.Indexes...),
		}
		if sourceLabel != "" {
			table.Models = []string{sourceLabel}
		}
		if dir != "" {
			table.Dirs = []string{dir}
		}
		if t.Alias != nil {
			table.Alias = *t.Alias
		}
		if t.Note != nil {
			table.Note = t.Note.Value
		}
		key := table.Schema + "\x00" + table.Name
		if idx, ok := m.tableByKey[key]; ok {
			existing := &m.Tables[idx]
			if !existing.Conflict && !sameColumns(existing.Columns, table.Columns) {
				existing.Conflict = true
			}
			for _, col := range table.Columns {
				if _, exists := existing.column(col.Name); !exists {
					existing.Columns = append(existing.Columns, col)
				}
			}
			if len(table.Indexes) > 0 {
				seenIdx := make(map[string]bool, len(existing.Indexes))
				for _, ei := range existing.Indexes {
					seenIdx[indexKey(ei)] = true
				}
				for _, ni := range table.Indexes {
					if k := indexKey(ni); !seenIdx[k] {
						seenIdx[k] = true
						existing.Indexes = append(existing.Indexes, ni)
					}
				}
			}
			if existing.Alias == "" && table.Alias != "" {
				existing.Alias = table.Alias
				if existing.Schema == "" {
					if _, exists := m.tableByAlias[existing.Alias]; !exists {
						m.tableByAlias[existing.Alias] = idx
					}
				}
			}
			if existing.Note == "" && table.Note != "" {
				existing.Note = table.Note
			}
			existing.Models = appendUniqueString(existing.Models, sourceLabel)
			existing.Dirs = appendUniqueString(existing.Dirs, dir)
			continue
		}
		newIdx := len(m.Tables)
		m.tableByKey[key] = newIdx
		if table.Schema == "" && table.Alias != "" {
			if _, exists := m.tableByAlias[table.Alias]; !exists {
				m.tableByAlias[table.Alias] = newIdx
			}
		}
		m.Tables = append(m.Tables, table)
	}
}

// sameColumns reports whether a and b have the same column names and types.
func sameColumns(a, b []dbml.Column) bool {
	if len(a) != len(b) {
		return false
	}
	types := make(map[string]string, len(a))
	for _, c := range a {
		types[c.Name] = columnType(c)
	}
	for _, c := range b {
		if t, ok := types[c.Name]; !ok || t != columnType(c) {
			return false
		}
	}
	return true
}

// TableConflict returns a warning when the named table was merged from models
// that define it with different columns.
func (m *Model) TableConflict(name string) (string, bool) {
	idx, ok := m.tableIndex(name)
	if !ok || !m.Tables[idx].Conflict {
		return "", false
	}
	t := m.Tables[idx]
	return fmt.Sprintf("table %s has different columns in models %s; columns are merged, use --model to read one", t.Display(), strings.Join(t.Models, ", ")), true
}

// ForeignTable returns a warning when the named table is not defined in
// localDir, the .dbh directory of the current project, but only in other
// projects' .dbh directories merged into m. It is silent when localDir is
// empty or the table has no recorded directory.
func (m *Model) ForeignTable(name, localDir string) (string, bool) {
	idx, ok := m.tableIndex(name)
	if localDir == "" || !ok || len(m.Tables[idx].Dirs) == 0 {
		return "", false
	}
	t := m.Tables[idx]
	for _, d := range t.Dirs {
		if d == localDir {
			return "", false
		}
	}
	return fmt.Sprintf("table %s is only in another project's models %s, not in %s", t.Display(), strings.Join(t.Models, ", "), DisplayPath(localDir)), true
}

func (m *Model) tableIndex(name string) (int, bool) {
	for i, t := range m.Tables {
		if t.Display() == name {
			return i, true
		}
	}
	return 0, false
}

func schemaName(schema *string) string {
	if schema == nil || *schema == "public" {
		return ""
	}
	return *schema
}

// resolve finds the table an endpoint names, by name or alias.
func (m *Model) resolve(schema *string, name string) (*Table, bool) {
	if m.tableByKey == nil {
		m.rebuildTableIndex()
	}
	want := schemaName(schema)
	if idx, ok := m.tableByKey[want+"\x00"+name]; ok {
		return &m.Tables[idx], true
	}
	if schema == nil {
		if idx, ok := m.tableByAlias[name]; ok {
			return &m.Tables[idx], true
		}
	}
	return nil, false
}

func (m *Model) addRelations(refs []dbml.Ref, source, file, rawSource string) {
	lines := strings.Split(rawSource, "\n")
	seen := map[string]string{}
	for _, r := range m.Relations {
		seen[r.key()] = r.Source
	}
	for _, ref := range refs {
		if len(ref.Endpoints) != 2 {
			continue
		}
		left, right := ref.Endpoints[0], ref.Endpoints[1]
		line := left.Line // 1-based line
		condition, evidence := extractRefComments(lines, line)
		if condition == "" && evidence == "" && ref.Line > 0 && ref.Line != line {
			condition, evidence = extractRefComments(lines, ref.Line)
		}
		rel := Relation{Source: source, File: file, Line: line, Condition: condition, Evidence: evidence}
		if ref.Name != nil {
			rel.Name = *ref.Name
		}
		if ref.OnDelete != nil {
			rel.OnDelete = *ref.OnDelete
		}
		if ref.OnUpdate != nil {
			rel.OnUpdate = *ref.OnUpdate
		}
		var problem string
		var leftTable, rightTable *Table
		var leftColumns, rightColumns []string
		leftTable, leftColumns, problem = m.endpoint(left)
		if problem == "" {
			rightTable, rightColumns, problem = m.endpoint(right)
		}
		if problem != "" {
			m.Errors = append(m.Errors, Issue{File: file, Line: line, Column: 1, Message: "relation " + problem})
			continue
		}
		rel.From, rel.FromColumns = leftTable.Display(), leftColumns
		rel.To, rel.ToColumns = rightTable.Display(), rightColumns
		switch {
		case left.Relation == "*" && right.Relation == "1":
			rel.Op = ">"
		case left.Relation == "1" && right.Relation == "*":
			rel.Op = ">"
			rel.From, rel.To = rel.To, rel.From
			rel.FromColumns, rel.ToColumns = rel.ToColumns, rel.FromColumns
		case left.Relation == "*" && right.Relation == "*":
			rel.Op = "<>"
		default:
			rel.Op = "-"
		}
		key := rel.key()
		if previous, ok := seen[key]; ok {
			if source == SourceInferred && previous == SourceDeclared {
				m.Warnings = append(m.Warnings, Issue{File: file, Line: line, Column: 1,
					Message: fmt.Sprintf("relation %s is already declared in the database; remove it from the refs file", rel.describe())})
			}
			continue
		}
		seen[key] = source
		m.Relations = append(m.Relations, rel)
	}
}

func (m *Model) endpoint(ep dbml.RefEndpoint) (*Table, []string, string) {
	table, ok := m.resolve(ep.SchemaName, ep.TableName)
	if !ok {
		name := ep.TableName
		if ep.SchemaName != nil {
			name = *ep.SchemaName + "." + name
		}
		return nil, nil, fmt.Sprintf("refers to unknown table %q", name)
	}
	columns := make([]string, len(ep.FieldNames))
	for i, field := range ep.FieldNames {
		column, ok := table.column(field)
		if !ok {
			return nil, nil, fmt.Sprintf("refers to unknown column %q", table.Display()+"."+field)
		}
		columns[i] = column
	}
	return table, columns, ""
}

func (r Relation) describe() string {
	base := fmt.Sprintf("%s %s %s", endpointText(r.From, r.FromColumns), r.Op, endpointText(r.To, r.ToColumns))
	if r.Condition != "" {
		return base + " (when " + r.Condition + ")"
	}
	return base
}

// extractRefComments reads the contiguous comment block immediately above a
// Ref line and its trailing comment. `// when: <expr>` directives form the
// condition. `// evidence: <text>` directives form the evidence; without any
// such directive, the other comments are the evidence.
func extractRefComments(lines []string, line int) (condition, evidence string) {
	if line < 1 || line > len(lines) {
		return "", ""
	}
	var comments []string
	for idx := line - 2; idx >= 0; idx-- {
		trimmed := strings.TrimSpace(lines[idx])
		if !strings.HasPrefix(trimmed, "//") {
			break
		}
		comments = append(comments, trimmed[2:])
	}
	for i, j := 0, len(comments)-1; i < j; i, j = i+1, j-1 {
		comments[i], comments[j] = comments[j], comments[i]
	}
	if trailing := trailingLineComment(lines[line-1]); trailing != "" {
		comments = append(comments, trailing)
	}
	var conds, tagged, notes []string
	for _, c := range comments {
		if cond := parseWhenComment(c); cond != "" {
			conds = append(conds, cond)
		} else if ev, ok := parseCommentDirective(c, "evidence:"); ok {
			if ev != "" {
				tagged = append(tagged, ev)
			}
		} else if note := strings.TrimSpace(c); note != "" {
			notes = append(notes, note)
		}
	}
	if len(tagged) > 0 {
		notes = tagged
	}
	return strings.Join(conds, " AND "), strings.Join(notes, "; ")
}

// parseCommentDirective reports whether comment starts with prefix (case
// insensitive) and returns the trimmed rest.
func parseCommentDirective(comment, prefix string) (string, bool) {
	body := strings.TrimSpace(comment)
	if len(body) < len(prefix) || !strings.EqualFold(body[:len(prefix)], prefix) {
		return "", false
	}
	return strings.TrimSpace(body[len(prefix):]), true
}

func parseWhenComment(comment string) string {
	body := strings.TrimSpace(comment)
	if len(body) < 5 || !strings.EqualFold(body[:5], "when:") {
		return ""
	}
	return strings.TrimSpace(body[5:])
}

func trailingLineComment(line string) string {
	var quote byte
	for i := 0; i < len(line); i++ {
		ch := line[i]
		if quote != 0 {
			if ch == '\\' && i+1 < len(line) {
				i++
				continue
			}
			if ch == quote {
				quote = 0
			}
			continue
		}
		switch ch {
		case '\'', '"', '`':
			quote = ch
		case '/':
			if i+1 < len(line) && line[i+1] == '/' {
				return line[i+2:]
			}
		}
	}
	return ""
}

func endpointText(table string, columns []string) string {
	if len(columns) == 1 {
		return table + "." + columns[0]
	}
	return table + ".(" + strings.Join(columns, ", ") + ")"
}

// table resolves user input: an exact display name, or a bare table name that
// matches a single schema.
func (m *Model) Lookup(name string) (string, error) {
	var matches []string
	for _, t := range m.Tables {
		if t.Display() == name {
			return t.Display(), nil
		}
	}
	for _, t := range m.Tables {
		if strings.EqualFold(t.Display(), name) || strings.EqualFold(t.Name, name) {
			matches = append(matches, t.Display())
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("table %q is not in ERD model %q", name, m.Name)
	case 1:
		return matches[0], nil
	}
	return "", fmt.Errorf("table %q is ambiguous: %s", name, strings.Join(matches, ", "))
}

// edge is a relation seen from one of its tables.
type edge struct {
	Other    string
	Relation *Relation
}

// adjacency treats relations as undirected; declared edges sort first, then
// by the other table, so traversal order is stable.
func (m *Model) adjacency(declaredOnly bool) map[string][]edge {
	source := ""
	if declaredOnly {
		source = SourceDeclared
	}
	return m.adjacencyBySource(source)
}

func (m *Model) adjacencyBySource(source string) map[string][]edge {
	graph := map[string][]edge{}
	for i := range m.Relations {
		r := &m.Relations[i]
		if source != "" && r.Source != source {
			continue
		}
		graph[r.From] = append(graph[r.From], edge{Other: r.To, Relation: r})
		if r.To != r.From {
			graph[r.To] = append(graph[r.To], edge{Other: r.From, Relation: r})
		}
	}
	for _, edges := range graph {
		sort.SliceStable(edges, func(i, j int) bool {
			if a, b := edges[i].Relation.Source == SourceDeclared, edges[j].Relation.Source == SourceDeclared; a != b {
				return a
			}
			if edges[i].Other != edges[j].Other {
				return edges[i].Other < edges[j].Other
			}
			return edges[i].Relation.key() < edges[j].Relation.key()
		})
	}
	return graph
}

// Neighborhood returns the tables within depth hops of the given ones.
func (m *Model) Neighborhood(tables []string, depth int) map[string]bool {
	return m.NeighborhoodFiltered(tables, depth, "")
}

// NeighborhoodFiltered returns the tables within depth hops of the given ones,
// optionally restricting traversal to relations of the given source.
func (m *Model) NeighborhoodFiltered(tables []string, depth int, source string) map[string]bool {
	graph := m.adjacencyBySource(source)
	seen := map[string]bool{}
	frontier := tables
	for _, t := range tables {
		seen[t] = true
	}
	for step := 0; step < depth; step++ {
		var next []string
		for _, t := range frontier {
			for _, e := range graph[t] {
				if !seen[e.Other] {
					seen[e.Other] = true
					next = append(next, e.Other)
				}
			}
		}
		frontier = next
	}
	return seen
}

type Step struct {
	From, To               string
	FromColumns, ToColumns []string
	Relation               *Relation
}

func makeStep(from, to string, r *Relation) Step {
	step := Step{From: from, To: to, Relation: r}
	if r.From == from && r.To == to {
		step.FromColumns, step.ToColumns = r.FromColumns, r.ToColumns
	} else {
		step.FromColumns, step.ToColumns = r.ToColumns, r.FromColumns
	}
	return step
}

// Path finds a shortest join path with breadth-first search.
func (m *Model) Path(from, to string, declaredOnly bool) ([]Step, error) {
	if from == to {
		return nil, fmt.Errorf("FROM and TO are the same table")
	}
	graph := m.adjacency(declaredOnly)
	previous := map[string]edge{}
	visited := map[string]bool{from: true}
	queue := []string{from}
	for len(queue) > 0 && !visited[to] {
		current := queue[0]
		queue = queue[1:]
		for _, e := range graph[current] {
			if visited[e.Other] {
				continue
			}
			visited[e.Other] = true
			previous[e.Other] = edge{Other: current, Relation: e.Relation}
			queue = append(queue, e.Other)
		}
	}
	if !visited[to] {
		return nil, fmt.Errorf("no relation path from %s to %s", from, to)
	}
	var steps []Step
	for t := to; t != from; {
		e := previous[t]
		steps = append([]Step{makeStep(e.Other, t, e.Relation)}, steps...)
		t = e.Other
	}
	return steps, nil
}

// AllPaths finds up to limit shortest join paths (including parallel foreign
// keys and conditional relations between the same pair of tables). When limit
// is <= 0, a default cap of 20 paths is used.
func (m *Model) AllPaths(from, to string, declaredOnly bool, limit int) ([][]Step, error) {
	if from == to {
		return nil, fmt.Errorf("FROM and TO are the same table")
	}
	if limit <= 0 {
		limit = 20
	}
	graph := m.adjacency(declaredOnly)
	dist := map[string]int{from: 0}
	parents := map[string][]edge{}
	queue := []string{from}
	targetDist := -1
	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]
		d := dist[current]
		if targetDist >= 0 && d >= targetDist {
			continue
		}
		for _, e := range graph[current] {
			nextDist, seen := dist[e.Other]
			if !seen {
				dist[e.Other] = d + 1
				parents[e.Other] = append(parents[e.Other], edge{Other: current, Relation: e.Relation})
				if e.Other == to {
					targetDist = d + 1
				} else {
					queue = append(queue, e.Other)
				}
			} else if nextDist == d+1 {
				parents[e.Other] = append(parents[e.Other], edge{Other: current, Relation: e.Relation})
			}
		}
	}
	if targetDist < 0 {
		return nil, fmt.Errorf("no relation path from %s to %s", from, to)
	}

	var results [][]Step
	var backtrack func(node string, rev []Step)
	backtrack = func(node string, rev []Step) {
		if len(results) >= limit {
			return
		}
		if node == from {
			path := make([]Step, len(rev))
			for i := range rev {
				path[i] = rev[len(rev)-1-i]
			}
			results = append(results, path)
			return
		}
		for _, p := range parents[node] {
			backtrack(p.Other, append(rev, makeStep(p.Other, node, p.Relation)))
			if len(results) >= limit {
				return
			}
		}
	}
	backtrack(to, nil)
	return results, nil
}

// List returns the model names in dir (those with a <name>.dbml file), sorted.
func List(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		name, ok := strings.CutSuffix(e.Name(), ".dbml")
		if !ok || e.IsDir() || strings.HasSuffix(name, ".refs") || ValidModelName(name) != nil {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names, nil
}

// ListRefsOnly returns model names in dir that have a <name>.refs.dbml file
// without a matching <name>.dbml file (e.g. cross-service reference files).
func ListRefsOnly(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	hasMain := map[string]bool{}
	var candidates []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if name, ok := strings.CutSuffix(e.Name(), ".refs.dbml"); ok {
			if ValidModelName(name) == nil {
				candidates = append(candidates, name)
			}
			continue
		}
		if name, ok := strings.CutSuffix(e.Name(), ".dbml"); ok {
			hasMain[name] = true
		}
	}
	var names []string
	for _, name := range candidates {
		if !hasMain[name] {
			names = append(names, name)
		}
	}
	sort.Strings(names)
	return names, nil
}
