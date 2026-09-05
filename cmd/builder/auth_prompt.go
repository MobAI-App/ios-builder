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
	if err := ctx.Err(); err != nil {
		return "", fmt.Errorf("API token input canceled: %w", err)
	}
	file, ok := input.(*os.File)
	if !ok || !term.IsTerminal(int(file.Fd())) {
		return "", fmt.Errorf("API token input requires a terminal; use --token-stdin for piped input")
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
	fmt.Fprint(output, "API token (input hidden; paste once, then press Enter): ")
	type result struct {
		token string
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
		token, err := terminal.ReadPassword("")
		done <- result{token, err}
	}()
	select {
	case <-ctx.Done():
		return "", fmt.Errorf("API token input canceled: %w", ctx.Err())
	case r := <-done:
		if r.err != nil {
			return "", fmt.Errorf("read API token: %w", r.err)
		}
		return r.token, nil
	}
}
