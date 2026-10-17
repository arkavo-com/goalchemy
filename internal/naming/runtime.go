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
	runtimeDecl  = regexp.MustCompile(`(?m)^(?:(?:pub(?:\([a-z]+\))?|public|internal|static|final|inline|extern|const|unsafe|async|typedef|struct|enum|union)\s+)*(?:fn|struct|enum|union|type|trait|const|static|mod|func|class|protocol|typealias|let|var|interface|record|#define|void|bool|int|double|gx_V|[a-z_0-9]+_t)\s+\**([A-Za-z_][A-Za-z0-9_]*)`)
)

// RuntimeNames scans a target runtime whose names generated code uses
// without a qualifier. declared lists what the runtime declares at top level
// and the type names it uses; no source name takes them. called lists the
// names it calls unqualified, such as host library functions; no top-level
// source name takes them.
func RuntimeNames(fsys fs.FS, roots ...string) (declared, called []string, err error) {
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
			scanRuntime(string(data), decl, call)
			return nil
		})
		if err != nil {
			return nil, nil, err
		}
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

func scanRuntime(src string, declared, called map[string]bool) {
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
		case word[0] >= 'A' && word[0] <= 'Z':
			declared[word] = true
		case strings.HasPrefix(next, "("):
			called[word] = true
		}
	}
}
