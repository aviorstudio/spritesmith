package client

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// What the CLI puts on the wire, and what it refuses to.

const testToken = "ak_ZmFrZS1zZWNyZXQtZm9yLXRlc3Rz"

// apiResponseBody is what the stand-in API serves. Distinctive on purpose, so
// a test can tell the API's answer apart from anything else that might end up
// on disk under the same name.
const apiResponseBody = "not really a png, but it is what the API sent"

// recordingAPI is a stand-in for the spritesmith API that keeps what the CLI put on
// the wire: the one header the auth work is about, and the form fields that
// decide what shape of output comes back.
type recordingAPI struct {
	authorization string
	form          url.Values
	seen          bool
	status        int
}

func (r *recordingAPI) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	r.seen = true
	r.authorization = req.Header.Get("Authorization")
	// Parsed by the same multipart reader the real API uses, rather than
	// scanned for as a substring of the raw body -- a body-level check would be
	// satisfied by a field name appearing anywhere, including inside a prompt.
	r.form = url.Values{}
	if err := req.ParseMultipartForm(1 << 20); err == nil && req.MultipartForm != nil {
		for name, values := range req.MultipartForm.Value {
			r.form[name] = values
		}
	}
	if r.status != 0 {
		w.WriteHeader(r.status)
		_, _ = w.Write([]byte(`{"error":"missing or invalid session token"}`))
		return
	}
	w.Header().Set("Content-Type", "image/png")
	_, _ = w.Write([]byte(apiResponseBody))
}

func promptRequest(t *testing.T) Request {
	t.Helper()
	return Request{
		Mode:       ModePrompt,
		Prompt:     "a duck",
		OutputPath: filepath.Join(t.TempDir(), "out.png"),
	}
}

func TestTheTokenIsSentAsABearerCredential(t *testing.T) {
	api := &recordingAPI{}
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)

	if _, err := (Client{BaseURL: srv.URL, Token: testToken}).Process(promptRequest(t)); err != nil {
		t.Fatal(err)
	}
	if want := "Bearer " + testToken; api.authorization != want {
		t.Errorf("Authorization = %q, want %q", api.authorization, want)
	}
}

// No token must mean no header at all, not an empty one. A local API with no
// Clerk configuration serves unauthenticated, and `Bearer ` with nothing after
// it would be a token it has to reject.
func TestWithNoTokenNoAuthorizationHeaderIsSent(t *testing.T) {
	api := &recordingAPI{}
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)

	if _, err := (Client{BaseURL: srv.URL}).Process(promptRequest(t)); err != nil {
		t.Fatal(err)
	}
	if api.authorization != "" {
		t.Errorf("Authorization = %q, want no header", api.authorization)
	}
}

// A 401 comes back as the sentinel, so the command can explain it. Reporting
// the API's own message instead would be a bare 401 dump: it says "missing or
// invalid session token" on purpose, because telling bad tokens apart out loud
// is free reconnaissance.
func TestAnUnauthorizedResponseIsTheSentinel(t *testing.T) {
	srv := httptest.NewServer(&recordingAPI{status: http.StatusUnauthorized})
	t.Cleanup(srv.Close)

	_, err := (Client{BaseURL: srv.URL, Token: testToken}).Process(promptRequest(t))
	if !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("err = %v, want ErrUnauthorized", err)
	}
}

// Every other failure keeps reporting what the API said.
func TestOtherFailuresStillReportTheAPIsMessage(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":"missing required prompt"}`))
	}))
	t.Cleanup(srv.Close)

	_, err := (Client{BaseURL: srv.URL}).Process(promptRequest(t))
	if err == nil || !strings.Contains(err.Error(), "missing required prompt") {
		t.Fatalf("err = %v, want the API's own message", err)
	}
	if errors.Is(err, ErrUnauthorized) {
		t.Error("a 400 was reported as unauthorized")
	}
}

// refusingTransport fails the test if it is ever asked to send anything.
type refusingTransport struct{ t *testing.T }

func (r refusingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	r.t.Error("a request was sent even though the transport had been refused")
	return nil, errors.New("this transport never sends")
}

