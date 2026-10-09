package token

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Where the credential comes from, and -- more importantly -- everything this
// package must never say out loud about it.

const secret = "ak_ZmFrZS1zZWNyZXQtZm9yLXRlc3Rz"

// loaderIn returns a Loader whose config directory is a temporary one and
// whose environment is whatever the test hands it. Nothing here touches the
// real HOME, which differs per platform and belongs to whoever is running the
// suite.
func loaderIn(t *testing.T, env map[string]string) (Loader, string) {
	t.Helper()
	dir := t.TempDir()
	return Loader{
		Getenv:    func(key string) string { return env[key] },
		ConfigDir: func() (string, error) { return dir, nil },
	}, dir
}

func writeTokenFile(t *testing.T, loader Loader, contents string, mode os.FileMode) string {
	t.Helper()
	path, err := loader.Path()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), mode); err != nil {
		t.Fatal(err)
	}
	// WriteFile is subject to the umask, so ask for the mode explicitly.
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestTheEnvironmentVariableIsRead(t *testing.T) {
	loader, _ := loaderIn(t, map[string]string{EnvVar: secret})

	got, err := loader.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Value != secret {
		t.Errorf("value = %q, want the token", got.Value)
	}
	if got.Source != "$"+EnvVar {
		t.Errorf("source = %q, want the variable name", got.Source)
	}
}

// Surrounding whitespace is what a copy-paste and a here-doc both produce.
func TestSurroundingWhitespaceIsTrimmed(t *testing.T) {
	loader, _ := loaderIn(t, map[string]string{EnvVar: "  " + secret + "\n"})

	got, err := loader.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Value != secret {
		t.Errorf("value = %q, want the token with no surrounding whitespace", got.Value)
	}
}

func TestTheTokenFileIsRead(t *testing.T) {
	loader, _ := loaderIn(t, nil)
	path := writeTokenFile(t, loader, secret+"\n", 0o600)

	got, err := loader.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Value != secret {
		t.Errorf("value = %q, want the token", got.Value)
	}
	if got.Source != path {
		t.Errorf("source = %q, want %q", got.Source, path)
	}
	if got.Warning != "" {
		t.Errorf("warning = %q, want none for a 0600 file", got.Warning)
	}
}

// The environment has to win, or a variable set for one invocation could not
// override a file, which is the whole point of having both.
func TestTheEnvironmentWinsOverTheFile(t *testing.T) {
	loader, _ := loaderIn(t, map[string]string{EnvVar: secret})
	path := writeTokenFile(t, loader, "ak_the-file-one", 0o600)

	got, err := loader.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Value != secret {
		t.Errorf("value = %q, want the environment's token", got.Value)
	}
	// And it has to SAY so. Somebody who rotates the file and keeps failing
	// otherwise has no way to see that a shell profile from months ago is what
	// the API is actually rejecting.
	if !strings.Contains(got.Warning, path) || !strings.Contains(got.Warning, EnvVar) {
		t.Errorf("warning = %q, want it to name the ignored file and the variable", got.Warning)
	}
}

// An empty variable is a variable that is not set. Falling through means
// `SPRITESMITH_API_TOKEN= spritesmith ...` does not silently mean "no token" while a
// perfectly good file sits there.
func TestAnEmptyEnvironmentVariableFallsThroughToTheFile(t *testing.T) {
	loader, _ := loaderIn(t, map[string]string{EnvVar: "   "})
	writeTokenFile(t, loader, secret, 0o600)

	got, err := loader.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Value != secret {
		t.Errorf("value = %q, want the file's token", got.Value)
	}
}

// No token anywhere is not an error. A local API with no Clerk configuration
// serves without one, and that is what makes `make cli` work on a laptop with
// nothing set up.
func TestNoTokenAnywhereIsNotAnError(t *testing.T) {
	loader, _ := loaderIn(t, nil)

	got, err := loader.Load()
	if err != nil {
		t.Fatalf("a missing token was an error: %v", err)
	}
	if got.Value != "" || got.Source != "" {
		t.Errorf("got %+v, want an empty token", got)
	}
}

func TestAnEmptyTokenFileIsTreatedAsNoToken(t *testing.T) {
	loader, _ := loaderIn(t, nil)
	writeTokenFile(t, loader, "\n\n  \n", 0o600)

	got, err := loader.Load()
	if err != nil {
		t.Fatalf("an empty token file was an error: %v", err)
	}
	if got.Value != "" {
		t.Errorf("value = %q, want empty", got.Value)
	}
}

// A token file other users can read is the one thing about the credential this
// package can actually see. Warned rather than refused -- see permissionWarning
// -- but it must be warned.
func TestAWorldReadableTokenFileIsWarnedAbout(t *testing.T) {
	loader, _ := loaderIn(t, nil)
	path := writeTokenFile(t, loader, secret, 0o644)

	got, err := loader.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Value != secret {
		t.Error("the token was not returned; this is a warning, not a refusal")
	}
	if !strings.Contains(got.Warning, path) || !strings.Contains(got.Warning, "chmod 600") {
		t.Errorf("warning = %q, want it to name the file and the fix", got.Warning)
	}
}

