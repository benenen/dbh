package erd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/mevdschee/dbml-tools/interpreter"
	"github.com/mevdschee/dbml-tools/lexer"
	"github.com/mevdschee/dbml-tools/parser"
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
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return "", candidate, nil
		}
		parent := filepath.Dir(d)
		if parent == d {
			return "", filepath.Join(start, DirName), nil
		}
		d = parent
	}
}

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

// ProjectRoot returns the nearest ancestor directory containing .git, or start
// itself when not inside a Git repository.
func ProjectRoot(start string) (string, error) {
	start, err := filepath.Abs(start)
	if err != nil {
		return "", err
	}
	for d := start; ; {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d, nil
		} else if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parent := filepath.Dir(d)
		if parent == d {
			return start, nil
		}
		d = parent
	}
}

// FindAllDirs scans the project root (the Git repository root when available,
// otherwise start) in parallel for every .dbh directory, pruning hidden
// directories and common build/dependency folders. Returns sorted absolute paths.
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

	var scan func(dir string)
	scan = func(dir string) {
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
			if strings.HasPrefix(name, ".") || skipScanDirs[name] {
				continue
			}
			child := filepath.Join(dir, name)
			select {
			case sem <- struct{}{}:
				wg.Add(1)
				go func(p string) {
					defer func() {
						<-sem
						wg.Done()
					}()
					scan(p)
				}(child)
			default:
				scan(child)
			}
		}
	}

	scan(root)
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
	Columns []interpreter.Column
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
	return a + "|" + b
}

func sortedCopy(values []string) []string {
	out := append([]string(nil), values...)
	sort.Strings(out)
	return out
}

type Model struct {
	Name      string
	File      string
	RefsFile  string // empty when the model has no refs file
	Tables    []Table
	Relations []Relation
	Errors    []Issue
	Warnings  []Issue
	refsFiles map[string]bool
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
		m.addTables(mainDB)
		m.addRelations(mainDB.Refs, SourceDeclared, mainFile)
	}
	refsSource, err := os.ReadFile(refsFile)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if err == nil {
		m.RefsFile = refsFile
		m.refsFiles[refsFile] = true
		if refsDB := m.parse(refsFile, string(refsSource), true); refsDB != nil {
			m.addRelations(refsDB.Refs, SourceInferred, refsFile)
		}
	}
	return m, nil
}

type modelEntry struct {
	dir  string
	name string
}

