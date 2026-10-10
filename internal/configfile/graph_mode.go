package configfile

import "fmt"

const (
	GraphModeDependency = "dependency"
	GraphModeLink       = "link"
)

// GetGraphMode resolves the persisted workspace model. Unknown values must not
// fall back to the Issue store: they may describe a format this client cannot
// safely open. A missing config or marker retains the historical default.
func (c *Config) GetGraphMode() (string, error) {
	if c == nil || c.GraphMode == "" {
		return GraphModeDependency, nil
	}
	switch c.GraphMode {
	case GraphModeDependency, GraphModeLink:
		return c.GraphMode, nil
	default:
		return "", fmt.Errorf("unsupported workspace graph_mode %q; use a compatible bd build without changing the workspace marker", c.GraphMode)
	}
}
