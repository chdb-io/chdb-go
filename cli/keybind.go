package cli

import (
	"github.com/c-bata/go-prompt"
)

// MultilineControl is a multiline toggle.
func (c *CLI) MultilineControl(buffer *prompt.Buffer) {
	if c.isMultilineInputStarted {
		return
	}

	c.Multiline = !c.Multiline
}