// The guard that matters most here. http:// is this CLI's default and --api is
// an ordinary string, so pointing a long-lived bearer credential at a
// plaintext host is one stale shell export away.
func TestTheTokenIsNotSentOverPlaintextToARemoteHost(t *testing.T) {
	bases := []string{
		"http://api.spritesmith.ai",
		"http://192.0.2.10:8080",
		"http://example.com/v1",
		// Not a scheme this CLI can use, and certainly not one to leak into.
		"ftp://example.com",
	}

	for _, base := range bases {
		t.Run(base, func(t *testing.T) {
			c := Client{
				BaseURL:    base,
				Token:      testToken,
				HTTPClient: &http.Client{Transport: refusingTransport{t}},
			}

			_, err := c.Process(promptRequest(t))
			if err == nil {
				t.Fatal("the token was sent in the clear")
			}
			if strings.Contains(err.Error(), testToken) {
				t.Errorf("the refusal message contains the token: %q", err)
			}
			// The message has to name the fix, not just the problem.
			if !strings.Contains(err.Error(), "SPRITESMITH_API_TOKEN") || !strings.Contains(err.Error(), "https") {
				t.Errorf("err = %q, want it to name the fix", err)
			}
		})
	}
}

// And the same URLs are perfectly fine with no token, because then there is
// nothing to leak. Refusing them outright would break plaintext local setups
// that never had a credential.
func TestAPlaintextRemoteHostIsAllowedWithNoToken(t *testing.T) {
	srv := httptest.NewServer(&recordingAPI{})
	t.Cleanup(srv.Close)

	// httptest is loopback, so exercise the check directly for the remote case.
	if err := checkTokenTransport("http://api.spritesmith.ai"); err == nil {
		t.Fatal("checkTokenTransport accepted a plaintext remote host")
	}
	if _, err := (Client{BaseURL: srv.URL}).Process(promptRequest(t)); err != nil {
		t.Fatalf("a tokenless request over plaintext was refused: %v", err)
	}
}

// Loopback over plaintext is the local API, which is the workflow this CLI is
// used for daily. The traffic never leaves the machine.
func TestTheTokenIsSentToALoopbackAPIOverPlaintext(t *testing.T) {
	for _, base := range []string{
		"http://127.0.0.1:8080",
		"http://localhost:8080",
		"http://[::1]:8080",
		"http://LOCALHOST:8080",
	} {
		t.Run(base, func(t *testing.T) {
			if err := checkTokenTransport(base); err != nil {
				t.Errorf("checkTokenTransport(%q) = %v, want nil", base, err)
			}
		})
	}

	// End to end against the real local server httptest gives us.
	api := &recordingAPI{}
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	if _, err := (Client{BaseURL: srv.URL, Token: testToken}).Process(promptRequest(t)); err != nil {
		t.Fatal(err)
	}
	if api.authorization == "" {
		t.Error("no token reached the local API")
	}
}

// Checking only the base URL does not hold the token safe. Go copies the
// Authorization header across a redirect to the same host or a subdomain of
// it, comparing hostnames and ignoring the scheme entirely -- so an https base
// answering `302 http://same-host/...` is a cleartext credential that passed
// every check. The guard has to run on each hop.
func TestAnHTTPSToHTTPRedirectOnTheSameHostIsRefused(t *testing.T) {
	var hops atomic.Int64
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hops.Add(1)
		if r.Header.Get("Authorization") != "" && r.TLS == nil {
			t.Error("the token arrived over a plaintext connection")
		}
		http.Redirect(w, r, "http://example.com/v1/prompt", http.StatusFound)
	}))
	t.Cleanup(srv.Close)

	// A transport that dials the test server whatever hostname is asked for,
	// so both legs are genuinely the same host and Go's own rule to copy the
	// header applies. Without this the redirect would cross hosts and net/http
	// would drop the header for its own reasons rather than ours. The hostname
	// is example.com because that is what httptest's certificate is issued for.
	transport := srv.Client().Transport.(*http.Transport).Clone()
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, strings.TrimPrefix(srv.URL, "https://"))
	}

	c := Client{
		BaseURL:    "https://example.com",
		Token:      testToken,
		HTTPClient: &http.Client{Transport: transport},
	}

	_, err := c.Process(promptRequest(t))
	if err == nil {
		t.Fatal("a downgrade redirect was followed with the token attached")
	}
	if !strings.Contains(err.Error(), "refusing to send the spritesmith API token") {
		t.Fatalf("err = %v, want the transport refusal", err)
	}
	if strings.Contains(err.Error(), testToken) {
		t.Errorf("the refusal message contains the token: %q", err)
	}
	if got := hops.Load(); got != 1 {
		t.Errorf("%d requests reached the server, want 1 (the https leg only)", got)
	}
}

