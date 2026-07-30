package logger

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type namedSecret string

type structuredSecret struct {
	Value string
}

func TestNewRetainsThreeArgumentFunctionSignature(t *testing.T) {
	var constructor func(string, string, bool) (*Logger, error) = New
	if constructor == nil {
		t.Fatal("New constructor is nil")
	}
}

func TestLoggerRedactsSecretFromMessageAndStructuredArgs(t *testing.T) {
	const token = "000000:synthetic-token-for-tests"
	log := newTestLogger(t, token)

	child := log.With(
		"with_string", "prefix"+token+"suffix",
		slog.String("with_attr", "prefix"+token+"suffix"),
	)
	child.Error(
		"request "+token,
		"string", "prefix"+token+"suffix",
		"error", fmt.Errorf("wrapped: prefix%ssuffix", token),
		slog.String("attr", "prefix"+token+"suffix"),
		slog.Group("group", slog.String("nested", "prefix"+token+"suffix")),
	)

	output := closeAndReadLog(t, log)
	if strings.Contains(output, token) {
		t.Fatalf("logger leaked synthetic secret: %q", output)
	}
	for _, want := range []string{
		"request [REDACTED]",
		"prefix[REDACTED]suffix",
		"wrapped: prefix[REDACTED]suffix",
	} {
		if !strings.Contains(output, want) {
			t.Errorf("logger output missing %q: %q", want, output)
		}
	}
	if got := strings.Count(output, "[REDACTED]"); got < 7 {
		t.Errorf("logger redacted %d values, want at least 7: %q", got, output)
	}
}

func TestLoggerIgnoresEmptySecret(t *testing.T) {
	log := newTestLogger(t, "")
	log.Info("message-stays", "value", "value-stays")

	output := closeAndReadLog(t, log)
	if !strings.Contains(output, "message-stays") || !strings.Contains(output, "value-stays") {
		t.Fatalf("logger with empty secret altered message: %q", output)
	}
	if strings.Contains(output, "[REDACTED]") {
		t.Fatalf("logger with empty secret unexpectedly redacted output: %q", output)
	}
}

func TestLoggerWithoutSecretsPreservesJSONStructuredValues(t *testing.T) {
	constructors := []struct {
		name string
		new  func() (*Logger, error)
	}{
		{
			name: "New",
			new: func() (*Logger, error) {
				return New("debug", "json", false)
			},
		},
		{
			name: "NewWithSecretsEmpty",
			new: func() (*Logger, error) {
				return NewWithSecrets("debug", "json", false, "")
			},
		},
	}

	for _, constructor := range constructors {
		t.Run(constructor.name, func(t *testing.T) {
			log := newTestLoggerFromConstructor(t, constructor.new)
			log.Info(
				"structured values",
				"map", map[string]string{"value": "visible"},
				"struct", structuredSecret{Value: "visible"},
			)

			output := closeAndReadLog(t, log)
			var record map[string]any
			if err := json.Unmarshal([]byte(output), &record); err != nil {
				t.Fatalf("Unmarshal() error = %v; output = %q", err, output)
			}
			for _, key := range []string{"map", "struct"} {
				value, ok := record[key].(map[string]any)
				if !ok {
					t.Fatalf("%s value type = %T, want JSON object; output = %q", key, record[key], output)
				}
				if got := value["value"]; got != "visible" {
					if key == "struct" {
						got = value["Value"]
					}
					if got != "visible" {
						t.Fatalf("%s nested value = %v, want visible; output = %q", key, got, output)
					}
				}
			}
		})
	}
}

func TestLoggerDoesNotMutateConcurrentlyReusedGroupAttr(t *testing.T) {
	const token = "000000:synthetic-token-for-tests"
	const original = "prefix" + token + "suffix"
	log := newTestLogger(t, token)
	shared := slog.Group("shared", slog.String("nested", original))

	var workers sync.WaitGroup
	for range 16 {
		workers.Add(1)
		go func() {
			defer workers.Done()
			log.Info("concurrent group", shared)
		}()
	}
	workers.Wait()

	if got := shared.Value.Group()[0].Value.String(); got != original {
		t.Fatalf("logging mutated shared group attr = %q, want %q", got, original)
	}
	if output := closeAndReadLog(t, log); strings.Contains(output, token) {
		t.Fatalf("logger leaked synthetic secret from shared group: %q", output)
	}
}

func TestLoggerRedactsUnhandledAnyValuesInTextAndJSON(t *testing.T) {
	const token = "000000:synthetic-token-for-tests"
	const value = "prefix" + token + "suffix"

	for _, format := range []string{"text", "json"} {
		t.Run(format, func(t *testing.T) {
			log := newTestLoggerWithFormat(t, format, token)
			log.Info(
				"structured values",
				"map", map[string]string{"value": value},
				"struct", structuredSecret{Value: value},
				"named_string", namedSecret(value),
			)

			output := closeAndReadLog(t, log)
			if strings.Contains(output, token) {
				t.Fatalf("%s logger leaked synthetic secret from KindAny: %q", format, output)
			}
			if got := strings.Count(output, "prefix[REDACTED]suffix"); got < 3 {
				t.Fatalf("%s logger redacted %d KindAny values, want at least 3: %q", format, got, output)
			}
		})
	}
}

func newTestLogger(t *testing.T, secrets ...string) *Logger {
	t.Helper()
	return newTestLoggerWithFormat(t, "text", secrets...)
}

func newTestLoggerWithFormat(t *testing.T, format string, secrets ...string) *Logger {
	t.Helper()
	return newTestLoggerFromConstructor(t, func() (*Logger, error) {
		return NewWithSecrets("debug", format, false, secrets...)
	})
}

func newTestLoggerFromConstructor(t *testing.T, constructor func() (*Logger, error)) *Logger {
	t.Helper()

	previousWorkingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd() error = %v", err)
	}
	if err := os.Chdir(t.TempDir()); err != nil {
		t.Fatalf("Chdir() error = %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(previousWorkingDirectory); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})

	log, err := constructor()
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return log
}

func closeAndReadLog(t *testing.T, log *Logger) string {
	t.Helper()

	if err := log.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	matches, err := filepath.Glob(filepath.Join("log", "*.log"))
	if err != nil {
		t.Fatalf("Glob() error = %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("log files = %v, want exactly one", matches)
	}
	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	return string(data)
}
