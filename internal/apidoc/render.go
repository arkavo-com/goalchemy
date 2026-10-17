package apidoc

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"go/doc/comment"
	"html"
	"path"
	"sort"
	"strings"
)

type renderer struct {
	catalog *Catalog
	symbols map[string]*Symbol
	files   map[string]string
	roots   map[string]bool
	site    bool
}

func slug(id string) string {
	var b strings.Builder
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			b.WriteString(strings.ToLower(string(r)))
		} else {
			b.WriteByte('-')
		}
	}
	s := strings.Trim(b.String(), "-")
	if len(s) > 60 {
		s = s[:60]
	}
	hash := sha256.Sum256([]byte(id))
	return fmt.Sprintf("%s-%x", s, hash[:6])
}
func newRenderer(c *Catalog) *renderer {
	r := &renderer{catalog: c, symbols: map[string]*Symbol{}, files: map[string]string{}, roots: map[string]bool{}}
	for _, s := range c.Symbols {
		r.symbols[s.ID] = s
		r.files[s.ID] = "symbols/" + slug(s.ID) + ".md"
	}
	for _, p := range c.Packages {
		r.files[p.ID] = "packages/" + slug(p.ID) + ".md"
		r.roots[p.ID] = p.Root
	}
	return r
}
func relative(from, to string) string {
	from = path.Dir(from)
	var ups []string
	for from != "." && from != "/" {
		ups = append(ups, "..")
		from = path.Dir(from)
	}
	return path.Join(append(ups, to)...)
}
func (r *renderer) href(from, to string) string {
	if !r.site {
		return relative(from, to)
	}
	route := func(file string) string {
		s := strings.TrimSuffix(file, ".md")
		if path.Base(s) == "index" {
			s = path.Dir(s)
		}
		return s
	}
	return relative(path.Join(route(from), "index.html"), route(to)) + "/"
}
func (r *renderer) link(from, id string) string { return r.href(from, r.files[id]) }
func (r *renderer) prose(text, pkg, file string) string {
	parser := comment.Parser{LookupPackage: func(name string) (string, bool) {
		for _, p := range r.catalog.Packages {
			if p.Name == name || p.ID == name {
				return p.ID, true
			}
		}
		return "", false
	}, LookupSym: func(recv, name string) bool {
		if recv != "" {
			name = recv + "." + name
		}
		return r.symbols[pkg+"#"+name] != nil
	}}
	printer := comment.Printer{DocLinkURL: func(link *comment.DocLink) string {
		owner := link.ImportPath
		if owner == "" {
			owner = pkg
		}
		id := owner
		if link.Name != "" {
			id += "#"
			if link.Recv != "" {
				id += link.Recv + "."
			}
			id += link.Name
		}
		if target := r.files[id]; target != "" {
			return r.href(file, target)
		}
		return "https://pkg.go.dev/" + owner + "#" + link.Name
	}}
	return string(printer.Markdown(parser.Parse(text)))
}
func code(b *bytes.Buffer, lang, text string) {
	fence := "```"
	for strings.Contains(text, fence) {
		fence += "`"
	}
	fmt.Fprintf(b, "%s%s\n%s\n%s\n\n", fence, lang, strings.TrimRight(text, "\n"), fence)
}
func label(text string) string {
	return strings.NewReplacer("[", "\\[", "]", "\\]", "\n", " ", "|", "\\|").Replace(html.EscapeString(text))
}
func (r *renderer) references(b *bytes.Buffer, file string, refs []string) {
	for _, id := range refs {
		if s := r.symbols[id]; s != nil {
			fmt.Fprintf(b, "[%s](%s) ", label(s.Name), r.link(file, id))
		}
	}
}
func (r *renderer) fields(b *bytes.Buffer, title, pkg, file string, fields []Field) {
	if len(fields) == 0 {
		return
	}
	fmt.Fprintf(b, "## %s\n\n", title)
	for i, f := range fields {
		name := f.Name
		if name == "" {
			name = fmt.Sprintf("result%d", i)
		}
		fmt.Fprintf(b, "### %s\n\n", label(name))
		code(b, "go", f.Type.Text)
		if f.Description != "" {
			b.WriteString(r.prose(f.Description, pkg, file) + "\n")
		}
		if f.Tag != "" {
			code(b, "text", f.Tag)
		}
		if len(f.Type.References) > 0 {
			b.WriteString("Types: ")
			r.references(b, file, f.Type.References)
			b.WriteString("\n\n")
		}
	}
}
func (r *renderer) symbol(s *Symbol, site bool) []byte {
	file := r.files[s.ID]
	var b bytes.Buffer
	if site {
		title, _ := json.Marshal(s.Name)
		fmt.Fprintf(&b, "---\ntitle: %s\neditUrl: false\nsidebar:\n  hidden: true\n---\n\n", title)
	} else {
		fmt.Fprintf(&b, "# %s\n\n", label(s.Name))
	}
	fmt.Fprintf(&b, "[%s](%s) · %s\n\n", label(s.Package), r.link(file, s.Package), s.Kind)
	if s.Parent != "" {
		fmt.Fprintf(&b, "Member of [%s](%s).\n\n", label(r.symbols[s.Parent].Name), r.link(file, s.Parent))
	}
	if s.Description != "" {
		b.WriteString(r.prose(s.Description, s.Package, file) + "\n")
	}
	if s.Deprecated != "" {
		b.WriteString("**Deprecated:** " + label(s.Deprecated) + "\n\n")
	}
	b.WriteString("## Go source\n\n")
	code(&b, "go", s.Signature)
	if s.Location.File != "" {
		fmt.Fprintf(&b, "Source: `%s:%d`.\n\n", s.Location.File, s.Location.Line)
	}
	r.fields(&b, "Parameters", s.Package, file, s.Parameters)
	r.fields(&b, "Results", s.Package, file, s.Results)
	r.fields(&b, "Fields", s.Package, file, s.Fields)
	if len(s.Members) > 0 {
		b.WriteString("## Methods\n\n")
		for _, id := range s.Members {
			fmt.Fprintf(&b, "- [%s](%s)\n", label(r.symbols[id].Name), r.link(file, id))
		}
		b.WriteByte('\n')
	}
	if len(s.Bindings) > 0 {
		b.WriteString("## Native APIs\n\n")
		for _, t := range r.catalog.Targets {
			for _, binding := range s.Bindings {
				if binding.Target == t.Name {
					fmt.Fprintf(&b, "### %s\n\n", targetLabel(t.Name))
					code(&b, language(t.Name), binding.Signature)
					if binding.Invocation != "" {
						b.WriteString("Call selector:\n\n")
						code(&b, language(t.Name), binding.Invocation)
					}
				}
			}
		}
		b.WriteString("Signatures describe compiler-generated library boundaries. Host adapters added by an SDK can expose additional APIs.\n\n")
	}
	if len(s.References) > 0 {
		b.WriteString("## Related types\n\n")
		r.references(&b, file, s.References)
		b.WriteString("\n")
	}
	return b.Bytes()
}
func targetLabel(target string) string {
	if name := map[string]string{"c": "C", "csharp": "C#", "go": "Go", "java": "Java", "python": "Python", "rust": "Rust", "swift": "Swift (experimental)", "typescript": "TypeScript"}[target]; name != "" {
		return name
	}
	return target
}
func language(target string) string {
	if target == "csharp" {
		return "csharp"
	}
	return target
}