// The guard refuses a downgrade, not redirects. Blanket-refusing them would
// be the lazy way to close the hole above and would break any deployment that
// redirects at all -- and the 307 replay is worth pinning too, since the body
// is a bytes.Buffer and only replays because GetBody is set.
func TestARedirectToAnAllowedTargetIsStillFollowed(t *testing.T) {
	final := &recordingAPI{}
	target := httptest.NewServer(final)
	t.Cleanup(target.Close)

	var hops atomic.Int64
	front := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hops.Add(1)
		http.Redirect(w, r, target.URL+"/v1/prompt", http.StatusTemporaryRedirect)
	}))
	t.Cleanup(front.Close)

	if _, err := (Client{BaseURL: front.URL, Token: testToken}).Process(promptRequest(t)); err != nil {
		t.Fatalf("a redirect between two allowed addresses was refused: %v", err)
	}
	if got := hops.Load(); got != 1 {
		t.Errorf("%d requests to the redirector, want 1", got)
	}
	if want := "Bearer " + testToken; final.authorization != want {
		t.Errorf("the redirect target got Authorization = %q, want %q", final.authorization, want)
	}
}

// Guarding redirects must not change redirect behaviour for anybody else in
// the process, which mutating http.DefaultClient in place would.
//
// Asserted against guardRedirects DIRECTLY rather than through a request, and
// with no skip. The first version of this test read http.DefaultClient and
// skipped when its CheckRedirect was already set -- which, under the very
// mutation it existed to catch, the buggy code set itself, via an earlier test
// in the same package that uses the default client. So it skipped, the package
// stayed green, and it only failed when run alone. Same shape as the
// millisecond fixture from Review 1: an assertion gated on a precondition the
// bug controls proves nothing, and CI runs packages, not single tests.
func TestGuardingRedirectsDoesNotMutateTheClientItIsGiven(t *testing.T) {
	original := &http.Client{Timeout: 42 * time.Second}

	guarded := guardRedirects(original)

	if original.CheckRedirect != nil {
		t.Error("the caller's own client was mutated; every other user of it now redirects differently")
	}
	if guarded == original {
		t.Fatal("the same client was returned, so there was nothing to mutate but the caller's")
	}
	if guarded.CheckRedirect == nil {
		t.Error("the copy carries no redirect guard, so the token is unprotected after the first hop")
	}
	// A copy, not a fresh client: everything else about it has to survive.
	if guarded.Timeout != original.Timeout {
		t.Errorf("Timeout = %v, want the caller's %v", guarded.Timeout, original.Timeout)
	}
}

// And the same fact end to end, through a real request that takes
// http.DefaultClient because no HTTPClient was supplied. That global is the
// one whose mutation would be worst, so it is worth exercising for real rather
// than only through guardRedirects.
//
// The field is reset and restored around the request. Two earlier versions of
// this test failed to catch its own mutation for want of that: the first
// SKIPPED when CheckRedirect was already set, and the second compared against
// a `before` value that the bug -- via an earlier test in this package using
// the same global -- had already set. Both times the assertion depended on
// state the bug controlled. Owning the field outright is what makes it
// order-independent.
func TestARequestWithATokenDoesNotMutateHTTPDefaultClient(t *testing.T) {
	before := http.DefaultClient.CheckRedirect
	http.DefaultClient.CheckRedirect = nil
	t.Cleanup(func() { http.DefaultClient.CheckRedirect = before })

	srv := httptest.NewServer(&recordingAPI{})
	t.Cleanup(srv.Close)
	if _, err := (Client{BaseURL: srv.URL, Token: testToken}).Process(promptRequest(t)); err != nil {
		t.Fatal(err)
	}

	if http.DefaultClient.CheckRedirect != nil {
		t.Error("http.DefaultClient was mutated; every other caller in the process now redirects differently")
	}
}

