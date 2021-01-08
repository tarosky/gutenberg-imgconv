package testutil

import (
	"os"
	"path"
	"runtime"
)

// InitTest moves working directory to project root directory.
// https://brandur.org/fragments/testing-go-project-root
func InitTest() {
	_, filename, _, _ := runtime.Caller(0)
	dir := path.Join(path.Dir(filename), "..")
	err := os.Chdir(dir)
	if err != nil {
		panic(err)
	}
}
