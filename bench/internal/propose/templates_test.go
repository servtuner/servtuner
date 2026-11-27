package propose

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/servtuner/servtuner/bench/internal/spec"
)

// Every starting search space must start a session: servtuner propose accepts its space and
// proposes a first step. One that fits a host may not rank per GPU (a host's runs have no GPU
// count, so the api refuses that objective there).
func TestEveryStartingSpaceStartsASession(t *testing.T) {
	templates, err := spec.SearchSpaceTemplates()
	if err != nil {
		t.Fatal(err)
	}
	if len(templates) < 4 {
		t.Fatalf("want the catalog's four starting spaces at least, got %d", len(templates))
	}
	for _, template := range templates {
		document, err := json.Marshal(template.Space)
		if err != nil {
			t.Fatal(err)
		}
		space, err := ParseSearchSpace(document)
		if err != nil {
			t.Errorf("%s: %v", template.Name, err)
			continue
		}
		first, err := Propose(space, BootResults{}, nil)
		if err != nil || first.Status != spec.ProposalStatusPropose || len(first.Proposals) == 0 {
			t.Errorf("%s: no first step: %+v, %v", template.Name, first, err)
		}
		fitsHost := slices.Contains(template.Fits.Placements, spec.SearchSpaceTemplateFitsPlacementsElemHost)
		if fitsHost && template.Space.Objective != nil && space.Objective != spec.ObjectiveNameSloMaxRps {
			t.Errorf("%s fits a host but ranks by %s", template.Name, space.Objective)
		}
	}
}