// Replacing CheckRedirect switches off net/http's own hop limit, so the limit
// has to be put back. Without it the CLI does not fail against a server that
// redirects in a loop -- it hangs, on an authenticated request, forever.
func TestARedirectLoopStopsRatherThanHanging(t *testing.T) {
	var hops atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hops.Add(1)
		http.Redirect(w, r, r.URL.Path, http.StatusFound)
	}))
	t.Cleanup(srv.Close)

	done := make(chan error, 1)
	go func() {
		_, err := (Client{BaseURL: srv.URL, Token: testToken}).Process(promptRequest(t))
		done <- err
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a redirect loop was followed to completion, which cannot happen")
		}
	case <-time.After(20 * time.Second):
		t.Fatal("a redirect loop hung instead of stopping; the hop limit is gone")
	}
	if got := hops.Load(); got > 11 {
		t.Errorf("%d hops before stopping, want at most 11", got)
	}
}

// Go attaches the token to a SUBDOMAIN of the original host too, over TLS, to
// a host that never had it. The spritesmith API has no reason to redirect across
// hosts, so it is refused.
func TestARedirectToAnotherHostIsRefusedWhileCarryingTheToken(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://evil.example.com/v1/prompt", http.StatusFound)
	}))
	t.Cleanup(srv.Close)

	transport := srv.Client().Transport.(*http.Transport).Clone()
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, strings.TrimPrefix(srv.URL, "https://"))
	}

	c := Client{
		BaseURL:    "https://example.com",
		Token:      testToken,
		HTTPClient: &http.Client{Transport: transport},
	}

	_, err := c.Process(promptRequest(t))
	if err == nil {
		t.Fatal("the token was handed to another host by a redirect")
	}
	if !strings.Contains(err.Error(), "refusing to follow a redirect") {
		t.Fatalf("err = %v, want the cross-host refusal", err)
	}
	if strings.Contains(err.Error(), testToken) {
		t.Errorf("the refusal message contains the token: %q", err)
	}
	// The same standard the plaintext refusal is held to: name the fix, not
	// just the problem. This is the refusal somebody is most likely to hit on a
	// real deployment -- an apex host redirecting to the API host.
	if !strings.Contains(err.Error(), "--api") || !strings.Contains(err.Error(), "SPRITESMITH_API_TOKEN") {
		t.Errorf("err = %q, want it to name the fix", err)
	}
	// And it must not assert a mechanism that is not true here: evil.example.com
	// is not a subdomain of example.com, so Go would have stripped the header
	// anyway. Refusing is still right; claiming the wrong reason is not.
	if strings.Contains(err.Error(), "subdomain") {
		t.Errorf("the message explains this refusal with a mechanism that does not apply: %q", err)
	}
}

// A caller's own redirect policy runs first, so a policy that stops the
// redirect is honoured rather than overridden by a refusal about a hop that
// was never going to be followed -- and the token never moves either way.
func TestACallersOwnRedirectPolicyIsHonouredFirst(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://example.com/v1/prompt", http.StatusFound)
	}))
	t.Cleanup(srv.Close)

	c := Client{
		BaseURL: srv.URL,
		Token:   testToken,
		HTTPClient: &http.Client{
			CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		},
	}

	// The caller's policy stops the redirect, so the 302 comes back as the
	// response. It is not a 2xx, so Process reports it -- what matters is that
	// the failure is the API's answer and not our refusal about a hop nobody
	// was going to take.
	_, err := c.Process(promptRequest(t))
	if err != nil && strings.Contains(err.Error(), "refusing to send the spritesmith API token") {
		t.Fatalf("the guard overrode the caller's own redirect policy: %v", err)
	}
}

// And https anywhere, which is what production is.
func TestTheTokenIsSentOverHTTPS(t *testing.T) {
	api := &recordingAPI{}
	srv := httptest.NewTLSServer(api)
	t.Cleanup(srv.Close)

	c := Client{BaseURL: srv.URL, Token: testToken, HTTPClient: srv.Client()}
	if _, err := c.Process(promptRequest(t)); err != nil {
		t.Fatal(err)
	}
	if want := "Bearer " + testToken; api.authorization != want {
		t.Errorf("Authorization = %q, want %q", api.authorization, want)
	}
}

