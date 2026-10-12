// Package link resolves the runtime files a generated program needs and
// writes them with a build manifest.
package link

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"

	"github.com/eugenioenko/goalchemy/internal/contracts"
	"github.com/eugenioenko/goalchemy/internal/diagnostics"
	"github.com/eugenioenko/goalchemy/internal/emit/artifact"
	"github.com/eugenioenko/goalchemy/internal/frontend"
)

const CompilerVersion = "0.6.1"

type Manifest struct {
	Compiler       string                  `json:"compiler"`
	Target         string                  `json:"target"`
	TargetVersion  string                  `json:"target_version"`
	SourceProfile  map[string]string       `json:"source_profile"`
	Contracts      []ContractRef           `json:"contracts"`
	RuntimeFiles   []string                `json:"runtime_files"`
	GeneratedFiles []string                `json:"generated_files"`
	SourcePackages []artifact.PackageFiles `json:"source_packages,omitempty"`
}

type ContractRef struct {
	ID      string `json:"id"`
	Version string `json:"version"`
	Hash    string `json:"hash"`
}

// Plan resolves contracts transitively for target and returns the runtime
// files to copy (relative to the target directory).
func Plan(cat *contracts.Catalog, target string, used []string) ([]ContractRef, []string, []diagnostics.Diagnostic) {
	t, ok := cat.Targets[target]
	if !ok {
		return nil, nil, []diagnostics.Diagnostic{{Code: "GCE001", Severity: diagnostics.Error, Feature: "target",
			Message: "no target mapping for " + target, Remedy: "Add targets/" + target + "/target.yaml."}}
	}
	ids, err := cat.Closure(used)
	if err != nil {
		return nil, nil, []diagnostics.Diagnostic{{Code: "GCE002", Severity: diagnostics.Error, Feature: "runtime linking",
			Message: err.Error(), Remedy: "Add the missing runtime contract."}}
	}
	var ds []diagnostics.Diagnostic
	files := map[string]bool{}
	for _, s := range t.SupportFiles {
		files[s] = true
	}
	var refs []ContractRef
	seen := map[string]bool{}
	var add func(id string)
	add = func(id string) {
		if seen[id] {
			return
		}
		seen[id] = true
		impl, ok := t.Function(id)
		if !ok {
			ds = append(ds, diagnostics.Diagnostic{Code: "GCE002", Severity: diagnostics.Error, Feature: "runtime linking",
				Symbol: id, Message: fmt.Sprintf("target %s does not implement runtime function %s", target, id),
				Remedy: "Implement the function for this target or avoid the operation."})
			return
		}
		fc := cat.Functions[id]
		if impl.ContractVersion != fc.Version {
			ds = append(ds, diagnostics.Diagnostic{Code: "GCE003", Severity: diagnostics.Error, Feature: "runtime linking",
				Symbol: id, Message: fmt.Sprintf("%s implements %s %s, catalog has %s", target, id, impl.ContractVersion, fc.Version),
				Remedy: "Update the target implementation to the current contract."})
		}
		files[impl.Implementation] = true
		refs = append(refs, ContractRef{ID: id, Version: fc.Version, Hash: fc.Hash})
		for _, r := range impl.Requires {
			add(r)
		}
	}
	for _, id := range ids {
		add(id)
	}
	sort.Slice(refs, func(i, j int) bool { return refs[i].ID < refs[j].ID })
	var list []string
	for f := range files {
		list = append(list, f)
	}
	sort.Strings(list)
	return refs, list, ds
}

// CopyRuntime copies runtime files from the catalog into out/dest, keeping
// only the base names when flatten is set.
func CopyRuntime(cat *contracts.Catalog, target string, files []string, out, dest string, flatten bool) ([]string, error) {
	t := cat.Targets[target]
	var written []string
	for _, f := range files {
		data, err := fs.ReadFile(cat.FS, path.Join(t.Dir, f))
		if err != nil {
			return nil, err
		}
		rel := f
		if flatten {
			rel = path.Base(f)
		}
		rel = path.Join(dest, rel)
		if err := WriteFile(out, rel, data); err != nil {
			return nil, err
		}
		written = append(written, rel)
	}
	return written, nil
}

func WriteFile(out, rel string, data []byte) error {
	p := filepath.Join(out, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	return os.WriteFile(p, data, 0o644)
}

func WriteManifest(out string, cat *contracts.Catalog, target string, refs []ContractRef, runtime, generated []string, prog *frontend.Program, packages ...artifact.PackageFiles) error {
	m := Manifest{
		Compiler: CompilerVersion, Target: target, TargetVersion: cat.Targets[target].Version,
		SourceProfile: map[string]string{"language": frontend.SourceLanguage, "toolchain": frontend.ReferenceToolchain,
			"goos": frontend.SourceGOOS, "goarch": frontend.SourceGOARCH, "cgo": "0"},
		Contracts: refs, RuntimeFiles: runtime, GeneratedFiles: generated,
		SourcePackages: packages,
	}
	if m.Contracts == nil {
		m.Contracts = []ContractRef{}
	}
	if prog != nil && len(prog.Tags) > 0 {
		b, _ := json.Marshal(prog.Tags)
		m.SourceProfile["tags"] = string(b)
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return WriteFile(out, "goalchemy.manifest.json", append(data, '\n'))
}

func Hash(data []byte) string {
	s := sha256.Sum256(data)
	return hex.EncodeToString(s[:])
}
