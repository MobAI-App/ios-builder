package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
)

func TestProviderTokenPromptRequiresTerminal(t *testing.T) {
	_, err := readProviderToken(context.Background(), strings.NewReader("secret"), io.Discard)
	if err == nil || !strings.Contains(err.Error(), "--token-stdin") {
		t.Fatalf("expected explicit pipe instructions, got %v", err)
	}
}

// Run only in the child attached to the pseudo-terminal below. No provider
// requests or credential writes occur in these prompt regression tests.
func TestProviderTokenPromptChild(t *testing.T) {
	mode := os.Getenv("BUILDER_PROMPT_TEST_MODE")
	if mode == "" {
		return
	}
	token, err := readProviderToken(context.Background(), os.Stdin, os.Stderr)
	if mode == "interrupt" {
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected cancellation, got %v", err)
		}
		return
	}
	if mode == "ctrlc" || mode == "eof" {
		if !errors.Is(err, io.EOF) {
			t.Fatalf("expected interrupted input, got %v", err)
		}
		return
	}
	if err != nil {
		t.Fatal(err)
	}
	expected := ""
	if mode == "paste" {
		expected = strings.Repeat("test-token-", 30)
	}
	if token != expected {
		t.Fatal("pasted token was changed")
	}
}

func TestProviderTokenPromptPTY(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("PTY regression uses Unix terminal APIs")
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("PTY regression requires python3")
	}
	for _, mode := range []string{"paste", "empty", "ctrlc", "eof", "interrupt"} {
		t.Run(mode, func(t *testing.T) {
			cmd := exec.Command(python, "-c", providerTokenPTYTest, os.Args[0], mode)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("PTY regression failed: %v\n%s", err, out)
			}
		})
	}
}

const providerTokenPTYTest = `
import fcntl, os, pty, select, signal, struct, subprocess, sys, termios, time
master, slave = pty.openpty()
# A long paste in a narrow terminal reproduced the original rendering issue.
fcntl.ioctl(slave, termios.TIOCSWINSZ, struct.pack('HHHH', 24, 40, 0, 0))
original = termios.tcgetattr(slave)
mode = sys.argv[2]
env = dict(os.environ, BUILDER_PROMPT_TEST_MODE=mode)
p = subprocess.Popen([sys.argv[1], '-test.run=^TestProviderTokenPromptChild$'],
                     stdin=slave, stdout=slave, stderr=slave, env=env)
output = b''
deadline = time.monotonic() + 15
def receive():
    global output
    if select.select([master], [], [], .02)[0]:
        output += os.read(master, 65536)
try:
    while b'API token (' not in output or termios.tcgetattr(slave)[3] & termios.ECHO:
        assert time.monotonic() < deadline, 'prompt did not become ready'
        receive()
    if mode == 'interrupt':
        p.send_signal(signal.SIGINT)
    elif mode == 'ctrlc':
        os.write(master, b'\x03')
    elif mode == 'eof':
        os.write(master, b'\x04')
    else:
        payload = b'test-token-' * 30 if mode == 'paste' else b''
        os.write(master, payload + b'\r')
    while p.poll() is None:
        assert time.monotonic() < deadline, 'prompt did not finish'
        receive()
    receive()
    assert p.returncode == 0, output.decode(errors='replace')
    assert output.count(b'API token (') == 1, 'prompt was redrawn'
    assert b'test-token-' not in output, 'token was echoed'
    assert b'*' not in output, 'masked input was redrawn'
    restored = termios.tcgetattr(slave)
    # macOS may set the kernel-managed pending-input bit on raw -> canonical.
    pending = getattr(termios, 'PENDIN', 0)
    restored[3] &= ~pending
    original[3] &= ~pending
    assert restored == original, 'terminal settings not restored'
finally:
    if p.poll() is None:
        p.kill()
    p.wait()
    os.close(master)
    os.close(slave)
`