// Every request keeps the legacy field for compatibility with older API
// deployments. Current APIs ignore it and always return one transparent PNG.
func TestEveryRequestAsksForThePackedSheet(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source.png")
	if err := os.WriteFile(source, []byte("this is the source image, not the result"), 0o600); err != nil {
		t.Fatal(err)
	}

	requests := []Request{
		// The prompt says "sheet" so that a check which merely found the word
		// somewhere in the request would pass here and prove nothing. The
		// assertion below reads a parsed form field instead.
		{Mode: ModePrompt, Prompt: "a duck standing on a bedsheet"},
		{Mode: ModeImage, SourcePath: source},
	}

	for _, req := range requests {
		t.Run(string(req.Mode), func(t *testing.T) {
			api := &recordingAPI{}
			srv := httptest.NewServer(api)
			t.Cleanup(srv.Close)

			req.OutputPath = filepath.Join(t.TempDir(), "out.png")
			if _, err := (Client{BaseURL: srv.URL}).Process(req); err != nil {
				t.Fatal(err)
			}

			if got := api.form.Get("sheet"); got != "true" {
				t.Errorf(`sheet = %q, want "true" for compatibility with older APIs`, got)
			}
			// The removed user-facing debug option must not return by accident.
			if _, present := api.form["debug"]; present {
				t.Errorf("the request still sends debug=%q", api.form.Get("debug"))
			}
		})
	}
}

