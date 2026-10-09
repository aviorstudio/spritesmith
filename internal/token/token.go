// Package token finds the credential the CLI presents to the spritesmith API.
//
// The credential is a Clerk API key: an opaque `ak_...` secret created against
// one spritesmith account, which the API verifies with Clerk on every request. It
// is long-lived and it is not scoped down -- whoever holds it is that person as
// far as the API is concerned -- so everything here is arranged around not
// leaking it:
//
//   - it is never returned in an error, a warning, or anything else this
//     package produces. Messages name the SOURCE and never the value.
//   - it is never accepted on the command line, which is why there is no
//     --token flag. An argument is in the shell history of everyone who typed
//     it and in the process table of everyone on the machine.
//   - the file it can live in is checked for being readable by other users,
//     which is the one thing about it this package can see.
//
// Two sources, in order: the SPRITESMITH_API_TOKEN environment variable, then a
// file under the user's config directory. Environment first because that is
// what a shell, a CI job, or a `direnv` sets, and it must be able to override
// whatever is on disk for one invocation.
package token

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// EnvVar is the environment variable holding the token. Named here so the help
// text, the error messages and the loader cannot disagree about it.
const EnvVar = "SPRITESMITH_API_TOKEN"

// dirName and fileName locate the token file: $XDG_CONFIG_HOME/spritesmith/token on
// Linux, ~/Library/Application Support/spritesmith/token on macOS.
const (
	dirName  = "spritesmith"
	fileName = "token"
)

// maxLength bounds what will be accepted as a token. Clerk's API key secrets
// are a few dozen characters. A file that is much larger is a file that is not
// a token -- a stray log, a whole config, a private key pasted by mistake --
// and sending it as a header is neither going to work nor worth doing.
//
// Deliberately the same number as the API's own maxMachineTokenLength
// (api/internal/clerkauth/machine.go), which cannot be referenced from here --
// separate modules -- so both are pinned by a literal in their own tests.
// When the CLI's bound was the looser of the two, a value between them was
// accepted here, sent, refused there, and reported back as "the API rejected
// your token, it may have been revoked" -- a confident wrong diagnosis for a
// value that was never a token. Refusing it locally says what is wrong.
//
// The two bounds are the same NUMBER and not the same SET, which is worth
// knowing before changing either. The API's applies only to `ak_` machine
// tokens; this one applies to whatever is in SPRITESMITH_API_TOKEN or the token
// file. A Clerk SESSION token pasted there -- 600-900 bytes of JWT -- is
// therefore refused here rather than being forwarded. That is intended: a
// session token lives about a minute and is useless to a CLI, so failing at
// once with "that is not a token" beats a 401 sixty seconds later.
const maxLength = 256

// Token is a credential and where it was found.
type Token struct {
	// Value is the token itself, or "" when there is none. An absent token is
	// not an error: a local API with no Clerk configuration serves without one,
	// which is what makes `make cli` work on a laptop with nothing set up.
	Value string
	// Source describes where Value came from, for messages. Never the value.
	Source string
	// Warning is a problem worth saying out loud that is not worth refusing
	// over. Empty when there is none, and never contains the value.
	Warning string
}

// Loader reads the token. The two dependencies are fields rather than direct
// calls so the tests can exercise every branch without depending on the host's
// idea of a config directory, which differs per platform.
type Loader struct {
	Getenv    func(string) string
	ConfigDir func() (string, error)
}

// New returns a Loader reading the real environment and config directory.
func New() Loader {
	return Loader{Getenv: os.Getenv, ConfigDir: os.UserConfigDir}
}

// Path is where the token file lives. Reported in help and in the error a
// missing token produces, so somebody is told exactly where to put one.
func (l Loader) Path() (string, error) {
	dir, err := l.ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, dirName, fileName), nil
}

// PathOrHint is Path, degraded to a description when the platform will not say
// where its config directory is. Used in help text, which must always render.
func (l Loader) PathOrHint() string {
	path, err := l.Path()
	if err != nil {
		return filepath.Join("<config dir>", dirName, fileName)
	}
	return path
}

// Load returns the token, or an empty one when there is none.
//
// An error means a token was found and is not usable -- not that none was
// found. The difference matters: a missing token is normal against a dev API,
// while a malformed one is a mistake that would otherwise surface as an
// unexplained 401 much later.
func (l Loader) Load() (Token, error) {
	if value := strings.TrimSpace(l.Getenv(EnvVar)); value != "" {
		source := "$" + EnvVar
		if err := validate(value, source); err != nil {
			return Token{}, err
		}
		return Token{Value: value, Source: source, Warning: l.shadowedFileWarning()}, nil
	}

	path, err := l.Path()
	if err != nil {
		// No config directory on this platform or no HOME. Not fatal: the
		// environment variable is still a way in, and no token at all is a
		// legitimate state.
		return Token{}, nil
	}

	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return Token{}, nil
	}
	if err != nil {
		return Token{}, fmt.Errorf("read the spritesmith API token from %s: %w", path, err)
	}

	value := strings.TrimSpace(string(data))
	if value == "" {
		return Token{}, nil
	}
	source := path
	if err := validate(value, source); err != nil {
		return Token{}, err
	}
	return Token{Value: value, Source: source, Warning: permissionWarning(path)}, nil
}

// shadowedFileWarning says that a token file exists and is being ignored.
//
// Without it, somebody who rotates the file and keeps failing has no way to
// see that a variable set in a shell profile months ago is what the API is
// actually rejecting.
func (l Loader) shadowedFileWarning() string {
	path, err := l.Path()
	if err != nil {
		return ""
	}
	if _, err := os.Stat(path); err != nil {
		return ""
	}
	return fmt.Sprintf("%s is being ignored because %s is set", path, EnvVar)
}

// permissionWarning reports a token file other users on the machine can read.
//
// A warning rather than a refusal. On a single-user laptop a 644 token file is
// a bad habit and not a breach, and a CLI that stops working because of the
// umask that wrote the file -- with no way to override -- would be worse than
// the problem. The message names the fix so it is one command to clear.
func permissionWarning(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return ""
	}
	if info.Mode().Perm()&0o077 == 0 {
		return ""
	}
	return fmt.Sprintf("%s is readable by other users on this machine; run: chmod 600 %s", path, path)
}

// validate refuses a value that cannot be a working token, naming the source
// and never the value.
func validate(value, source string) error {
	if len(value) > maxLength {
		return fmt.Errorf("the spritesmith API token in %s is %d bytes; that is not a token", source, len(value))
	}
	// A header value cannot contain spaces, tabs, newlines or control bytes,
	// and Go's HTTP client refuses one that does with an error that says
	// nothing about where the value came from. Caught here instead: this is
	// what a file with two lines in it, or a copy-paste that took the
	// surrounding quotes and a newline, actually looks like.
	for _, r := range value {
		if r <= ' ' || r == 0x7f {
			return fmt.Errorf("the spritesmith API token in %s contains whitespace or control characters; it should be a single line", source)
		}
	}
	return nil
}
