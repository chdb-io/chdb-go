package cli

import (
	"github.com/chdb-io/chdb-go/cli/history"
	"github.com/chdb-io/chdb-go/chdb"
)

// CLI object of cli :)
type CLI struct {
	history *history.History

	Session *chdb.Session
	Multiline               bool
	isMultilineInputStarted bool
	query                   string
}

// New - returns CLI object
func New(sess *chdb.Session, history *history.History, multiline bool) *CLI {
	return &CLI{
		history:   history,
		Session:   sess,
		Multiline: multiline,

		isMultilineInputStarted: false,
	}
}
