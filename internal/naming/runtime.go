package naming

import (
	"io/fs"
	"regexp"
	"sort"
	"strings"
)

var (
	runtimeNoise = regexp.MustCompile(`(?s)/\*.*?\*/|//[^\n]*|"(?:\\.|[^"\\\n])*"|'(?:\\.|[^'\\\n])'`)
	runtimeIdent = regexp.MustCompile(`[A-Za-z_][A-Za-z0-9_]*`)
	runtimeDecl  = regexp.MustCompile(`(?m)^(?:(?:pub(?:\([a-z]+\))?|public|internal|static|final|abstract|sealed|partial|readonly|inline|extern|const|unsafe|async|typedef|struct|enum|union)\s+)*(?:fn|struct|enum|union|type|trait|const|static|mod|func|class|protocol|typealias|let|var|interface|record|#define|void|bool|int|double|gx_V|[a-z_0-9]+_t)\s+\**([A-Za-z_][A-Za-z0-9_]*)`)
)

// RuntimeScan selects what RuntimeNames reserves besides the runtime's
// top-level declarations.
type RuntimeScan struct {
	// Types reserves every capitalized name the runtime uses, for targets
	// where a source declaration would shadow it for the runtime itself.
	Types bool
	// Calls reserves for top-level names the names the runtime calls
	// unqualified, for targets that share a namespace with the runtime.
	Calls bool
	// LocalCalls reserves them for locals too, for targets whose runtime
	// macros expand to those calls inside generated functions.
	LocalCalls bool
}

// RuntimeNames scans a target runtime whose names generated code uses
// without a qualifier. declared lists what no source name takes: the
// runtime's top-level declarations and, as scan selects, the type names it
// uses and the names it calls. called lists the names it calls unqualified,
// such as host library functions, which no top-level source name takes.
func RuntimeNames(fsys fs.FS, scan RuntimeScan, roots ...string) (declared, called []string, err error) {
	decl, call := map[string]bool{}, map[string]bool{}
	for _, root := range roots {
		err := fs.WalkDir(fsys, root, func(name string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			data, err := fs.ReadFile(fsys, name)
			if err != nil {
				return err
			}
			scanRuntime(string(data), scan.Types, decl, call)
			return nil
		})
		if err != nil {
			return nil, nil, err
		}
	}
	if scan.LocalCalls {
		for n := range call {
			decl[n] = true
		}
	}
	if !scan.Calls {
		call = nil
	}
	return sorted(decl), sorted(call), nil
}

func sorted(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for n := range set {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func scanRuntime(src string, types bool, declared, called map[string]bool) {
	src = runtimeNoise.ReplaceAllStringFunc(src, func(s string) string {
		return strings.Repeat(" ", len(s))
	})
	for _, m := range runtimeDecl.FindAllStringSubmatch(src, -1) {
		declared[m[1]] = true
	}
	for _, loc := range runtimeIdent.FindAllStringIndex(src, -1) {
		start, end := loc[0], loc[1]
		if start > 0 && (src[start-1] == '.' || src[start-1] == '$' || src[start-1] >= '0' && src[start-1] <= '9') {
			continue
		}
		if start > 1 && (src[start-2:start] == "->" || src[start-2:start] == "::") {
			continue
		}
		word := src[start:end]
		switch next := strings.TrimLeft(src[end:], " \t"); {
		case types && word[0] >= 'A' && word[0] <= 'Z':
			declared[word] = true
		case strings.HasPrefix(next, "("):
			called[word] = true
		}
	}
}