// One PNG, where it was asked for, and nothing beside it.
func TestASuccessfulRunWritesOneFileAtTheNamedPath(t *testing.T) {
	srv := httptest.NewServer(&recordingAPI{})
	t.Cleanup(srv.Close)

	dir := t.TempDir()
	// Deliberately not DefaultOutputPath. Code that ignored OutputPath and fell
	// back to the default would still produce exactly one PNG somewhere, and a
	// test that accepted "one PNG called spritesmith-alpha.png" would pass for it.
	want := filepath.Join(dir, "sprite.png")

	resp, err := (Client{BaseURL: srv.URL}).Process(Request{
		Mode:       ModePrompt,
		Prompt:     "a duck",
		OutputPath: want,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.OutputPath != want {
		t.Errorf("OutputPath = %q, want %q", resp.OutputPath, want)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		names := make([]string, 0, len(entries))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("the run left %d entries %v, want exactly one PNG", len(entries), names)
	}
	if entries[0].Name() != "sprite.png" || entries[0].IsDir() {
		t.Errorf("wrote %q (dir=%v), want the file sprite.png", entries[0].Name(), entries[0].IsDir())
	}
	// The bytes have to be the API's answer. Creating a file of the right name
	// is not the same as saving the result into it.
	data, err := os.ReadFile(want)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != apiResponseBody {
		t.Errorf("the file holds %q, want the API's response body", string(data))
	}
}

// A result costs an OpenAI call and cannot be reproduced -- the same prompt
// does not generate the same image twice -- so an existing file is refused
// rather than overwritten. Refused BEFORE the request, too: finding out
// afterwards means paying for a generation that is then discarded.
func TestAnExistingOutputFileIsRefusedBeforeAnythingIsSent(t *testing.T) {
	existing := filepath.Join(t.TempDir(), "out.png")
	const precious = "yesterday's generation, which nobody can make again"
	if err := os.WriteFile(existing, []byte(precious), 0o600); err != nil {
		t.Fatal(err)
	}

	c := Client{
		BaseURL: "http://127.0.0.1:8080",
		// Fails the test if a request is sent at all.
		HTTPClient: &http.Client{Transport: refusingTransport{t}},
	}

	_, err := c.Process(Request{Mode: ModePrompt, Prompt: "a duck", OutputPath: existing})
	if err == nil {
		t.Fatal("an existing file was accepted as an output path")
	}
	if !strings.Contains(err.Error(), existing) {
		t.Errorf("err = %q, want it to name the path in the way", err)
	}
	// Name the fix, the standard every other refusal in this package is held to.
	if !strings.Contains(err.Error(), "--out") {
		t.Errorf("err = %q, want it to name the fix", err)
	}

	data, err := os.ReadFile(existing)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != precious {
		t.Fatalf("the existing file was modified: %q", string(data))
	}
}

// The pre-flight check saves a wasted generation; it is not the guarantee. A
// whole network round trip separates it from the write, so the write refuses
// on its own -- through the kernel, which cannot lose that race.
func TestTheWriteRefusesAFileThatAppearedAfterTheCheck(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.png")
	const precious = "landed here while the request was in flight"
	if err := os.WriteFile(path, []byte(precious), 0o600); err != nil {
		t.Fatal(err)
	}

	err := writeOutput(path, strings.NewReader(apiResponseBody))
	if err == nil {
		t.Fatal("the write truncated a file that appeared after the check")
	}
	if !strings.Contains(err.Error(), path) {
		t.Errorf("err = %q, want it to name the path", err)
	}
	// The raw *fs.PathError from OpenFile already reads "... file exists" and
	// already contains the path, so naming the path proves nothing about the
	// mapping to errOutputExists. This does: only the mapped message names the
	// fix, and without it the write-time refusal degrades to a bare errno while
	// the pre-flight one stays helpful.
	if !strings.Contains(err.Error(), "--out") {
		t.Errorf("err = %q, want the same message the pre-flight refusal gives, naming the fix", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != precious {
		t.Fatalf("the file was overwritten: %q", string(data))
	}
}

// A symlink at the output path is refused, which is what keeps spritesmith from
// being talked into writing a file somewhere else entirely -- a link planted
// in a world-writable directory, pointed at something the caller can write and
// did not mean to replace.
//
// It also pins the two halves of the guard to the same definition of "taken".
// A dangling symlink does not exist to Stat but does to O_EXCL, so a
// pre-flight built on Stat would call this path free and only fail later, at
// the write, having already spent the generation.
func TestASymlinkAtTheOutputPathIsRefusedAndNotFollowed(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "somewhere-else.png")
	link := filepath.Join(dir, "out.png")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlinks are unavailable here: %v", err)
	}

	if err := checkOutputPathFree(link); err == nil {
		t.Error("a symlink was reported as a free output path, so the refusal comes only after the generation is paid for")
	}
	if err := writeOutput(link, strings.NewReader(apiResponseBody)); err == nil {
		t.Fatal("the result was written through a symlink")
	}
	if _, err := os.Lstat(target); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the symlink target exists, so the write followed the link: %v", err)
	}
}

// Naming a path under a directory that does not exist yet still works: the
// parent is created. Convenience, but it is existing behaviour and losing it
// silently would break anybody scripting `--out build/assets/thing.png`.
func TestAMissingParentDirectoryIsCreated(t *testing.T) {
	srv := httptest.NewServer(&recordingAPI{})
	t.Cleanup(srv.Close)

	want := filepath.Join(t.TempDir(), "build", "assets", "thing.png")
	if _, err := (Client{BaseURL: srv.URL}).Process(Request{
		Mode:       ModePrompt,
		Prompt:     "a duck",
		OutputPath: want,
	}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(want)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != apiResponseBody {
		t.Errorf("the file holds %q, want the API's response body", string(data))
	}
}

// failingReader stops partway, the way a dropped connection does.
type failingReader struct{ sent bool }

func (f *failingReader) Read(p []byte) (int, error) {
	if f.sent {
		return 0, errors.New("the connection dropped")
	}
	f.sent = true
	return copy(p, []byte("half a PNG and then nothing")), nil
}

// A download that fails partway must leave nothing behind.
//
// O_EXCL has already created the file by the time the copy starts, so without
// the removal a network hiccup leaves a truncated PNG the caller never asked
// for -- and the refusal to overwrite, seeing a file, then blocks the retry
// with a message about protecting a result that is really half a download.
// Refusing to overwrite is only safe if spritesmith never leaves a broken file.
func TestAFailedDownloadLeavesNothingBehind(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "out.png")

	err := writeOutput(path, &failingReader{})
	if err == nil {
		t.Fatal("a failed copy was reported as success")
	}
	if !strings.Contains(err.Error(), "the connection dropped") {
		t.Errorf("err = %q, want the underlying failure", err)
	}

	if _, statErr := os.Lstat(path); !errors.Is(statErr, fs.ErrNotExist) {
		data, _ := os.ReadFile(path)
		t.Fatalf("a truncated file was left at the output path (%q); the next run would refuse to overwrite it", string(data))
	}
	// Nothing else either -- not an empty file under another name, not the
	// parent left holding a stray.
	entries, readErr := os.ReadDir(dir)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Errorf("the failed run left %d entries behind, want none", len(entries))
	}
}

