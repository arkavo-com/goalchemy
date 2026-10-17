package apidoc

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

//go:embed template
var template embed.FS

const inventoryName = ".goalchemy-docs.json"

type inventory struct {
	Format string            `json:"format"`
	Files  map[string]string `json:"files"`
}

func digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

// Write publishes an API catalog, portable Markdown, or an optional Starlight site.
// It preserves unrelated files and customized scaffold files, and protects edited references.
func (c *Catalog) Write(out, format string) error {
	if err := c.Check(); err != nil {
		return err
	}
	catalog, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	catalog = append(catalog, '\n')
	files := map[string][]byte{}
	scaffold := map[string]bool{}
	switch format {
	case "json":
		files["api.json"] = catalog
	case "markdown":
		files = c.Markdown()
		files["api.json"] = catalog
	case "site":
		for file, body := range c.pages(true) {
			files["src/content/docs/api/"+file] = body
		}
		files["public/api.json"] = catalog
		title, _ := json.Marshal(c.Title)
		err = fs.WalkDir(template, "template", func(file string, entry fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() {
				return nil
			}
			data, err := template.ReadFile(file)
			if err != nil {
				return err
			}
			name := strings.TrimPrefix(file, "template/")
			files[name] = []byte(strings.ReplaceAll(string(data), "__TITLE__", string(title)))
			scaffold[name] = true
			return nil
		})
		if err != nil {
			return err
		}
		files[".gitignore"] = []byte("node_modules/\ndist/\n.astro/\n")
		scaffold[".gitignore"] = true
	default:
		return fmt.Errorf("unsupported documentation format %q", format)
	}
	return publish(out, format, files, scaffold)
}

func safePath(root, name string) (string, error) {
	if name == "" || filepath.IsAbs(name) || strings.Contains(name, "\\") || filepath.Clean(name) != name || name == ".." || strings.HasPrefix(name, "../") {
		return "", fmt.Errorf("invalid managed documentation path %q", name)
	}
	full := filepath.Join(root, filepath.FromSlash(name))
	current := root
	if info, err := os.Lstat(current); err == nil && info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("refusing symlink documentation directory %s", current)
	}
	for _, part := range strings.Split(name, "/") {
		current = filepath.Join(current, part)
		if info, err := os.Lstat(current); err == nil && info.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("refusing symlink documentation path %s", current)
		}
	}
	return full, nil
}
func publish(out, format string, files map[string][]byte, scaffold map[string]bool) error {
	root, err := filepath.Abs(out)
	if err != nil {
		return err
	}
	inventoryPath, err := safePath(root, inventoryName)
	if err != nil {
		return err
	}
	previous := inventory{Format: format, Files: map[string]string{}}
	if data, err := os.ReadFile(inventoryPath); err == nil {
		if err = json.Unmarshal(data, &previous); err != nil {
			return fmt.Errorf("invalid documentation inventory: %w", err)
		}
		if previous.Format != format {
			return fmt.Errorf("output directory belongs to format %s; choose a separate directory for %s", previous.Format, format)
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	next := inventory{Format: format, Files: map[string]string{}}
	var names []string
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		file, err := safePath(root, name)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(file)
		if err == nil {
			if scaffold[name] {
				files[name] = data
			} else if digest(data) != digest(files[name]) && (previous.Files[name] == "" || digest(data) != previous.Files[name]) {
				return fmt.Errorf("refusing to overwrite edited or unrelated file %s; move it or restore the generated version", file)
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		next.Files[name] = digest(files[name])
	}
	var obsolete []string
	for name, hash := range previous.Files {
		if _, ok := files[name]; ok {
			continue
		}
		file, err := safePath(root, name)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(file)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if digest(data) != hash {
			return fmt.Errorf("refusing to remove edited generated file %s", file)
		}
		obsolete = append(obsolete, file)
	}
	for _, name := range names {
		file, _ := safePath(root, name)
		if err := os.MkdirAll(filepath.Dir(file), 0755); err != nil {
			return err
		}
		temp, err := os.CreateTemp(filepath.Dir(file), ".goalchemy-docs-*")
		if err != nil {
			return err
		}
		tempName := temp.Name()
		if _, err = temp.Write(files[name]); err == nil {
			err = temp.Chmod(0644)
		}
		closeErr := temp.Close()
		if err == nil {
			err = closeErr
		}
		if err == nil {
			err = os.Rename(tempName, file)
		}
		if err != nil {
			os.Remove(tempName)
			return err
		}
	}
	for _, file := range obsolete {
		if err := os.Remove(file); err != nil {
			return err
		}
	}
	data, _ := json.MarshalIndent(next, "", "  ")
	return os.WriteFile(inventoryPath, append(data, '\n'), 0644)
}
