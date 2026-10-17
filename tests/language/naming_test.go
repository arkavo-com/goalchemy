package language

import (
	"testing"

	"github.com/eugenioenko/goalchemy/internal/driver"
	"github.com/eugenioenko/goalchemy/internal/testutil"
)

// Every mode must preserve package/type identity, shadowing, anonymous value
// semantics, closures, and cooperative frame behavior against the Go oracle.
func TestNamingModes(t *testing.T) {
	selected := targets
	if testing.Short() {
		selected = selected[:2]
	}
	fixtures, err := testutil.Discover("testdata")
	if err != nil {
		t.Fatal(err)
	}
	wanted := map[string]bool{"naming": true, "structs_values": true, "co_frames": true, "shadow_scopes": true, "multipkg": true}
	for _, fixture := range fixtures {
		if !wanted[fixture.Name] {
			continue
		}
		delete(wanted, fixture.Name)
		for _, mode := range []string{"source", "readable", "compact"} {
			t.Run(fixture.Name+"/"+mode, func(t *testing.T) {
				testutil.RunFixtureOptions(t, fixture, selected, driver.EmitOptions{Names: mode})
			})
		}
	}
	if len(wanted) != 0 {
		t.Fatalf("missing required naming fixtures: %v", wanted)
	}
}
