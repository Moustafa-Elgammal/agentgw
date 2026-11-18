package bridge

import "os"

func osPipe() (*os.File, *os.File, error) { return os.Pipe() }

// swapStdout replaces os.Stdout and returns a function that restores it.
func swapStdout(w *os.File) func() {
	old := os.Stdout
	os.Stdout = w
	return func() { os.Stdout = old }
}
