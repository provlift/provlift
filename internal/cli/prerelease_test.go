package cli

import (
	"encoding/json"
	"testing"
)

func TestPrereleaseOptInIsConsistentAcrossCheckAndUpgrade(t *testing.T) {
	// The only newer release is a prerelease. check and upgrade must agree on
	// whether it exists: both ignore it by default and both see it when asked.
	for _, fixture := range []struct {
		name   string
		locked string
	}{
		{"locked root", "4.65.0"},
		{"unlocked shared module", ""},
	} {
		for _, includePrerelease := range []bool{false, true} {
			h := newDriftHarness(t, map[string]map[string]string{
				".": moduleFiles("hashicorp/azurerm", ">= 4.0.0", fixture.locked),
			})
			h.fake.versions = map[string][]string{"hashicorp/azurerm": {"4.65.0", "5.0.0-beta.1"}}
			var flags []string
			if includePrerelease {
				flags = []string{"--include-prerelease"}
			}

			if err := h.runWithGlobalFlags(newCheckCommand(h.opts), append([]string{"--json"}, flags...)...); err != nil {
				t.Fatalf("%s: check: %v", fixture.name, err)
			}
			var checked struct {
				Providers []struct {
					Latest    string `json:"latest"`
					Available string `json:"available_upgrade"`
				} `json:"providers"`
			}
			if err := json.Unmarshal(h.stdout.Bytes(), &checked); err != nil || len(checked.Providers) != 1 {
				t.Fatalf("%s: parsing check output: %v\n%s", fixture.name, err, h.stdout.String())
			}

			h.stdout.Reset()
			arguments := append([]string{"--json", "--level", "major", "--allow-major", "--dry-run"}, flags...)
			if err := h.runWithGlobalFlags(newUpgradeCommand(h.opts), arguments...); err != nil {
				t.Fatalf("%s: upgrade: %v", fixture.name, err)
			}
			var planned struct {
				Plan struct {
					Actions []struct {
						To string `json:"to"`
					} `json:"actions"`
				} `json:"plan"`
			}
			if err := json.Unmarshal(h.stdout.Bytes(), &planned); err != nil {
				t.Fatalf("%s: parsing upgrade output: %v\n%s", fixture.name, err, h.stdout.String())
			}

			wantLatest, wantAvailable, wantActions := "4.65.0", "none", 0
			if includePrerelease {
				wantLatest, wantAvailable, wantActions = "5.0.0-beta.1", "major", 1
			}
			provider := checked.Providers[0]
			if provider.Latest != wantLatest || provider.Available != wantAvailable {
				t.Errorf("%s (include=%t): check reported latest %s, upgrade %s; want %s, %s",
					fixture.name, includePrerelease, provider.Latest, provider.Available, wantLatest, wantAvailable)
			}
			if len(planned.Plan.Actions) != wantActions {
				t.Errorf("%s (include=%t): upgrade planned %d action(s), want %d",
					fixture.name, includePrerelease, len(planned.Plan.Actions), wantActions)
			}
			if wantActions == 1 && planned.Plan.Actions[0].To != wantLatest {
				t.Errorf("%s: upgrade planned %s but check reported %s", fixture.name, planned.Plan.Actions[0].To, wantLatest)
			}
		}
	}
}
