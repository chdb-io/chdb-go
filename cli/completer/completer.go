package completer

import "github.com/c-bata/go-prompt"

// Completer object
type Completer struct {
}

// New - returns completer object
func New() *Completer {
	return &Completer{}
}

// Complete - returns suggestions for input
func (c *Completer) Complete(d prompt.Document) []prompt.Suggest {
	return []prompt.Suggest{}
}