// LoadAll reads and merges models from dirs. When modelFilter is empty, every
// model in every directory is loaded; otherwise only models with that name are
// loaded. Tables with the same display name are merged across models, and
// inferred relations are resolved after all tables are loaded so cross-.dbh
// relations work.
func LoadAll(dirs []string, modelFilter string) (*Model, error) {
	if modelFilter != "" {
		if err := ValidModelName(modelFilter); err != nil {
			return nil, err
		}
	}
	var entries []modelEntry
	for _, dir := range dirs {
		if modelFilter != "" {
			mainFile, _ := ModelFiles(dir, modelFilter)
			if _, err := os.Stat(mainFile); err == nil {
				entries = append(entries, modelEntry{dir: dir, name: modelFilter})
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
	}
	if len(entries) == 0 {
		if modelFilter != "" {
			if len(dirs) == 1 {
				mainFile, _ := ModelFiles(dirs[0], modelFilter)
				return nil, fmt.Errorf("no ERD model %q (looked for %s); run dbh erd pull NAME --save", modelFilter, DisplayPath(mainFile))
			}
			return nil, fmt.Errorf("no ERD model %q found in %d .dbh directories; run dbh erd pull NAME --save", modelFilter, len(dirs))
		}
		return nil, errors.New("no ERD models found in .dbh directories; run dbh erd pull NAME --save")
	}
	if len(entries) == 1 {
		return Load(entries[0].dir, entries[0].name)
	}

	mergedName := modelFilter
	if mergedName == "" {
		mergedName = entries[0].name
		for _, e := range entries[1:] {
			if e.name != mergedName {
				mergedName = "all"
				break
			}
		}
	}
	m := &Model{Name: mergedName, refsFiles: map[string]bool{}}

	type parsedMain struct {
		file string
		db   *interpreter.Database
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
			m.addTables(mainDB)
			mains = append(mains, parsedMain{file: mainFile, db: mainDB})
		}
		if _, err := os.Stat(refsFile); err == nil {
			refsFiles = append(refsFiles, refsFile)
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
	}
	for _, pm := range mains {
		m.addRelations(pm.db.Refs, SourceDeclared, pm.file)
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
			m.addRelations(refsDB.Refs, SourceInferred, refsFile)
		}
	}
	return m, nil
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

func (m *Model) parse(file, source string, refsOnly bool) *interpreter.Database {
	before := len(m.Errors)
	l := lexer.New(source)
	tokens := l.Lex()
	for _, e := range l.Errors {
		m.Errors = append(m.Errors, issueAt(file, e.Position, e.Message))
	}
	p := parser.New(tokens, source)
	prog := p.Parse()
	for _, e := range p.Errors {
		m.Errors = append(m.Errors, issueAt(file, e.Position, e.Message))
	}
	if refsOnly {
		for _, node := range prog.Body {
			decl, ok := node.(*parser.ElementDeclNode)
			if !ok || decl.Type.Value != "Ref" {
				m.Errors = append(m.Errors, issueAt(file, node.FirstToken().StartPos, "only Ref declarations are allowed in a refs file"))
			}
		}
	}
	interp := interpreter.New()
	db := interp.Interpret(prog)
	for _, e := range interp.Errors {
		m.Errors = append(m.Errors, issueAt(file, e.Position, e.Message))
	}
	if len(m.Errors) > before {
		return nil
	}
	return db
}

func issueAt(file string, pos lexer.Position, message string) Issue {
	return Issue{File: file, Line: pos.Line + 1, Column: pos.Column + 1, Message: message}
}

func (m *Model) addTables(db *interpreter.Database) {
	indexByDisplay := make(map[string]int, len(m.Tables))
	for i, existing := range m.Tables {
		indexByDisplay[existing.Display()] = i
	}
	for _, t := range db.Tables {
		table := Table{Schema: schemaName(t.SchemaName), Name: t.Name, Columns: append([]interpreter.Column(nil), t.Fields...)}
		if t.Alias != nil {
			table.Alias = *t.Alias
		}
		if t.Note != nil {
			table.Note = t.Note.Value
		}
		display := table.Display()
		if idx, ok := indexByDisplay[display]; ok {
			existing := &m.Tables[idx]
			for _, col := range table.Columns {
				if _, exists := existing.column(col.Name); !exists {
					existing.Columns = append(existing.Columns, col)
				}
			}
			if existing.Alias == "" && table.Alias != "" {
				existing.Alias = table.Alias
			}
			if existing.Note == "" && table.Note != "" {
				existing.Note = table.Note
			}
			continue
		}
		indexByDisplay[display] = len(m.Tables)
		m.Tables = append(m.Tables, table)
	}
}

func schemaName(schema *string) string {
	if schema == nil || *schema == "public" {
		return ""
	}
	return *schema
}

// resolve finds the table an endpoint names, by name or alias.
func (m *Model) resolve(schema *string, name string) (*Table, bool) {
	want := schemaName(schema)
	for i := range m.Tables {
		t := &m.Tables[i]
		if t.Schema != want {
			continue
		}
		if t.Name == name || (schema == nil && t.Alias != "" && t.Alias == name) {
			return t, true
		}
	}
	return nil, false
}

func (m *Model) addRelations(refs []interpreter.Ref, source, file string) {
	seen := map[string]string{}
	for _, r := range m.Relations {
		seen[r.key()] = r.Source
	}
	for _, ref := range refs {
		if len(ref.Endpoints) != 2 {
			continue
		}
		left, right := ref.Endpoints[0], ref.Endpoints[1]
		line := left.Token.Start.Line // TokenPos lines are 1-based
		rel := Relation{Source: source, File: file, Line: line}
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

func (m *Model) endpoint(ep interpreter.RefEndpoint) (*Table, []string, string) {
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
	return fmt.Sprintf("%s %s %s", endpointText(r.From, r.FromColumns), r.Op, endpointText(r.To, r.ToColumns))
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
	graph := map[string][]edge{}
	for i := range m.Relations {
		r := &m.Relations[i]
		if declaredOnly && r.Source != SourceDeclared {
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
			return edges[i].Other < edges[j].Other
		})
	}
	return graph
}

// neighborhood returns the tables within depth hops of the given ones.
func (m *Model) Neighborhood(tables []string, depth int) map[string]bool {
	graph := m.adjacency(false)
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

// path finds a shortest join path with breadth-first search.
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
		step := Step{From: e.Other, To: t, Relation: e.Relation}
		if e.Relation.From == e.Other && (e.Relation.To == t) {
			step.FromColumns, step.ToColumns = e.Relation.FromColumns, e.Relation.ToColumns
		} else {
			step.FromColumns, step.ToColumns = e.Relation.ToColumns, e.Relation.FromColumns
		}
		steps = append([]Step{step}, steps...)
		t = e.Other
	}
	return steps, nil
}

// List returns the model names in dir, sorted.
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