func TestAGroupReadableTokenFileIsWarnedAbout(t *testing.T) {
	loader, _ := loaderIn(t, nil)
	writeTokenFile(t, loader, secret, 0o640)

	got, err := loader.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got.Warning == "" {
		t.Error("a group-readable token file produced no warning")
	}
}

// Refused, because these produce an HTTP error from deep inside net/http that
// says nothing about where the value came from -- and because a file with two
// lines in it is a mistake worth naming.
func TestATokenThatCannotBeAHeaderIsRefused(t *testing.T) {
	tests := []struct {
		name  string
		value string
	}{
		{"two lines", "ak_one\nak_two"},
		{"an embedded space", "ak_one two"},
		{"a tab", "ak_one\tak_two"},
		{"a control byte", "ak_one\x01two"},
		{"a delete byte", "ak_one\x7ftwo"},
		{"far too long to be a token", strings.Repeat("a", maxLength+1)},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			loader, _ := loaderIn(t, map[string]string{EnvVar: tc.value})

			got, err := loader.Load()
			if err == nil {
				t.Fatalf("this was accepted as a token: %+v", got)
			}
			if got.Value != "" {
				t.Error("a refused token was still returned")
			}
			// The message has to be usable without being a leak.
			if !strings.Contains(err.Error(), EnvVar) {
				t.Errorf("err = %q, want it to name the source", err)
			}
		})
	}
}

// The bound is pinned to a LITERAL size, not to maxLength, because the point
// of the number is that it agrees with the API's own maxMachineTokenLength.
// A test written as maxLength+1 follows the constant wherever it goes and
// cannot notice the two drifting apart -- which is how a 300-byte value came
// to be accepted here, sent, refused by the API as implausibly long, and
// reported back to the user as a revoked or expired token.
func TestAValueTheAPIWouldRefuseIsRefusedLocallyInstead(t *testing.T) {
	loader, _ := loaderIn(t, map[string]string{EnvVar: "ak_" + strings.Repeat("a", 300)})

	_, err := loader.Load()
	if err == nil {
		t.Fatal("a 303-byte value was accepted; the API refuses anything over 256 and the user is told their token was revoked")
	}
	if !strings.Contains(err.Error(), "not a token") {
		t.Errorf("err = %q, want it to say this is not a token", err)
	}
}

// The central promise of this package. Nothing it produces -- value aside --
// may contain the credential, because all of it is printed to a terminal.
func TestNothingThisPackageProducesEverContainsTheToken(t *testing.T) {
	t.Run("the warning on a loose file", func(t *testing.T) {
		loader, _ := loaderIn(t, nil)
		writeTokenFile(t, loader, secret, 0o644)
		got, err := loader.Load()
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(got.Warning, secret) {
			t.Errorf("the warning contains the token: %q", got.Warning)
		}
		if strings.Contains(got.Source, secret) {
			t.Errorf("the source contains the token: %q", got.Source)
		}
	})

	t.Run("the warning on a shadowed file", func(t *testing.T) {
		loader, _ := loaderIn(t, map[string]string{EnvVar: secret})
		writeTokenFile(t, loader, secret, 0o600)
		got, err := loader.Load()
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(got.Warning, secret) {
			t.Errorf("the warning contains the token: %q", got.Warning)
		}
	})

	t.Run("the error on a malformed token", func(t *testing.T) {
		malformed := secret + " " + secret
		loader, _ := loaderIn(t, map[string]string{EnvVar: malformed})
		_, err := loader.Load()
		if err == nil {
			t.Fatal("a malformed token was accepted")
		}
		if strings.Contains(err.Error(), secret) {
			t.Errorf("the error contains the token: %q", err)
		}
	})

	t.Run("the error on an unreadable file", func(t *testing.T) {
		loader, _ := loaderIn(t, nil)
		path := writeTokenFile(t, loader, secret, 0o000)
		if os.Geteuid() == 0 {
			t.Skip("root reads a 0000 file regardless")
		}
		_, err := loader.Load()
		if err == nil {
			t.Fatal("an unreadable token file was not an error")
		}
		if !strings.Contains(err.Error(), path) {
			t.Errorf("err = %q, want it to name the file", err)
		}
		if strings.Contains(err.Error(), secret) {
			t.Errorf("the error contains the token: %q", err)
		}
	})
}

func TestThePathIsUnderTheConfigDirectory(t *testing.T) {
	loader, dir := loaderIn(t, nil)

	path, err := loader.Path()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(dir, dirName, fileName); path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
}

// Help text has to render on a machine with no config directory, or `--help`
// would fail for exactly the person who needs to read it.
func TestThePathHintSurvivesAPlatformWithNoConfigDirectory(t *testing.T) {
	loader := Loader{
		Getenv:    func(string) string { return "" },
		ConfigDir: func() (string, error) { return "", os.ErrNotExist },
	}

	if hint := loader.PathOrHint(); hint == "" || !strings.Contains(hint, fileName) {
		t.Errorf("hint = %q, want something naming the token file", hint)
	}
	// And Load must not fail either: the environment variable still works.
	if _, err := loader.Load(); err != nil {
		t.Errorf("Load failed with no config directory: %v", err)
	}
}
