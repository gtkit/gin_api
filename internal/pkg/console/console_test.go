package console

import (
	"bytes"
	"errors"
	"testing"
)

func TestSuccessWritesColoredOutput(t *testing.T) {
	buf := &bytes.Buffer{}
	restoreOutput := setConsoleOutputForTest(buf)
	defer restoreOutput()

	Success("hello")

	if buf.Len() == 0 {
		t.Fatal("expected success output to be written")
	}
}

func TestExitIfUsesExitHook(t *testing.T) {
	restoreOutput := setConsoleOutputForTest(&bytes.Buffer{})
	defer restoreOutput()

	exited := false
	code := 0
	restoreExit := setConsoleExitForTest(func(status int) {
		exited = true
		code = status
	})
	defer restoreExit()

	ExitIf(errors.New("boom"))

	if !exited {
		t.Fatal("expected exit hook to be called")
	}
	if code != 1 {
		t.Fatalf("expected exit code 1, got %d", code)
	}
}
