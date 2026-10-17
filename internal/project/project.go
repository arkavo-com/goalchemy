// Package project reads goalchemy.yaml, the project configuration that
// records roots, build tags, the language gate, and target outputs.
package project

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/eugenioenko/goalchemy/internal/contracts"
)

const FileName = "goalchemy.yaml"

type Target struct {
	Out string `json:"out"`
	// Host lists Go files or directories, relative to the configuration
	// file, that the project compiles into the generated Go package.
	Host []string `json:"host,omitempty"`
}

type Config struct {
	SchemaVersion int      `json:"schema_version"`
	Packages      []string `json:"packages"`
	Tags          []string `json:"tags,omitempty"`
	Gate          string   `json:"gate,omitempty"`
	// Names is source (the default), readable or compact; compact_names is
	// the older spelling of names: compact.
	Names          string `json:"names,omitempty"`
	NameCollisions string `json:"name_collisions,omitempty"`
	CompactNames   bool   `json:"compact_names,omitempty"`
	PauseModel     string `json:"pause_model,omitempty"`
	// LineDirectives, when false, omits source annotations; nil means on.
	LineDirectives *bool             `json:"line_directives,omitempty"`
	Targets        map[string]Target `json:"targets"`

	// Dir is the directory containing the configuration file.
	Dir string `json:"-"`
}

// Load reads and validates a configuration file.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	v, err := contracts.ParseYAML(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	data, _ := json.Marshal(v)
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var c Config
	if err := dec.Decode(&c); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if c.SchemaVersion != 1 {
		return nil, fmt.Errorf("%s: schema_version must be 1", path)
	}
	if len(c.Packages) == 0 {
		return nil, fmt.Errorf("%s: packages must list at least one package pattern", path)
	}
	if len(c.Targets) == 0 {
		return nil, fmt.Errorf("%s: targets must name at least one target", path)
	}
	for name, t := range c.Targets {
		if t.Out == "" {
			return nil, fmt.Errorf("%s: target %s needs an out directory", path, name)
		}
		if len(t.Host) > 0 && name != "go" {
			return nil, fmt.Errorf("%s: target %s: host files are only supported for the go target", path, name)
		}
	}
	switch c.PauseModel {
	case "", "frames", "native":
	default:
		return nil, fmt.Errorf("%s: unknown pause_model %q", path, c.PauseModel)
	}
	switch c.Names {
	case "", "source", "readable", "compact":
	default:
		return nil, fmt.Errorf("%s: unknown names %q", path, c.Names)
	}
	if c.CompactNames && c.Names != "" && c.Names != "compact" {
		return nil, fmt.Errorf("%s: compact_names conflicts with names: %s", path, c.Names)
	}
	switch c.NameCollisions {
	case "", "rename", "fail":
	default:
		return nil, fmt.Errorf("%s: unknown name_collisions %q", path, c.NameCollisions)
	}
	switch c.Gate {
	case "", "sequential", "cooperative":
	default:
		return nil, fmt.Errorf("%s: unknown gate %q", path, c.Gate)
	}
	abs, _ := filepath.Abs(path)
	c.Dir = filepath.Dir(abs)
	return &c, nil
}

// NameMode returns the configured names mode; empty means the default.
func (c *Config) NameMode() string {
	if c.Names == "" && c.CompactNames {
		return "compact"
	}
	return c.Names
}

// TargetNames returns configured targets in sorted order.
func (c *Config) TargetNames() []string {
	var out []string
	for n := range c.Targets {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

// OutDir resolves a target output directory relative to the config file.
func (c *Config) OutDir(target string) string {
	out := c.Targets[target].Out
	if filepath.IsAbs(out) {
		return out
	}
	return filepath.Join(c.Dir, out)
}

// HostFiles resolves a target's host files relative to the config file.
func (c *Config) HostFiles(target string) []string {
	var out []string
	for _, h := range c.Targets[target].Host {
		if !filepath.IsAbs(h) {
			h = filepath.Join(c.Dir, h)
		}
		out = append(out, h)
	}
	return out
}

// RegressionMeta records how a corpus regression was found.
type RegressionMeta struct {
	ID        string   `json:"id"`
	Summary   string   `json:"summary"`
	FoundBy   string   `json:"found_by"`
	Compiler  string   `json:"compiler"`
	Toolchain string   `json:"toolchain"`
	Targets   []string `json:"targets"`
	Golden    bool     `json:"golden,omitempty"`
	Seed      string   `json:"seed,omitempty"`
}

func LoadMeta(path string) (*RegressionMeta, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	v, err := contracts.ParseYAML(raw)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	data, _ := json.Marshal(v)
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var m RegressionMeta
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	if m.ID == "" || m.Summary == "" || len(m.Targets) == 0 {
		return nil, fmt.Errorf("%s: id, summary, and targets are required", path)
	}
	return &m, nil
}
