package diagnostics

import (
	"encoding/json"
	"fmt"
	"go/token"
	"io"
	"sort"
)

type Severity string

const (
	Error   Severity = "error"
	Warning Severity = "warning"
)

type Diagnostic struct {
	Code     string   `json:"code"`
	Severity Severity `json:"severity"`
	File     string   `json:"file,omitempty"`
	Line     int      `json:"line,omitempty"`
	Column   int      `json:"column,omitempty"`
	EndLine  int      `json:"end_line,omitempty"`
	EndCol   int      `json:"end_column,omitempty"`
	Symbol   string   `json:"symbol,omitempty"`
	Feature  string   `json:"feature,omitempty"`
	Message  string   `json:"message"`
	Remedy   string   `json:"remedy"`
}

func (d Diagnostic) String() string {
	loc := "goalchemy"
	if d.File != "" {
		loc = d.File
		if d.Line > 0 {
			loc = fmt.Sprintf("%s:%d:%d", d.File, d.Line, d.Column)
		}
	}
	s := fmt.Sprintf("%s: %s %s: %s", loc, d.Severity, d.Code, d.Message)
	if d.Symbol != "" {
		s += fmt.Sprintf(" [in %s]", d.Symbol)
	}
	if d.Remedy != "" {
		s += "\n    remedy: " + d.Remedy
	}
	return s
}

type List []Diagnostic

func (l *List) Add(d Diagnostic) { *l = append(*l, d) }

func Span(fset *token.FileSet, pos, end token.Pos) Diagnostic {
	var d Diagnostic
	if pos.IsValid() {
		p := fset.Position(pos)
		d.File, d.Line, d.Column = p.Filename, p.Line, p.Column
	}
	if end.IsValid() {
		e := fset.Position(end)
		d.EndLine, d.EndCol = e.Line, e.Column
	}
	return d
}

func Sort(ds []Diagnostic) {
	sort.SliceStable(ds, func(i, j int) bool {
		a, b := ds[i], ds[j]
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		if a.Column != b.Column {
			return a.Column < b.Column
		}
		if a.Code != b.Code {
			return a.Code < b.Code
		}
		return a.Message < b.Message
	})
}

func Dedup(ds []Diagnostic) []Diagnostic {
	seen := map[string]bool{}
	out := ds[:0]
	for _, d := range ds {
		k := fmt.Sprintf("%s|%s|%d|%d|%s", d.Code, d.File, d.Line, d.Column, d.Message)
		if !seen[k] {
			seen[k] = true
			out = append(out, d)
		}
	}
	return out
}

func HasErrors(ds []Diagnostic) bool {
	for _, d := range ds {
		if d.Severity == Error {
			return true
		}
	}
	return false
}

func Write(w io.Writer, ds []Diagnostic, asJSON bool) error {
	if asJSON {
		if ds == nil {
			ds = []Diagnostic{}
		}
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(struct {
			Diagnostics []Diagnostic `json:"diagnostics"`
		}{ds})
	}
	for _, d := range ds {
		if _, err := fmt.Fprintln(w, d.String()); err != nil {
			return err
		}
	}
	return nil
}