// Markdown renders CommonMark pages with relative cross-references and fenced native signatures.
func (c *Catalog) Markdown() map[string][]byte { return c.pages(false) }
func (c *Catalog) pages(site bool) map[string][]byte {
	r := newRenderer(c)
	r.site = site
	files := map[string][]byte{}
	var b bytes.Buffer
	if site {
		title, _ := json.Marshal(c.Title)
		fmt.Fprintf(&b, "---\ntitle: %s\neditUrl: false\nsidebar:\n  order: 0\n---\n\n", title)
	} else {
		fmt.Fprintf(&b, "# %s\n\n", label(c.Title))
	}
	b.WriteString("## Packages\n\n")
	for _, p := range c.Packages {
		if p.Root {
			fmt.Fprintf(&b, "- [%s](%s)\n", label(p.ID), r.href("index.md", r.files[p.ID]))
		}
	}
	b.WriteString("\n## Target availability\n\n")
	for _, t := range c.Targets {
		fmt.Fprintf(&b, "- **%s**: %s", targetLabel(t.Name), t.Status)
		if t.Reason != "" {
			fmt.Fprintf(&b, ". %s", label(t.Reason))
		}
		b.WriteByte('\n')
	}
	b.WriteString("\nNative declarations are listed where a symbol is separately exposed. Source constants, variables, interfaces, aliases, or implementation methods may have no standalone native declaration. Swift is experimental.\n\n")
	files["index.md"] = append([]byte{}, b.Bytes()...)
	for _, p := range c.Packages {
		b.Reset()
		file := r.files[p.ID]
		if site {
			title, _ := json.Marshal(p.Name)
			fmt.Fprintf(&b, "---\ntitle: %s\neditUrl: false\n", title)
			if !p.Root {
				b.WriteString("sidebar:\n  hidden: true\n")
			}
			b.WriteString("---\n\n")
			code(&b, "text", p.ID)
		} else {
			fmt.Fprintf(&b, "# %s\n\n", label(p.ID))
		}
		fmt.Fprintf(&b, "[API index](%s)\n\n", r.href(file, "index.md"))
		b.WriteString(r.prose(p.Description, p.ID, file) + "\n")
		if !p.Root {
			b.WriteString("Referenced dependency type definitions.\n\n")
		}
		var ids []string
		ids = append(ids, p.Symbols...)
		sort.Strings(ids)
		for _, group := range []struct{ title, kinds string }{
			{"Types and aliases", " type alias "}, {"Functions", " function "},
			{"Constants", " constant "}, {"Variables", " variable "},
		} {
			var entries bytes.Buffer
			for _, id := range ids {
				s := r.symbols[id]
				if s.Parent == "" && strings.Contains(group.kinds, " "+s.Kind+" ") {
					fmt.Fprintf(&entries, "- [%s](%s) (%s)\n", label(s.Name), r.link(file, id), s.Kind)
				}
			}
			if entries.Len() > 0 {
				fmt.Fprintf(&b, "## %s\n\n%s\n", group.title, &entries)
			}
		}
		files[file] = append([]byte{}, b.Bytes()...)
	}
	for _, s := range c.Symbols {
		files[r.files[s.ID]] = r.symbol(s, site)
	}
	return files
}
