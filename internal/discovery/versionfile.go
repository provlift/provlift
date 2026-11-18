package discovery

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
)

// Version file names read by tfenv/tenv (.terraform-version) and
// tofuenv/tenv (.opentofu-version).
const (
	TerraformVersionFile = ".terraform-version"
	OpenTofuVersionFile  = ".opentofu-version"
)

// findVersionFile returns the version named by the nearest version file at or
// above dir, never looking above root, and that file's path relative to root.
// The nearest directory wins; when one directory holds both files, openTofu
// decides which one applies. An empty or unreadable file is skipped.
func findVersionFile(root, dir string, openTofu bool) (version, rel string) {
	names := []string{TerraformVersionFile, OpenTofuVersionFile}
	if openTofu {
		names[0], names[1] = names[1], names[0]
	}

	for {
		for _, name := range names {
			path := filepath.Join(dir, name)
			if v := readVersionFile(path); v != "" {
				return v, filepath.ToSlash(relTo(root, path))
			}
		}
		if dir == root {
			return "", ""
		}
		parent := filepath.Dir(dir)
		if parent == dir || !strings.HasPrefix(dir, root) {
			return "", ""
		}
		dir = parent
	}
}

// readVersionFile returns the first line of a version file that is neither
// blank nor a "#" comment, trimmed.
func readVersionFile(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = f.Close() }()

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		return line
	}
	return ""
}