// And the same thing end to end, because the path that matters is the one
// Process takes: the caller must be free to simply run the command again.
func TestAFailedDownloadLeavesTheOutputPathFreeForARetry(t *testing.T) {
	var attempts atomic.Int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		if attempts.Add(1) == 1 {
			// Promise more than is delivered, then hang up: io.Copy sees an
			// unexpected EOF partway through.
			w.Header().Set("Content-Length", "4096")
			_, _ = w.Write([]byte("half a PNG"))
			if flusher, canFlush := w.(http.Flusher); canFlush {
				flusher.Flush()
			}
			panic(http.ErrAbortHandler)
		}
		_, _ = w.Write([]byte(apiResponseBody))
	}))
	t.Cleanup(srv.Close)
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)

	path := filepath.Join(t.TempDir(), "out.png")
	req := Request{Mode: ModePrompt, Prompt: "a duck", OutputPath: path}

	if _, err := (Client{BaseURL: srv.URL}).Process(req); err == nil {
		t.Fatal("a truncated download was reported as success")
	}

	// The retry, which is the whole point.
	if _, err := (Client{BaseURL: srv.URL}).Process(req); err != nil {
		t.Fatalf("the retry was refused, so the failed run wedged the output path: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != apiResponseBody {
		t.Errorf("the file holds %q, want the retry's complete response", string(data))
	}
}

// A 200 that is not a PNG must not be written. Accept is advisory, so a
// captive portal or a proxy error page answering 200 with HTML would otherwise
// land in spritesmith-alpha.png as a perfectly valid file that is not an image --
// and, the path now being taken, the retry would be refused too.
func TestANonPNGSuccessResponseIsRefusedAndNotWritten(t *testing.T) {
	for _, answer := range []struct {
		name        string
		contentType string
		body        string
	}{
		{"html", "text/html; charset=utf-8", "<html>sign in to this network</html>"},
		{"json", "application/json", `{"status":"ok"}`},
		{"none", "", "who knows"},
	} {
		t.Run(answer.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				if answer.contentType != "" {
					w.Header().Set("Content-Type", answer.contentType)
				} else {
					// Go sniffs a type when none is set, so unset it outright.
					w.Header()["Content-Type"] = nil
				}
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(answer.body))
			}))
			t.Cleanup(srv.Close)

			dir := t.TempDir()
			path := filepath.Join(dir, "out.png")

			_, err := (Client{BaseURL: srv.URL}).Process(Request{
				Mode:       ModePrompt,
				Prompt:     "a duck",
				OutputPath: path,
			})
			if err == nil {
				t.Fatal("a non-PNG 200 was accepted as a result")
			}
			if _, statErr := os.Lstat(path); !errors.Is(statErr, fs.ErrNotExist) {
				data, _ := os.ReadFile(path)
				t.Errorf("the non-PNG body was written to disk: %q", string(data))
			}
			entries, readErr := os.ReadDir(dir)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if len(entries) != 0 {
				t.Errorf("%d entries were left behind, want none", len(entries))
			}
		})
	}
}

// The real API serves the sheet through gin's c.File, which sets a bare
// image/png; a parameter on it is still a PNG and must not be refused.
func TestAPNGContentTypeWithParametersIsAccepted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png; name=alpha.png")
		_, _ = w.Write([]byte(apiResponseBody))
	}))
	t.Cleanup(srv.Close)

	path := filepath.Join(t.TempDir(), "out.png")
	if _, err := (Client{BaseURL: srv.URL}).Process(Request{
		Mode:       ModePrompt,
		Prompt:     "a duck",
		OutputPath: path,
	}); err != nil {
		t.Fatalf("a PNG with a content-type parameter was refused: %v", err)
	}
}

