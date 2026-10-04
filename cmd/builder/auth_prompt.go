package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"golang.org/x/term"
)

// Hidden terminal input avoids line-editor redraws and wrapping when pasting
// long API tokens. Pipes must explicitly opt in with --token-stdin.
func readProviderToken(ctx context.Context, input io.Reader, output io.Writer) (string, error) {
	return readHidden(ctx, input, output, "API token", "--token-stdin")
}

// readHidden reads one value from the terminal without echoing it. what names
// the value in the prompt and errors; stdinFlag is the flag that takes it from
// a pipe instead.
func readHidden(ctx context.Context, input io.Reader, output io.Writer, what, stdinFlag string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("%s input canceled: %w", what, err)
	}
	file, ok := input.(*os.File)
	if !ok || !term.IsTerminal(int(file.Fd())) {
		return "", fmt.Errorf("%s input requires a terminal; use %s for piped input", what, stdinFlag)
	}
	fd := int(file.Fd())
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	state, err := term.MakeRaw(fd)
	if err != nil {
		return "", fmt.Errorf("configure terminal input: %w", err)
	}
	// Only this goroutine changes OS terminal settings. The input goroutine
	// cannot disable echo after cancellation restores it. A canceled read may
	// stay blocked until the CLI exits; do not reuse stdin after cancellation.
	defer func() {
		_ = term.Restore(fd, state)
		fmt.Fprintln(output)
	}()
	fmt.Fprintf(output, "%s (input hidden; paste once, then press Enter): ", what)
	type result struct {
		value string
		err   error
	}
	done := make(chan result, 1)
	go func() {
		// Terminal handles paste, backspace, and Ctrl+C/Ctrl+D in raw mode.
		// Discard its rendering so only our single static prompt is displayed.
		terminal := term.NewTerminal(struct {
			io.Reader
			io.Writer
		}{file, io.Discard}, "")
		value, err := terminal.ReadPassword("")
		done <- result{value, err}
	}()
	select {
	case <-ctx.Done():
		return "", fmt.Errorf("%s input canceled: %w", what, ctx.Err())
	case r := <-done:
		if r.err != nil {
			return "", fmt.Errorf("read %s: %w", what, r.err)
		}
		return r.value, nil
	}
}
