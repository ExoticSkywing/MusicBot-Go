package platform

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestVerificationRequiredErrorKeepsCapabilityOutOfErrorText(t *testing.T) {
	verificationURL := "https://verify.example/challenge?token=one-time-secret"
	expiresAt := time.Now().Add(5 * time.Minute).UTC()
	source := &VerificationRequiredError{URL: verificationURL, ExpiresAt: expiresAt}

	if got := source.Error(); got != "platform: verification required" {
		t.Fatalf("Error() = %q", got)
	}
	if strings.Contains(source.Error(), verificationURL) || strings.Contains(source.Error(), "one-time-secret") {
		t.Fatalf("Error() exposed verification capability: %q", source.Error())
	}

	wrapped := fmt.Errorf("resolve download: %w", source)
	var target *VerificationRequiredError
	if !errors.As(wrapped, &target) {
		t.Fatal("wrapped error did not preserve VerificationRequiredError")
	}
	if target.URL != verificationURL || !target.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("errors.As() target = %#v", target)
	}
}

func TestVerificationRequiredErrorStopsProviderFallback(t *testing.T) {
	err := &VerificationRequiredError{
		URL:       "https://verify.example/challenge",
		ExpiresAt: time.Now().Add(time.Minute),
	}
	if shouldRetry(err) {
		t.Fatal("verification-required errors must wait for user action instead of trying another provider")
	}
}