// --out meant a directory until spritesmith collapsed to one output, so the old
// command is still in shell histories. Caught before the request it costs
// nothing; caught at the write it costs a generation and reports a bare errno.
func TestADirectoryShapedOutIsRefusedBeforeAnythingIsSent(t *testing.T) {
	existing := t.TempDir()

	for _, path := range []string{
		// Does not exist yet, so only the trailing separator gives it away.
		filepath.Join(t.TempDir(), "spritesmith-out") + string(os.PathSeparator),
		// Exists, and is a directory.
		existing,
	} {
		t.Run(path, func(t *testing.T) {
			c := Client{
				BaseURL:    "http://127.0.0.1:8080",
				HTTPClient: &http.Client{Transport: refusingTransport{t}},
			}

			_, err := c.Process(Request{Mode: ModePrompt, Prompt: "a duck", OutputPath: path})
			if err == nil {
				t.Fatal("a directory was accepted as the output path")
			}
			// The message has to say what is wrong with it. "already exists,
			// and spritesmith will not overwrite it. Move or delete that file" is
			// the wrong noun and the wrong advice for a directory.
			if !strings.Contains(err.Error(), "directory") {
				t.Errorf("err = %q, want it to say the path is a directory", err)
			}
			if !strings.Contains(err.Error(), "--out") {
				t.Errorf("err = %q, want it to name the fix", err)
			}
		})
	}
}

// An empty or whitespace-only OutputPath falls back to the default rather than
// resolving to ".", which filepath treats a bare "" as and which is a
// directory, not somewhere a PNG can be written.
func TestAnEmptyOutputPathFallsBackToTheDefaultFile(t *testing.T) {
	srv := httptest.NewServer(&recordingAPI{})
	t.Cleanup(srv.Close)

	for _, given := range []string{"", "   "} {
		t.Run(fmt.Sprintf("%q", given), func(t *testing.T) {
			dir := t.TempDir()
			t.Chdir(dir)

			resp, err := (Client{BaseURL: srv.URL}).Process(Request{
				Mode:       ModePrompt,
				Prompt:     "a duck",
				OutputPath: given,
			})
			if err != nil {
				t.Fatal(err)
			}
			if resp.OutputPath != DefaultOutputPath {
				t.Errorf("OutputPath = %q, want %q", resp.OutputPath, DefaultOutputPath)
			}
			if _, statErr := os.Lstat(filepath.Join(dir, DefaultOutputPath)); statErr != nil {
				t.Errorf("nothing was written to the default path: %v", statErr)
			}
		})
	}

	// A path somebody actually gave is used exactly as given -- not trimmed,
	// because a trailing space in a filename is legal and not ours to remove.
	// The space is at the very END of the path on purpose: an earlier version
	// of this fixture used "sprite .png", where the space is interior and
	// TrimSpace would not have touched it, so the assertion held under the
	// exact mutation it exists to catch.
	dir := t.TempDir()
	odd := filepath.Join(dir, "sprite.png") + " "
	resp, err := (Client{BaseURL: srv.URL}).Process(Request{
		Mode:       ModePrompt,
		Prompt:     "a duck",
		OutputPath: odd,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.OutputPath != odd {
		t.Errorf("OutputPath = %q, want the path exactly as given, %q", resp.OutputPath, odd)
	}
}

// The default name is a decision, not an accident: it is what the app saves
// (RESULT_NAME in app/src/lib/spritesmith-request.ts), so a result is the same thing
// wherever it came from. cli/.gitignore hardcodes it too, and would go quietly
// stale if the constant moved -- `make cli` runs from cli/, so the default
// lands in the source tree.
func TestTheDefaultOutputNameIsTheOneEverythingElseExpects(t *testing.T) {
	if DefaultOutputPath != "spritesmith-alpha.png" {
		t.Errorf("DefaultOutputPath = %q; the app saves spritesmith-alpha.png and a result should be the same thing wherever it came from", DefaultOutputPath)
	}

	ignore, err := os.ReadFile(filepath.Join("..", "..", ".gitignore"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(ignore), DefaultOutputPath) {
		t.Errorf("cli/.gitignore does not list %q, so `make cli` leaves a result in the source tree:\n%s", DefaultOutputPath, ignore)
	}
}
