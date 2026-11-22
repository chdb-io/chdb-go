package staticbundle

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
)

type BindingVersions struct {
	Bindings string `json:"bindings"`
	Arrow    string `json:"arrow"`
}

type PublicationPlan struct {
	Schema        int    `json:"schema"`
	SiteOrigin    string `json:"site_origin"`
	Prefix        string `json:"module_prefix"`
	Engine        string `json:"engine"`
	StaticVersion string `json:"static_version"`
	Validation    struct {
		Prefix string `json:"module_prefix"`
		BindingVersions
	} `json:"validation"`
	Release BindingVersions `json:"release"`
}

var arrowVersionRE = regexp.MustCompile(`^v[01]\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-rc\.[1-9][0-9]*)?$`)
var rcSuffixRE = regexp.MustCompile(`^rc\.[1-9][0-9]*$`)

func ReadPublicationPlan(filename string, release Release) (PublicationPlan, error) {
	var p PublicationPlan
	b, err := os.ReadFile(filename)
	if err != nil {
		return p, err
	}
	if err = json.Unmarshal(b, &p); err != nil {
		return p, err
	}
	v, err := engVersion(release)
	if err != nil {
		return p, err
	}
	if p.Schema != 1 || p.Engine != release.Engine || p.StaticVersion != v || ValidatePrefix(p.Prefix) != nil || p.SiteOrigin != "https://"+strings.Split(p.Prefix, "/")[0] {
		return p, fmt.Errorf("publication identity must match the pinned engine, static version and HTTPS module hostname")
	}
	if ValidatePrefix(p.Validation.Prefix) != nil || p.Validation.Prefix == p.Prefix || !strings.HasPrefix(p.Validation.Prefix, "static-ci.example/") {
		return p, fmt.Errorf("validation must use a separate, non-public static-ci.example namespace")
	}
	if !bindingRE.MatchString(p.Release.Bindings) || strings.Contains(p.Release.Bindings, "-") || !arrowVersionRE.MatchString(p.Release.Arrow) || strings.Contains(p.Release.Arrow, "-") {
		return p, fmt.Errorf("release bindings and Arrow versions must be canonical stable versions")
	}
	rootRC := strings.TrimPrefix(p.Validation.Bindings, p.Release.Bindings+"-")
	arrowRC := strings.TrimPrefix(p.Validation.Arrow, p.Release.Arrow+"-")
	if !strings.HasPrefix(p.Validation.Bindings, p.Release.Bindings+"-") || !rcSuffixRE.MatchString(rootRC) || !strings.HasPrefix(p.Validation.Arrow, p.Release.Arrow+"-") || !rcSuffixRE.MatchString(arrowRC) {
		return p, fmt.Errorf("validation versions must be RCs of the selected stable releases")
	}
	return p, nil
}

// Phases: validation snapshots the checkout under the planned RC versions in
// the non-public CI namespace. public-rc publishes those same RC versions at the
// permanent namespace, and only for a prerelease static version, so it never
// takes the stable identity. release publishes the stable versions, and only
// for a stable engine.
const (
	PhaseValidation = "validation"
	PhasePublicRC   = "public-rc"
	PhaseRelease    = "release"
)

func (p PublicationPlan) Select(phase string) (string, BindingVersions, error) {
	prerelease := strings.Contains(p.StaticVersion, "-")
	switch phase {
	case PhaseValidation:
		return p.Validation.Prefix, p.Validation.BindingVersions, nil
	case PhasePublicRC:
		if !prerelease {
			return "", BindingVersions{}, fmt.Errorf("public-rc needs a prerelease static version; %s is the stable identity", p.StaticVersion)
		}
		return p.Prefix, p.Validation.BindingVersions, nil
	case PhaseRelease:
		if prerelease {
			return "", BindingVersions{}, fmt.Errorf("release needs a stable engine; %s pins prerelease %s", p.Engine, p.StaticVersion)
		}
		return p.Prefix, p.Release, nil
	default:
		return "", BindingVersions{}, fmt.Errorf("phase must be validation, public-rc or release")
	}
}

// Public reports whether a phase publishes at the permanent namespace.
func Public(phase string) bool { return phase == PhasePublicRC || phase == PhaseRelease }

func (p PublicationPlan) CheckBundle(b Bundle, phase string) error {
	prefix, versions, err := p.Select(phase)
	if err != nil {
		return err
	}
	if b.Prefix != prefix || b.Bindings != versions.Bindings || b.Engine != p.Engine || b.Version != p.StaticVersion {
		return fmt.Errorf("bundle differs from selected %s publication plan; do not publish a validation snapshot or RC under another identity", phase)
	}
	return nil
}
