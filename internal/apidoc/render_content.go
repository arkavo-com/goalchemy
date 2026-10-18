package apidoc

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"strings"
)

func escapeMDX(text string) string {
	var out strings.Builder
	fence, inline := "", ""
	for _, line := range strings.SplitAfter(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			marker := trimmed[:3]
			if fence == "" {
				fence = marker
			} else if strings.HasPrefix(trimmed, fence) {
				fence = ""
			}
			out.WriteString(line)
			continue
		}
		if fence != "" {
			out.WriteString(line)
			continue
		}
		if strings.HasPrefix(line, "import ") || strings.HasPrefix(line, "export ") {
			fmt.Fprintf(&out, "&#%d;", line[0])
			line = line[1:]
		}
		for i := 0; i < len(line); {
			if line[i] == '`' {
				j := i
				for j < len(line) && line[j] == '`' {
					j++
				}
				run := line[i:j]
				if inline == "" {
					inline = run
				} else if inline == run {
					inline = ""
				}
				out.WriteString(run)
				i = j
				continue
			}
			if inline == "" && line[i] == '{' {
				out.WriteString("&#123;")
			} else if inline == "" && line[i] == '}' {
				out.WriteString("&#125;")
			} else {
				out.WriteByte(line[i])
			}
			i++
		}
	}
	return out.String()
}

func (r *renderer) imports(b *bytes.Buffer, file string, rpc bool) {
	fmt.Fprintf(b, "import LanguageTabs from %q;\n", relative(file, "../../../components/LanguageTabs.astro"))
	if rpc {
		fmt.Fprintf(b, "import RpcFields from %q;\n", relative(file, "../../../components/RpcFields.astro"))
	}
	b.WriteByte('\n')
}

func (r *renderer) snippets(b *bytes.Buffer, snippets []Snippet) {
	if len(snippets) == 0 {
		return
	}
	if r.site {
		data, _ := json.Marshal(snippets)
		fmt.Fprintf(b, "<LanguageTabs snippets={%s} />\n\n", data)
		return
	}
	for _, snippet := range snippets {
		fmt.Fprintf(b, "### %s\n\n", targetLabel(snippet.Target))
		code(b, language(snippet.Target), snippet.Code)
	}
}

func (r *renderer) bindings(b *bytes.Buffer, bindings []Binding) {
	var snippets []Snippet
	positions := map[string]int{}
	for _, binding := range bindings {
		text := binding.Signature
		if binding.Invocation != "" {
			text += "\n\n" + binding.Invocation
		}
		if index, ok := positions[binding.Target]; ok {
			snippets[index].Code += "\n\n" + text
		} else {
			positions[binding.Target] = len(snippets)
			snippets = append(snippets, Snippet{Target: binding.Target, Code: text})
		}
	}
	r.snippets(b, snippets)
}

func (r *renderer) example(b *bytes.Buffer, e Example) {
	fmt.Fprintf(b, "### %s\n\n", label(e.Title))
	text := html.EscapeString(e.Description)
	if r.site {
		text = escapeMDX(text)
	}
	if text != "" {
		b.WriteString(text + "\n\n")
	}
	r.snippets(b, e.Snippets)
	for _, body := range []struct {
		title string
		value json.RawMessage
	}{{"Example request", e.Request}, {"Example response", e.Response}} {
		if len(body.value) == 0 {
			continue
		}
		fmt.Fprintf(b, "#### %s\n\n", body.title)
		var pretty bytes.Buffer
		if err := json.Indent(&pretty, body.value, "", "  "); err == nil {
			code(b, "json", pretty.String())
		}
	}
	if len(e.Request) > 0 || len(e.Response) > 0 {
		b.WriteString("These are illustrative payloads, checked against the protobuf JSON schema. They do not prove server authorization or validation rules.\n\n")
	}
}

func (r *renderer) examplesFor(b *bytes.Buffer, id string) {
	started := false
	for _, e := range r.catalog.Examples {
		if e.For != id {
			continue
		}
		if !started {
			b.WriteString("## Usage examples\n\n")
			started = true
		}
		r.example(b, e)
	}
}

func (r *renderer) header(b *bytes.Buffer, title, file string, hidden, rpc bool) {
	if !r.site {
		fmt.Fprintf(b, "# %s\n\n", label(title))
		return
	}
	encoded, _ := json.Marshal(title)
	fmt.Fprintf(b, "---\ntitle: %s\neditUrl: false\n", encoded)
	if hidden {
		b.WriteString("sidebar:\n  hidden: true\n")
	}
	b.WriteString("---\n\n")
	if strings.HasSuffix(file, ".mdx") {
		r.imports(b, file, rpc)
	}
}

func (r *renderer) examplePages(files map[string][]byte) {
	if len(r.catalog.Examples) == 0 {
		return
	}
	var index bytes.Buffer
	r.header(&index, "Usage examples", "examples/index.md", false, false)
	for _, e := range r.catalog.Examples {
		file := "examples/" + slug(e.ID) + ".md"
		if r.site {
			file += "x"
		}
		fmt.Fprintf(&index, "- [%s](%s)\n", label(e.Title), r.href("examples/index.md", file))
		var page bytes.Buffer
		r.header(&page, e.Title, file, true, false)
		if target := r.files[e.For]; target != "" {
			fmt.Fprintf(&page, "[API reference](%s)\n\n", r.href(file, target))
		}
		r.example(&page, e)
		files[file] = page.Bytes()
	}
	files["examples/index.md"] = index.Bytes()
}
