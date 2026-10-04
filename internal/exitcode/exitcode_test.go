package exitcode

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"testing"
)

type coded struct{}

func (coded) Error() string { return "coded" }
func (coded) ExitCode() int { return BuildFailed }

type netTimeout struct{}

func (netTimeout) Error() string   { return "i/o timeout" }
func (netTimeout) Timeout() bool   { return true }
func (netTimeout) Temporary() bool { return false }

func TestCode(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{"nil", nil, OK},
		{"plain", errors.New("boom"), Failure},
		{"usage", Usagef("pass --%s", "yes"), Usage},
		{"tag survives wrapping", fmt.Errorf("outer: %w", With(Auth, errors.New("no login"))), Auth},
		{"ExitCode method", fmt.Errorf("build failed: %w", coded{}), BuildFailed},
		{"canceled", fmt.Errorf("upload: %w", context.Canceled), Interrupted},
		{"deadline", fmt.Errorf("wait: %w", context.DeadlineExceeded), Timeout},
		{"network timeout", &url.Error{Op: "Get", URL: "https://x", Err: netTimeout{}}, Timeout},
		{"outer tag wins", With(Usage, fmt.Errorf("x: %w", context.DeadlineExceeded)), Usage},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Code(tt.err); got != tt.want {
				t.Errorf("Code(%v) = %d, want %d", tt.err, got, tt.want)
			}
		})
	}
}

func TestWrapKeepsMessage(t *testing.T) {
	inner := errors.New("not authenticated")
	err := With(Auth, inner)
	if err.Error() != inner.Error() || !errors.Is(err, inner) {
		t.Fatalf("With changed the error: %v", err)
	}
	if With(Auth, nil) != nil {
		t.Fatal("With(nil) is not nil")
	}
}
