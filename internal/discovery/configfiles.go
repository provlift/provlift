package discovery

import (
	"os"
	"sort"
	"strings"

	"github.com/provlift/provlift/internal/model"
)

// configFiles is the configuration a directory contributes, as Terraform or
// OpenTofu would load it.
type configFiles struct {
	// HCL lists native-syntax files, sorted by name.
	HCL []string
	// JSON lists JSON-syntax files, which provlift does not read.
	JSON []string
	// IgnoredTofu lists .tofu and .tofu.json files left out because the
	// configured binary is Terraform, which never loads them.
	IgnoredTofu []string
}

// openTofuHost reports whether registryHost is the one an OpenTofu binary
// selects (see model.DefaultRegistryHost), which is how the scan knows the
// configured binary.
func openTofuHost(registryHost string) bool {
	return registryHost == model.OpenTofuRegistryHost
}

// selectConfigFiles picks the configuration files from a directory listing.
// Terraform loads .tf and .tf.json. OpenTofu also loads .tofu and .tofu.json,
// and X.tofu shadows X.tf (X.tofu.json shadows X.tf.json), so the shadowed
// file is skipped as OpenTofu skips it.
func selectConfigFiles(entries []os.DirEntry, openTofu bool) configFiles {
	present := map[string]bool{}
	for _, e := range entries {
		if !e.IsDir() {
			present[e.Name()] = true
		}
	}

	var out configFiles
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		switch {
		case strings.HasSuffix(name, ".tofu.json"), strings.HasSuffix(name, ".tofu"):
			if !openTofu {
				out.IgnoredTofu = append(out.IgnoredTofu, name)
			} else if strings.HasSuffix(name, ".json") {
				out.JSON = append(out.JSON, name)
			} else {
				out.HCL = append(out.HCL, name)
			}
		case strings.HasSuffix(name, ".tf.json"):
			if !openTofu || !present[strings.TrimSuffix(name, ".tf.json")+".tofu.json"] {
				out.JSON = append(out.JSON, name)
			}
		case strings.HasSuffix(name, ".tf"):
			if !openTofu || !present[strings.TrimSuffix(name, ".tf")+".tofu"] {
				out.HCL = append(out.HCL, name)
			}
		}
	}
	sort.Strings(out.HCL)
	sort.Strings(out.JSON)
	sort.Strings(out.IgnoredTofu)
	return out
}
