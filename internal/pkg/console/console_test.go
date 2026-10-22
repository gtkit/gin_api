package console

import (
	"bytes"
	"context"
	"errors"
	"testing"

	applog "github.com/gtkit/gin-api/internal/pkg/log"
)

type failedConsoleWriter struct{ cause error }

func (w failedConsoleWriter) Write([]byte) (int, error) { return 0, w.cause }

type consoleFailureLog struct{ entries []applog.LogEntry }

func (l *consoleFailureLog) Log(_ context.Context, entry applog.LogEntry) {
	l.entries = append(l.entries, entry)
}

func TestConsoleFailureReachesExistingLogger(t *testing.T) {
	cause := errors.New("console unavailable")
	restore := setConsoleOutputForTest(failedConsoleWriter{cause})
	defer restore()
	logs := &consoleFailureLog{}
	release, err := applog.Install(logs, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	Success("accepted")
	if len(logs.entries) != 1 || logs.entries[0].Level != applog.LevelError {
		t.Fatalf("entries=%v", logs.entries)
	}
	for _, field := range logs.entries[0].Fields {
		if err, ok := field.Value.(error); ok && errors.Is(err, cause) {
			return
		}
	}
	t.Fatal("console failure cause missing from terminal log")
}

// TestColoredOutputBytes 锁定三个打印函数的**逐字节**输出。
//
// 期望值是替换 `github.com/mgutz/ansi` 之前实测得到的字节：
//
//	ansi.Color("MSG", "green")  == "\x1b[0;32mMSG\x1b[0m"
//	ansi.Color("MSG", "red")    == "\x1b[0;31mMSG\x1b[0m"
//	ansi.Color("MSG", "yellow") == "\x1b[0;33mMSG\x1b[0m"
//
// 注意 `0;` 前缀（先复位再设色）。裸 `\x1b[32m` 在终端上看不出差别，但对任何
// 断言输出字节的调用方都不等价——自实现替换第三方库时，正是这种"看起来一样"
// 的差异最容易溜过去。
//
// 此前这里只断言 `buf.Len() != 0`：色码写错、甚至消息内容写错，它都照样通过。
// 「输出是彩色的」是本包唯一的契约（三个函数除颜色外没有任何差异），没有反证
// 测试的契约等于凭空承诺。
func TestColoredOutputBytes(t *testing.T) {
	tests := []struct {
		name  string
		print func(string)
		input string
		want  string
	}{
		{"success is green", Success, "hello", "\x1b[0;32mhello\x1b[0m\n"},
		{"error is red", Error, "boom", "\x1b[0;31mboom\x1b[0m\n"},
		{"warning is yellow", Warning, "careful", "\x1b[0;33mcareful\x1b[0m\n"},
		{"empty message still wrapped", Success, "", "\x1b[0;32m\x1b[0m\n"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf := &bytes.Buffer{}
			restoreOutput := setConsoleOutputForTest(buf)
			defer restoreOutput()

			tt.print(tt.input)

			if got := buf.String(); got != tt.want {
				t.Fatalf("output = %q, want %q", got, tt.want)
			}
		})
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
