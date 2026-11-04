package graph

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// extractor accumulates per-file facts, then resolves cross-file references
// by symbol name once every file has been ingested.
type extractor struct {
	g *Graph
	// defs maps a bare symbol name to the node ids that define it.
	defs map[string][]string
	// refs are (fromNodeID, symbolName, rel) awaiting resolution.
	refs []pendingRef
}

type pendingRef struct {
	from string
	name string
	rel  string
}

func newExtractor(g *Graph) *extractor {
	return &extractor{g: g, defs: map[string][]string{}}
}

var codeExts = map[string]bool{
	".go": true, ".py": true, ".js": true, ".jsx": true, ".ts": true,
	".tsx": true, ".rs": true, ".rb": true, ".java": true, ".kt": true,
	".c": true, ".h": true, ".cc": true, ".cpp": true, ".hpp": true,
	".cs": true, ".swift": true, ".php": true, ".sh": true, ".zsh": true,
}

var docExts = map[string]bool{
	".md": true, ".mdx": true, ".rst": true, ".txt": true, ".adoc": true,
}

// ingest parses one file and adds its nodes/edges. Returns false when the
// file type is not mapped.
func (ex *extractor) ingest(rel, abs string) bool {
	ext := strings.ToLower(filepath.Ext(rel))
	switch {
	case ext == ".go":
		return ex.ingestGo(rel, abs)
	case codeExts[ext]:
		return ex.ingestLexical(rel, abs, ext)
	case docExts[ext]:
		return ex.ingestDoc(rel, abs)
	}
	return false
}

func (ex *extractor) fileNode(rel, kind string) *Node {
	n := ex.g.node(rel, filepath.Base(rel), kind, rel, 1)
	if dir := filepath.ToSlash(filepath.Dir(rel)); dir != "." {
		p := ex.g.node("pkg:"+dir, dir, KindPackage, dir, 0)
		ex.g.edge(p.ID, n.ID, "contains", OriginExtracted)
	}
	return n
}

func (ex *extractor) define(fileID, name, kind, rel string, line int) {
	id := "sym:" + rel + ":" + name
	ex.g.node(id, name, kind, rel, line)
	ex.g.edge(fileID, id, "defines", OriginExtracted)
	ex.defs[name] = append(ex.defs[name], id)
}

// --- Go: real AST, fully deterministic ---

func (ex *extractor) ingestGo(rel, abs string) bool {
	src, err := os.ReadFile(abs)
	if err != nil {
		return false
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, abs, src, parser.SkipObjectResolution)
	if err != nil {
		return ex.ingestLexical(rel, abs, ".go") // fall back on broken files
	}
	file := ex.fileNode(rel, KindFile)

	for _, imp := range f.Imports {
		path, _ := strconv.Unquote(imp.Path.Value)
		impNode := ex.g.node("import:"+path, path, KindPackage, "", 0)
		ex.g.edge(file.ID, impNode.ID, "imports", OriginExtracted)
	}

	called := map[string]bool{}
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			ex.define(file.ID, d.Name.Name, KindFunction, rel, fset.Position(d.Pos()).Line)
			from := "sym:" + rel + ":" + d.Name.Name
			ast.Inspect(d, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				var name string
				switch fn := call.Fun.(type) {
				case *ast.Ident:
					name = fn.Name
				case *ast.SelectorExpr:
					name = fn.Sel.Name
				}
				if name != "" && name != d.Name.Name && !called[d.Name.Name+"→"+name] {
					called[d.Name.Name+"→"+name] = true
					ex.refs = append(ex.refs, pendingRef{from: from, name: name, rel: "calls"})
				}
				return true
			})
		case *ast.GenDecl:
			if d.Tok == token.TYPE {
				for _, spec := range d.Specs {
					if ts, ok := spec.(*ast.TypeSpec); ok {
						ex.define(file.ID, ts.Name.Name, KindType, rel, fset.Position(ts.Pos()).Line)
					}
				}
			}
		}
	}
	return true
}

// --- Other languages: lexical extraction (imports + top-level definitions) ---

var (
	rePyImport   = regexp.MustCompile(`(?m)^\s*(?:from\s+([\w.]+)\s+import|import\s+([\w.]+))`)
	reJSImport   = regexp.MustCompile(`(?m)(?:from\s+|require\s*\(\s*|import\s+)['"]([^'"]+)['"]`)
	reRustUse    = regexp.MustCompile(`(?m)^\s*use\s+([\w:]+)`)
	reCInclude   = regexp.MustCompile(`(?m)^\s*#include\s*[<"]([^>"]+)[>"]`)
	reDefGeneric = regexp.MustCompile(`(?m)^\s*(?:export\s+)?(?:pub\s+)?(?:async\s+)?(def|class|function|fn|struct|enum|trait|interface|impl|module)\s+([A-Za-z_]\w*)`)
	reJSConstFn  = regexp.MustCompile(`(?m)^\s*(?:export\s+)?const\s+([A-Za-z_]\w*)\s*=\s*(?:async\s*)?(?:\(|function)`)
	reIdent      = regexp.MustCompile(`[A-Za-z_]\w{2,}`)
)

