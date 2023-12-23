package cli

// MultilineCLIPrefix shows that multiline input is activated
const MultilineCLIPrefix = ":-] "

// GetLivePrefixState - returns prefix and multiline input current state (true/false)
func (c *CLI) GetLivePrefixState() (string, bool) {
	return MultilineCLIPrefix, c.isMultilineInputStarted
}
