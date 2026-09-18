package otainstall

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"
)

// Options configures Run.
type Options struct {
	App     *App
	Backend Backend
	// Log receives progress and the human-readable link; nil discards it.
	Log io.Writer
	// JSON, when set, receives one Links object per mint as NDJSON and turns
	// the prompt and the QR code off.
	JSON io.Writer
	// QR prints the code; QRInvert renders it for a dark-on-light terminal.
	QR, QRInvert bool
	// Once prints one link, leaves the upload in place and returns.
	Once bool
	// Timeout ends the session; zero means an hour.
	Timeout time.Duration
	// Stdin is read for Enter (refresh) and q (quit); nil or EOF disables it.
	Stdin io.Reader
	// Progress reports the IPA upload; nil is silent.
	Progress func(done, total int64)

	refreshLead time.Duration
	now         func() time.Time
}

// Result is what Run reports: the last link and what it left behind.
type Result struct {
	Links
	App       *App     `json:"app"`
	Leftovers []string `json:"leftovers,omitempty"`
}

// Run uploads the app, prints a link, keeps it fresh until the user quits,
// the timeout passes or ctx ends, then removes everything.
func Run(ctx context.Context, opts *Options) (*Result, error) {
	if opts.now == nil {
		opts.now = time.Now
	}
	if opts.refreshLead == 0 {
		opts.refreshLead = time.Minute
	}
	timeout := opts.Timeout
	if timeout <= 0 {
		timeout = time.Hour
	}
	logf(opts.Log, "Uploading %s (%s %s, build %s; profile: %s)", opts.App.Path, opts.App.Title, opts.App.Version, opts.App.Build, opts.App.Profile)
	res := &Result{App: opts.App}
	up, err := opts.Backend.Upload(ctx, opts.App, opts.Progress)
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return res, nil
		}
		return nil, err
	}
	keep := false // --once keeps the upload once its link is out
	defer func() {
		if keep {
			return
		}
		// The session's context may be what ended it; cleanup gets its own.
		cctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
		defer cancel()
		if err := up.Close(cctx); err != nil {
			logf(opts.Log, "Cleanup failed: %v. Run builder ios distribute --cleanup", err)
			res.Leftovers = up.Leftovers()
			return
		}
		logf(opts.Log, "Removed the upload and the manifest.")
	}()

	mint := func() error {
		links, err := up.Mint(ctx, func(ipaURL string) ([]byte, error) { return Manifest(opts.App, ipaURL) })
		if err != nil {
			return err
		}
		res.Links = *links
		return opts.print(res)
	}
	if err := mint(); err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return res, nil
		}
		return res, err
	}
	if opts.Once {
		keep = true
		res.Leftovers = up.Leftovers()
		logf(opts.Log, "Left in place (remove with builder ios distribute --cleanup): %s", strings.Join(res.Leftovers, "; "))
		return res, nil
	}

	keys := make(chan string)
	if opts.Stdin != nil && opts.JSON == nil {
		logf(opts.Log, "Press Enter to refresh the link, q to quit.")
		done := make(chan struct{})
		defer close(done)
		go readKeys(opts.Stdin, keys, done)
	}
	deadline := time.NewTimer(timeout)
	defer deadline.Stop()
	for {
		refresh := time.NewTimer(opts.refreshIn(res.ExpiresAt))
		select {
		case <-ctx.Done():
			refresh.Stop()
			if errors.Is(ctx.Err(), context.Canceled) {
				return res, nil
			}
			return res, ctx.Err()
		case <-deadline.C:
			refresh.Stop()
			logf(opts.Log, "Session timeout (%s) reached.", timeout)
			return res, nil
		case <-refresh.C:
		case k := <-keys:
			refresh.Stop()
			if k == "q" {
				return res, nil
			}
		}
		if err := mint(); err != nil {
			if errors.Is(ctx.Err(), context.Canceled) {
				return res, nil
			}
			return res, err
		}
	}
}

// refreshIn is how long until the link is re-minted: refreshLead before it
// expires, and never less than a second.
func (o *Options) refreshIn(expires time.Time) time.Duration {
	d := expires.Sub(o.now()) - o.refreshLead
	if d < time.Second {
		return time.Second
	}
	return d
}

// print writes the current link as JSON (with the app, so the line carries
// the profile's device count) or as the human block with the QR code.
func (o *Options) print(res *Result) error {
	if o.JSON != nil {
		return json.NewEncoder(o.JSON).Encode(Result{Links: res.Links, App: res.App})
	}
	if o.Log == nil {
		return nil
	}
	links := &res.Links
	fmt.Fprintln(o.Log)
	fmt.Fprintf(o.Log, "Install link (valid until %s):\n%s\n", links.ExpiresAt.Local().Format("15:04:05"), links.Link)
	if o.QR {
		qr, err := QR(links.Link, o.QRInvert)
		if err != nil {
			return fmt.Errorf("render QR code: %w", err)
		}
		fmt.Fprintln(o.Log)
		fmt.Fprint(o.Log, qr)
	}
	fmt.Fprintln(o.Log)
	return nil
}

// readKeys turns lines on r into "" (refresh) or "q" (quit) until EOF or
// done closes; a read blocked on a terminal ends with the process.
func readKeys(r io.Reader, keys chan<- string, done <-chan struct{}) {
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		key := ""
		switch strings.ToLower(strings.TrimSpace(sc.Text())) {
		case "q", "quit", "exit":
			key = "q"
		}
		select {
		case keys <- key:
		case <-done:
			return
		}
		if key == "q" {
			return
		}
	}
}

func logf(w io.Writer, format string, args ...any) {
	if w != nil {
		fmt.Fprintf(w, format+"\n", args...)
	}
}
