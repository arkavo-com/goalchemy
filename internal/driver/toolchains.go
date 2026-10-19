package driver

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ToolEnv returns the environment for running target toolchains: toolchains
// unpacked under a .toolchains directory in the working directory or one of
// its parents are put first on PATH, and the matching environment paths take
// precedence over ambient toolchain settings.
func ToolEnv() []string {
	env := os.Environ()
	tc := ToolchainRoot()
	if tc == "" {
		return env
	}
	return withToolchains(env, tc)
}

// ToolchainRoot finds the nearest repository-local .toolchains directory.
func ToolchainRoot() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		tc := filepath.Join(dir, ".toolchains")
		if st, err := os.Stat(tc); err == nil && st.IsDir() {
			return tc
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

func withToolchains(env []string, tc string) []string {
	var bins []string
	if isDirectory(filepath.Join(tc, "binaryen", "bin")) {
		bins = append(bins, filepath.Join(tc, "binaryen", "bin"))
	}
	if isDirectory(filepath.Join(tc, "tinygo", "bin")) {
		bins = append(bins, filepath.Join(tc, "tinygo", "bin"))
	}
	set := map[string]string{}
	if native := filepath.Join(tc, "pkgconfig"); isDirectory(native) {
		set["PKG_CONFIG_PATH"] = native
		for _, entry := range env {
			if existing, ok := strings.CutPrefix(entry, "PKG_CONFIG_PATH="); ok && existing != "" {
				set["PKG_CONFIG_PATH"] += string(os.PathListSeparator) + existing
				break
			}
		}
	}
	if swift, _ := filepath.Glob(filepath.Join(tc, "swift-*")); len(swift) > 0 {
		sort.Strings(swift)
		for i := len(swift) - 1; i >= 0; i-- {
			selected := swift[i]
			if st, err := os.Stat(filepath.Join(selected, "usr", "bin", "swiftc")); err == nil && !st.IsDir() {
				bins = append(bins, filepath.Join(selected, "usr", "bin"))
				break
			}
		}
	}
	libDir := filepath.Join(tc, "libtinfo5", "lib", "x86_64-linux-gnu")
	if _, err := os.Stat(filepath.Join(libDir, "libtinfo.so.5")); err == nil {
		set["LD_LIBRARY_PATH"] = libDir
	} else {
		libDir = ""
	}
	if jdks, _ := filepath.Glob(filepath.Join(tc, "jdk-*")); len(jdks) > 0 {
		sort.Strings(jdks)
		j := jdks[len(jdks)-1]
		bins = append(bins, filepath.Join(j, "bin"))
		set["JAVA_HOME"] = j
	}
	if st, err := os.Stat(filepath.Join(tc, "dotnet", "dotnet")); err == nil && !st.IsDir() {
		d := filepath.Join(tc, "dotnet")
		bins = append(bins, d)
		set["DOTNET_ROOT"] = d
	}
	if st, err := os.Stat(filepath.Join(tc, "bdwgc", "lib", "libgc.a")); err == nil && !st.IsDir() {
		set["GOALCHEMY_BDWGC"] = filepath.Join(tc, "bdwgc")
	}
	if len(bins) == 0 && len(set) == 0 {
		return env
	}
	var out []string
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		switch {
		case k == "PATH" && len(bins) > 0:
			kv = "PATH=" + strings.Join(bins, string(os.PathListSeparator)) + string(os.PathListSeparator) + v
		case k == "LD_LIBRARY_PATH" && libDir != "":
			kv = "LD_LIBRARY_PATH=" + libDir + string(os.PathListSeparator) + v
			delete(set, k)
		case set[k] != "":
			kv = k + "=" + set[k]
			delete(set, k)
		}
		out = append(out, kv)
	}
	for k, v := range set {
		out = append(out, k+"="+v)
	}
	return out
}

func isDirectory(path string) bool {
	st, err := os.Stat(path)
	return err == nil && st.IsDir()
}