func (ex *extractor) ingestLexical(rel, abs, ext string) bool {
	src, err := os.ReadFile(abs)
	if err != nil {
		return false
	}
	text := string(src)
	file := ex.fileNode(rel, KindFile)

	var importRe *regexp.Regexp
	switch ext {
	case ".py":
		importRe = rePyImport
	case ".js", ".jsx", ".ts", ".tsx":
		importRe = reJSImport
	case ".rs":
		importRe = reRustUse
	case ".c", ".h", ".cc", ".cpp", ".hpp":
		importRe = reCInclude
	}
	if importRe != nil {
		seen := map[string]bool{}
		for _, m := range importRe.FindAllStringSubmatch(text, 200) {
			target := m[1]
			if target == "" && len(m) > 2 {
				target = m[2]
			}
			if target == "" || seen[target] {
				continue
			}
			seen[target] = true
			impNode := ex.g.node("import:"+target, target, KindPackage, "", 0)
			ex.g.edge(file.ID, impNode.ID, "imports", OriginExtracted)
		}
	}

	for _, m := range reDefGeneric.FindAllStringSubmatchIndex(text, 400) {
		keyword := text[m[2]:m[3]]
		name := text[m[4]:m[5]]
		kind := KindFunction
		if keyword == "class" || keyword == "struct" || keyword == "enum" || keyword == "trait" || keyword == "interface" || keyword == "impl" || keyword == "module" {
			kind = KindClass
		}
		ex.define(file.ID, name, kind, rel, lineAt(text, m[0]))
	}
	for _, m := range reJSConstFn.FindAllStringSubmatchIndex(text, 200) {
		ex.define(file.ID, text[m[2]:m[3]], KindFunction, rel, lineAt(text, m[0]))
	}

	// Reference pass: any identifier matching a symbol defined elsewhere.
	seen := map[string]bool{}
	for _, id := range reIdent.FindAllString(text, 4000) {
		if !seen[id] {
			seen[id] = true
			ex.refs = append(ex.refs, pendingRef{from: file.ID, name: id, rel: "references"})
		}
	}
	return true
}

// --- Docs: headings + links ---

var (
	reMDHeading = regexp.MustCompile(`(?m)^#{1,3}\s+(.+)$`)
	reMDLink    = regexp.MustCompile(`\[[^\]]*\]\(([^)#\s]+)`)
)

func (ex *extractor) ingestDoc(rel, abs string) bool {
	src, err := os.ReadFile(abs)
	if err != nil {
		return false
	}
	text := string(src)
	file := ex.fileNode(rel, KindDoc)

	for _, m := range reMDLink.FindAllStringSubmatch(text, 200) {
		target := m[1]
		if strings.Contains(target, "://") {
			continue
		}
		clean := filepath.ToSlash(filepath.Join(filepath.Dir(rel), target))
		ex.refs = append(ex.refs, pendingRef{from: file.ID, name: "file:" + clean, rel: "links_to"})
	}
	// Headings become reference candidates: a doc that names a code symbol
	// in a heading gets connected to it.
	for _, m := range reMDHeading.FindAllStringSubmatch(text, 100) {
		for _, id := range reIdent.FindAllString(m[1], 8) {
			ex.refs = append(ex.refs, pendingRef{from: file.ID, name: id, rel: "references"})
		}
	}
	return true
}

// resolve turns pending name references into INFERRED edges against the
// symbol table built during ingestion.
func (ex *extractor) resolve() {
	for _, r := range ex.refs {
		if fileTarget, ok := strings.CutPrefix(r.name, "file:"); ok {
			if n := ex.g.byID[fileTarget]; n != nil {
				ex.g.edge(r.from, n.ID, r.rel, OriginExtracted)
			}
			continue
		}
		targets := ex.defs[r.name]
		// Ambiguous names (defined in many places) create noise, not signal.
		if len(targets) == 0 || len(targets) > 3 {
			continue
		}
		for _, t := range targets {
			if strings.HasPrefix(t, "sym:") && ex.g.byID[t] != nil {
				// Skip self-file references from the lexical pass.
				if tn := ex.g.byID[t]; tn.File == ex.g.byID[r.from].File && r.rel == "references" {
					continue
				}
				ex.g.edge(r.from, t, r.rel, OriginInferred)
			}
		}
	}
	ex.dedupeEdges()
}

func (ex *extractor) dedupeEdges() {
	seen := map[string]bool{}
	out := ex.g.Edges[:0]
	for _, e := range ex.g.Edges {
		key := e.From + "\x00" + e.To + "\x00" + e.Rel
		if !seen[key] {
			seen[key] = true
			out = append(out, e)
		}
	}
	ex.g.Edges = out
	sort.Slice(ex.g.Edges, func(i, j int) bool {
		a, b := ex.g.Edges[i], ex.g.Edges[j]
		if a.From != b.From {
			return a.From < b.From
		}
		if a.To != b.To {
			return a.To < b.To
		}
		return a.Rel < b.Rel
	})
}

func lineAt(text string, offset int) int {
	return strings.Count(text[:offset], "\n") + 1
}
