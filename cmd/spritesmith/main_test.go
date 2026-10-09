package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/aviorstudio/spritesmith/internal/client"
	"github.com/aviorstudio/spritesmith/internal/token"
)

const testToken = "ak_ZmFrZS1zZWNyZXQtZm9yLXRlc3Rz"

func testLoader() token.Loader {
	return token.Loader{
		Getenv:    func(string) string { return "" },
		ConfigDir: func() (string, error) { return "/home/somebody/.config", nil },
	}
}

// A 401 with no token configured is the case the CLI exists to explain. "401
// Unauthorized" tells somebody nothing they can act on; this has to tell them
// where a token comes from and where to put it.
func TestTheHelpForAMissingTokenNamesTheFix(t *testing.T) {
	message := unauthorizedHelp(testLoader(), token.Token{})

	for _, want := range []string{
		token.EnvVar,
		"/home/somebody/.config/spritesmith/token",
		"Clerk",
		"chmod 600",
	} {
		if !strings.Contains(message, want) {
			t.Errorf("the message does not mention %q:\n%s", want, message)
		}
	}
}

// A 401 WITH a token is a different problem and needs a different answer:
// nothing is missing, the credential is dead. Naming the source is the whole
// value, because the token may be coming from somewhere the caller forgot.
func TestTheHelpForARejectedTokenNamesItsSourceAndNotItsValue(t *testing.T) {
	credential := token.Token{Value: testToken, Source: "$" + token.EnvVar}

	message := unauthorizedHelp(testLoader(), credential)

	if !strings.Contains(message, "$"+token.EnvVar) {
		t.Errorf("the message does not name where the token came from:\n%s", message)
	}
	if !strings.Contains(message, "revoked") {
		t.Errorf("the message does not say what is likely wrong:\n%s", message)
	}
	if strings.Contains(message, testToken) {
		t.Fatalf("the message contains the token:\n%s", message)
	}
}

// The token is a credential and this message is printed to a terminal, so the
// promise holds for a file source too -- where the source is a path that the
// value must not be confused with.
func TestNoUnauthorizedMessageEverContainsTheToken(t *testing.T) {
	for _, credential := range []token.Token{
		{},
		{Value: testToken, Source: "$" + token.EnvVar},
		{Value: testToken, Source: "/home/somebody/.config/spritesmith/token"},
	} {
		if message := unauthorizedHelp(testLoader(), credential); strings.Contains(message, testToken) {
			t.Errorf("the message contains the token:\n%s", message)
		}
	}
}

// --help is the only place anybody is told how to authenticate, which is half
// of what this issue is.
func TestHelpExplainsHowToGetAndSetAToken(t *testing.T) {
	var out strings.Builder
	printUsage(&out)
	help := out.String()

	for _, want := range []string{
		token.EnvVar,
		"Clerk API key",
		"spritesmith/token",
		// The local API needs none, or somebody will go hunting for a token
		// they do not need to run `make cli`.
		"local development API",
	} {
		if !strings.Contains(help, want) {
			t.Errorf("--help does not mention %q:\n%s", want, help)
		}
	}
}

// A recipe in the help is a command somebody pastes. `install` without -D does
// not create the parent directory, so the version without it fails on every
// machine that has never had a spritesmith config directory -- which is every
// machine reading this help for the first time.
func TestHelpUsesARecipeThatWorksOnAFreshMachine(t *testing.T) {
	var out strings.Builder
	printUsage(&out)
	help := out.String()

	if !strings.Contains(help, "install -D -m 600") {
		t.Errorf("--help suggests an install that does not create the directory:\n%s", help)
	}
}

// The output-format toggles are gone, not merely undocumented. A flag that
// still parses is a flag somebody's script still passes, and `--sheet` in
// particular would go on parsing forever while quietly meaning nothing.
func TestTheOutputFormatFlagsNoLongerExist(t *testing.T) {
	for _, gone := range []string{"-sheet", "-debug"} {
		t.Run(gone, func(t *testing.T) {
			fs, _ := newCommandFlags("image")
			fs.SetOutput(io.Discard)
			if err := fs.Parse([]string{gone, "source.png"}); err == nil {
				t.Errorf("%s still parses, so it is still part of the interface", gone)
			}
		})
	}
}

