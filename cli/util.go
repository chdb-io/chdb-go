package cli

import (
	"context"
	"strings"
	"io/ioutil"
)

// GetCurrentDB from chDB
func (c *CLI) GetCurrentDB(ctx context.Context) string {
	// read current database from path "c.Session.Path()/default_database"
	filePath := c.Session.Path() + "/default_database"
	content, err := ioutil.ReadFile(filePath)
	if err != nil {
		return ""
	}

	return strings.TrimSpace(string(content))
}
