package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/MobAI-App/ios-builder/internal/release"
)

func TestReleaseErrorTellsBuildTimeoutFromASCWait(t *testing.T) {
	deadline := fmt.Errorf("build failed: %w", context.DeadlineExceeded)
	err := releaseError(&release.Result{BuildNumber: "3"}, deadline, 5*time.Minute)
	if errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "did not finish within 5m0s") {
		t.Errorf("build deadline = %v", err)
	}
	// After the IPA exists the deadline is the App Store Connect wait; finish explains that one.
	if err := releaseError(&release.Result{IPAPath: "dist/app.ipa"}, deadline, time.Minute); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("ASC deadline = %v", err)
	}
	other := errors.New("runner exploded")
	if err := releaseError(nil, other, time.Minute); err != other {
		t.Errorf("other error = %v", err)
	}
}