// --out names the PNG now, not a directory to go looking in. The default has
// to be a file for that to be true of the path somebody gets with no flags at
// all -- which is the path almost everybody gets.
func TestOutDefaultsToASinglePNGFileInTheCurrentDirectory(t *testing.T) {
	fs, flags := newCommandFlags("image")
	if err := fs.Parse(nil); err != nil {
		t.Fatal(err)
	}

	got := outputPath(flags)
	if filepath.Ext(got) != ".png" {
		t.Errorf("the default output %q is not a .png file", got)
	}
	if dir := filepath.Dir(got); dir != "." {
		t.Errorf("the default output %q is under %q; it should land in the current directory, not a directory spritesmith invents", got, dir)
	}
}

// The seam between a flag and the request built from it. Nothing else crosses
// it: the flags are tested here, the writing is tested in internal/client, and
// dropping OutputPath from either struct literal used to leave both halves
// green while --out silently did nothing -- the CLI would still print a path
// and still write a PNG, just never the one that was asked for.
func TestTheFlagsReachTheRequestForBothCommands(t *testing.T) {
	t.Run("image", func(t *testing.T) {
		apiBase, req, ok := parseImage([]string{"--api", "https://api.example.com", "--out", "build/sprite.png", "source.png"})
		if !ok {
			t.Fatal("parsing a valid image command failed")
		}
		if apiBase != "https://api.example.com" {
			t.Errorf("apiBase = %q, want the --api that was given", apiBase)
		}
		if req.Mode != client.ModeImage {
			t.Errorf("Mode = %q, want %q", req.Mode, client.ModeImage)
		}
		if req.SourcePath != "source.png" {
			t.Errorf("SourcePath = %q, want the positional argument", req.SourcePath)
		}
		if req.OutputPath != "build/sprite.png" {
			t.Errorf("OutputPath = %q, want the --out that was given; --out is being ignored", req.OutputPath)
		}
	})

	t.Run("prompt", func(t *testing.T) {
		apiBase, req, ok := parsePrompt([]string{"--api", "https://api.example.com", "--out", "build/sprite.png", "a", "duck"})
		if !ok {
			t.Fatal("parsing a valid prompt command failed")
		}
		if apiBase != "https://api.example.com" {
			t.Errorf("apiBase = %q, want the --api that was given", apiBase)
		}
		if req.Mode != client.ModePrompt {
			t.Errorf("Mode = %q, want %q", req.Mode, client.ModePrompt)
		}
		if req.Prompt != "a duck" {
			t.Errorf("Prompt = %q, want the joined positional arguments", req.Prompt)
		}
		if req.OutputPath != "build/sprite.png" {
			t.Errorf("OutputPath = %q, want the --out that was given; --out is being ignored", req.OutputPath)
		}
	})
}

// With no --out the request carries the default, which is what makes the
// no-flags run land somewhere sensible rather than nowhere.
func TestWithNoOutFlagTheRequestCarriesTheDefaultFile(t *testing.T) {
	_, req, ok := parsePrompt([]string{"a duck"})
	if !ok {
		t.Fatal("parsing a valid prompt command failed")
	}
	if req.OutputPath != client.DefaultOutputPath {
		t.Errorf("OutputPath = %q, want %q", req.OutputPath, client.DefaultOutputPath)
	}
}

// Both commands still reject the argument shapes they cannot act on, and do it
// without building a request.
func TestACommandWithTheWrongArgumentsIsRejected(t *testing.T) {
	if _, _, ok := parseImage(nil); ok {
		t.Error("image with no file was accepted")
	}
	if _, _, ok := parseImage([]string{"one.png", "two.png"}); ok {
		t.Error("image with two files was accepted")
	}
	for _, args := range [][]string{nil, {"--out", "x.png"}, {"--out=x.png"}, {"   "}} {
		message := captureStderr(t, func() {
			if _, _, ok := parsePrompt(args); ok {
				t.Errorf("prompt arguments %q were accepted", args)
			}
		})
		if !strings.Contains(message, "usage: spritesmith prompt [flags] PROMPT") {
			t.Errorf("prompt arguments %q did not print usage", args)
		}
	}
}

