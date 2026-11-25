package runnel

// SetMaxMessage sets the limit of a message for a test and returns the function that
// restores it.
func SetMaxMessage(n int) func() {
	old := maxMessage
	maxMessage = n
	return func() { maxMessage = old }
}
