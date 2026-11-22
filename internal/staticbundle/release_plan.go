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

func (p PublicationPlan) Select(phase string) (string, BindingVersions, error) {
	switch phase {
	case "validation":
		return p.Validation.Prefix, p.Validation.BindingVersions, nil
	case "release":
		return p.Prefix, p.Release, nil
	default:
		return "", BindingVersions{}, fmt.Errorf("phase must be validation or release")
	}
}

func (p PublicationPlan) CheckBundle(b Bundle, phase string) error {
	prefix, versions, err := p.Select(phase)
	if err != nil {
		return err
	}
	if b.Prefix != prefix || b.Bindings != versions.Bindings || b.Engine != p.Engine || b.Version != p.StaticVersion {
		return fmt.Errorf("bundle differs from selected %s publication plan; do not publish a validation snapshot under a stable identity", phase)
	}
	return nil
}