func TestAFlagAfterThePromptIsRejectedBeforeARequest(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	t.Cleanup(server.Close)

	for name, args := range map[string][]string{
		"ordinary":             {"--api", server.URL, "a duck", "--out", "x.png"},
		"consumed -- as value": {"--api", server.URL, "--out", "--", "duck", "--out", "x.png"},
	} {
		t.Run(name, func(t *testing.T) {
			var exitCode int
			message := captureStderr(t, func() { exitCode = runPrompt(args) })
			if exitCode != 2 || requests.Load() != 0 {
				t.Errorf("exit code = %d, requests = %d; want 2, 0", exitCode, requests.Load())
			}
			for _, want := range []string{`misplaced flag "--out"`, `spritesmith prompt --out x.png "a duck"`} {
				if !strings.Contains(message, want) {
					t.Errorf("error does not contain %q:\n%s", want, message)
				}
			}
		})
	}
}

func TestAFlagBeforeThePromptStillBuildsTheRequestedOutput(t *testing.T) {
	_, req, ok := parsePrompt([]string{"--out", "x.png", "a duck"})
	if !ok {
		t.Fatal("flag-first prompt was rejected")
	}
	if req.Prompt != "a duck" || req.OutputPath != "x.png" {
		t.Errorf("request = %#v, want prompt %q and output %q", req, "a duck", "x.png")
	}
}

func TestTheFlagTerminatorStillAllowsAPromptBeginningWithADash(t *testing.T) {
	_, req, ok := parsePrompt([]string{"--", "-a duck"})
	if !ok {
		t.Fatal("prompt escaped with -- was rejected")
	}
	if req.Prompt != "-a duck" {
		t.Errorf("Prompt = %q, want %q", req.Prompt, "-a duck")
	}
}

// image is the control: unlike prompt it accepts exactly one positional
// argument, so the same misplaced flag shape was already rejected.
func TestImageStillRejectsAFlagAfterItsArgument(t *testing.T) {
	if _, _, ok := parseImage([]string{"source.png", "--out", "x.png"}); ok {
		t.Error("image accepted a flag after its positional argument")
	}
}

func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	stderr, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stderr
	os.Stderr = stderr
	defer func() { os.Stderr = original }()

	fn()
	if err := stderr.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := os.ReadFile(stderr.Name())
	if err != nil {
		t.Fatal(err)
	}
	return string(output)
}

// The help describes one output. Every word below names a shape spritesmith does
// not produce, and leaving one in the help is how somebody goes looking for a
// flag or a directory that is not there.
//
// The archive and index formats are absent from this list on purpose, not by
// oversight: #56 asks that `grep -ri` for their names over cli/ come back
// empty, and a list here that spelled them would be the one hit. That grep is
// the stronger guard anyway -- it covers the whole tree, help text included,
// rather than this one string.
func TestHelpDescribesOneOutputAndNothingThatNoLongerExists(t *testing.T) {
	var out strings.Builder
	printUsage(&out)
	// The help embeds the reader's own config directory, which this test does
	// not control and which could contain any of the words below. Scanning it
	// would make the test fail on somebody's laptop for a reason that has
	// nothing to do with spritesmith.
	help := strings.ToLower(out.String())
	help = strings.ReplaceAll(help, strings.ToLower(token.New().PathOrHint()), "<token-path>")

	for _, gone := range []string{"sheet", "slice", "bundle", "spritesmith-out", "individual", "assets", "directory"} {
		if strings.Contains(help, gone) {
			t.Errorf("--help still mentions %q:\n%s", gone, out.String())
		}
	}
	for _, want := range []string{
		"one png or svg",
		// --out takes a file, and saying DIR would send somebody looking
		// inside it for the result.
		"--out file",
	} {
		if !strings.Contains(help, want) {
			t.Errorf("--help does not say %q:\n%s", want, out.String())
		}
	}
}

// The refusal to overwrite is a decision somebody has to be able to find
// before they hit it, and it lives in exactly one place.
func TestHelpSaysAnExistingFileIsNotOverwritten(t *testing.T) {
	var out strings.Builder
	printUsage(&out)

	if !strings.Contains(strings.ToLower(out.String()), "never overwritten") {
		t.Errorf("--help does not say what happens when the output path is taken:\n%s", out.String())
	}
}
