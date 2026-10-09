package client

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ErrUnauthorized is a 401 from the API: the request needed an identity and
// did not establish one.
//
// Returned as a sentinel with no detail because the useful message depends on
// something this package does not know -- whether a token was configured at
// all, and where it would go. The command builds that; see cmd/spritesmith.
var ErrUnauthorized = errors.New("the API rejected this request as unauthenticated")

type Mode string

const (
	ModeImage  Mode = "image"
	ModePrompt Mode = "prompt"
)

// DefaultOutputPath is where a result lands when the caller names nowhere. A
// file rather than a directory, because there is exactly one of them, and the
// same name the app saves so a spritesmith result is the same thing wherever it
// came from.
const DefaultOutputPath = "spritesmith-alpha.png"

type Request struct {
	Kind        string
	Format      string
	OperationID string
	Mode        Mode
	Prompt      string
	SourcePath  string
	OutputPath  string
}

type Response struct {
	OutputPath  string
	ContentType string
}

type Client struct {
	BaseURL    string
	HTTPClient *http.Client
	// Token is the Clerk API key (`ak_…`), sent as a bearer credential. Empty
	// sends no Authorization header at all, which is what a local API with no
	// Clerk configuration expects and is why `make cli` works with nothing set
	// up. See internal/token for where it comes from.
	Token string
}

func (c Client) Process(req Request) (Response, error) {
	if req.Kind == "" {
		req.Kind = "raster"
	}
	if req.Format == "" {
		req.Format = "png"
	}
	if (req.Kind != "raster" && req.Kind != "vector") || (req.Format != "png" && req.Format != "svg") || (req.Kind == "raster" && req.Format == "svg") {
		return Response{}, errors.New("choose raster/png, vector/png, or vector/svg")
	}
	if c.BaseURL == "" {
		c.BaseURL = "http://127.0.0.1:8080"
	}
	// Before anything is built, let alone sent. A token is a bearer credential:
	// anyone who reads it off the wire is that person until it is revoked.
	//
	// First of the two pre-flight checks, ahead of the output path, because
	// when both would fail this is the one that matters: being told to move a
	// file, fixing that, and only then learning the token was one step from
	// crossing the network in the clear gets the order exactly backwards.
	if c.Token != "" {
		if err := checkTokenTransport(c.BaseURL); err != nil {
			return Response{}, err
		}
	}
	// An all-whitespace path is a mistake, but a path is otherwise taken
	// verbatim -- trailing spaces in a filename are legal and not ours to trim.
	outputPath := req.OutputPath
	if strings.TrimSpace(outputPath) == "" {
		outputPath = DefaultOutputPath
	}
	// Checked before the request rather than after it. A generation costs an
	// OpenAI call and a minute of somebody's attention; discovering the
	// collision on the way to disk means paying for a result that is then
	// thrown away. The write itself re-checks atomically -- see writeOutput.
	if err := checkOutputPathFree(outputPath); err != nil {
		return Response{}, err
	}
	httpClient := c.HTTPClient
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if c.Token != "" {
		httpClient = guardRedirects(httpClient)
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err := writer.WriteField("kind", req.Kind); err != nil {
		return Response{}, err
	}
	// Keep compatibility with older API deployments. Current APIs ignore this
	// field and always serve the packed transparent PNG.
	if err := writer.WriteField("sheet", "true"); err != nil {
		return Response{}, err
	}

	endpoint := "/v1/image"
	switch req.Mode {
	case ModeImage:
		if err := addFile(writer, "image", req.SourcePath); err != nil {
			return Response{}, err
		}
	case ModePrompt:
		endpoint = "/v1/prompt"
		if strings.TrimSpace(req.Prompt) == "" {
			return Response{}, errors.New("prompt is required")
		}
		if err := writer.WriteField("prompt", req.Prompt); err != nil {
			return Response{}, err
		}
	default:
		return Response{}, fmt.Errorf("unsupported mode %q", req.Mode)
	}
	if err := writer.Close(); err != nil {
		return Response{}, err
	}

	httpReq, err := http.NewRequest(http.MethodPost, strings.TrimRight(c.BaseURL, "/")+endpoint, &body)
	if err != nil {
		return Response{}, err
	}
	httpReq.Header.Set("Content-Type", writer.FormDataContentType())
	httpReq.Header.Set("Accept", "image/png, application/json")
	operation := req.OperationID
	if operation == "" {
		value := make([]byte, 16)
		rand.Read(value)
		operation = hex.EncodeToString(value)
	}
	if len(operation) > 128 {
		return Response{}, errors.New("operation ID is too long")
	}
	for _, character := range operation {
		if !(character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' || character >= '0' && character <= '9' || character == '-' || character == '_') {
			return Response{}, errors.New("invalid operation ID")
		}
	}
	httpReq.Header.Set("Idempotency-Key", operation)
	if c.Token != "" {
		httpReq.Header.Set("Authorization", "Bearer "+c.Token)
	}

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		return Response{}, fmt.Errorf("generation submission interrupted; retain --operation-id %s for a safe retry: %w", operation, err)
	}
	if resp.StatusCode == http.StatusAccepted {
		initial := resp
		resp, err = c.awaitGeneration(httpClient, initial, req.Format)
		initial.Body.Close()
		if err != nil {
			return Response{}, err
		}
	}
	defer resp.Body.Close()

	// A 401 is answered by the caller, not reported. The API's own message is
	// deliberately uninformative -- telling bad tokens apart out loud is free
	// reconnaissance -- so repeating it here would be a bare 401 dump and
	// nothing else. See ErrUnauthorized.
	if resp.StatusCode == http.StatusUnauthorized {
		return Response{}, ErrUnauthorized
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		message, err := decodeError(resp.Body)
		if err != nil {
			return Response{}, fmt.Errorf("API returned %s", resp.Status)
		}
		return Response{}, errors.New(message)
	}

	// A 200 is not a PNG. Accept is advisory, and a captive portal, a proxy
	// error page or a JSON body carrying a 200 would otherwise be written
	// verbatim to a .png -- an asset that is silently not an image, and, since
	// the path is now taken, a retry that gets refused. Content-Type is the
	// only signal there is before the body reaches disk.
	contentType := resp.Header.Get("Content-Type")
	expectedType := "image/png"
	if req.Format == "svg" {
		expectedType = "image/svg+xml"
	}
	if mediaType, _, err := mime.ParseMediaType(contentType); err != nil || mediaType != expectedType {
		return Response{}, fmt.Errorf("the API answered %s with content type %q, which is not the sprite format requested", resp.Status, contentType)
	}

	if err := writeOutput(outputPath, resp.Body); err != nil {
		return Response{}, err
	}
	return Response{OutputPath: outputPath, ContentType: contentType}, nil
}

func (c Client) awaitGeneration(client *http.Client, initial *http.Response, format string) (*http.Response, error) {
	var job struct {
		ID string `json:"id"`
	}
	if err := json.NewDecoder(io.LimitReader(initial.Body, 4096)).Decode(&job); err != nil || !validJobID(job.ID) {
		return nil, errors.New("API returned an invalid generation identity")
	}
	endpoint := strings.TrimRight(c.BaseURL, "/") + "/v1/generations/" + job.ID
	for {
		request, err := http.NewRequest(http.MethodGet, endpoint, nil)
		if err != nil {
			return nil, err
		}
		if c.Token != "" {
			request.Header.Set("Authorization", "Bearer "+c.Token)
		}
		response, err := client.Do(request)
		if err != nil {
			return nil, fmt.Errorf("generation %s remains recoverable: %w", job.ID, err)
		}
		if response.StatusCode != http.StatusOK {
			response.Body.Close()
			return nil, fmt.Errorf("generation %s status unavailable (%d)", job.ID, response.StatusCode)
		}
		var state struct {
			Status string `json:"status"`
		}
		err = json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&state)
		response.Body.Close()
		if err != nil {
			return nil, err
		}
		switch state.Status {
		case "succeeded":
			request, err := http.NewRequest(http.MethodGet, endpoint+"/result?format="+format, nil)
			if err != nil {
				return nil, err
			}
			if c.Token != "" {
				request.Header.Set("Authorization", "Bearer "+c.Token)
			}
			return client.Do(request)
		case "uncertain":
			return nil, fmt.Errorf("generation %s needs reconciliation; no paid resubmission was attempted", job.ID)
		case "failed":
			return nil, fmt.Errorf("generation %s failed", job.ID)
		case "receiving", "queued", "claimed", "submitting", "processing":
			time.Sleep(500 * time.Millisecond)
		default:
			return nil, errors.New("API returned an unknown generation state")
		}
	}
}

func validJobID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, c := range id {
		if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// checkOutputPathFree refuses a path that is already taken.
//
// Overwriting is the wrong default for output that costs money to produce and
// cannot be reproduced: the same prompt does not generate the same image
// twice, so a clobbered result is gone. Suffixing was the other candidate and
// is worse where it matters -- `spritesmith prompt --out out.png ... && optimize
// out.png` would quietly read yesterday's file while today's sat beside it
// under a name nobody asked for. Refusing fails loudly instead, which a script
// can act on and a person can read.
//
// Lstat, not Stat, so this agrees with the O_EXCL below: a symlink pointing at
// nothing exists as far as the kernel is concerned, and reporting it free here
// would only move the failure later.
//
// It also catches a --out that names a directory. That was what --out meant
// until spritesmith collapsed to one output, so `--out spritesmith-out/` is the mistake
// to expect from anybody with the old command in their shell history -- and
// left to the write it would surface as a bare "is a directory" errno, after
// the generation had been paid for.
func checkOutputPathFree(path string) error {
	if hasTrailingSeparator(path) {
		return fmt.Errorf("--out %s names a directory, and spritesmith writes a single PNG.\n"+
			"  Give it a file path instead, for example %s%s", path, path, DefaultOutputPath)
	}
	info, err := os.Lstat(path)
	if err == nil {
		if info.IsDir() {
			return fmt.Errorf("%s is a directory, and spritesmith writes a single PNG.\n"+
				"  Pass --out with a file path instead", path)
		}
		return errOutputExists(path)
	}
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	return err
}

func hasTrailingSeparator(path string) bool {
	if path == "" {
		return false
	}
	last := path[len(path)-1]
	return last == '/' || last == os.PathSeparator
}

func errOutputExists(path string) error {
	return fmt.Errorf("%s already exists, and spritesmith will not overwrite it.\n"+
		"  Move or delete that file, or pass --out with a different path", path)
}

// writeOutput saves the one PNG.
//
// O_EXCL rather than os.Create, for two reasons. It closes the gap between the
// pre-flight check and here -- a whole network round trip, during which
// anything may appear at this path -- so the promise not to overwrite is the
// kernel's rather than a hope about timing. And because O_EXCL fails on a
// symlink, a link sitting AT the output path is refused rather than followed.
// Only that last component: the directories above it are resolved normally, so
// this is not a defence against a planted parent directory, and no comment here
// should suggest it is.
//
// A failed copy takes the file with it. O_EXCL has already created it, so
// without the removal a dropped connection leaves a truncated PNG that the
// caller never asked for -- and the refusal above, seeing a file, would then
// block the retry with a message about protecting a result that is really half
// a download. Nothing but this call could have created the file, so removing it
// destroys nothing.
func writeOutput(path string, body io.Reader) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if errors.Is(err, fs.ErrExist) {
			return errOutputExists(path)
		}
		return err
	}
	_, copyErr := io.Copy(out, body)
	closeErr := out.Close()
	if copyErr != nil || closeErr != nil {
		if removeErr := os.Remove(path); removeErr != nil && !errors.Is(removeErr, fs.ErrNotExist) {
			return fmt.Errorf("%w (and the incomplete file could not be removed: %v)", errors.Join(copyErr, closeErr), removeErr)
		}
		return errors.Join(copyErr, closeErr)
	}
	return nil
}

// checkTokenTransport refuses to put a bearer credential on a connection that
// anybody in the path can read.
//
// --api and SPRITESMITH_API_BASE are ordinary strings, and http:// is the default
// this CLI ships with, so pointing a token at a plaintext host is one typo or
// one stale shell export away. On the wire the token is not a password to
// crack, it IS the access -- for as long as it takes somebody to notice and
// revoke it, which for a long-lived key is forever.
//
// Loopback is allowed because that is the local API, the traffic never leaves
// the machine, and refusing it would break the one workflow this CLI is used
// for daily.
func checkTokenTransport(base string) error {
	parsed, err := url.Parse(base)
	if err != nil {
		return fmt.Errorf("invalid API base URL %q: %w", base, err)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "https":
		return nil
	case "http":
		if isLoopback(parsed.Hostname()) {
			return nil
		}
	}
	return fmt.Errorf(
		"refusing to send the spritesmith API token to %s: it is neither https nor a local address, so the token would cross the network in the clear.\n"+
			"  Point --api or SPRITESMITH_API_BASE at an https:// URL, or unset %s to send no token",
		base, "SPRITESMITH_API_TOKEN")
}

// guardRedirects re-runs the transport check on every hop, because checking
// the base URL alone does not hold the token safe.
//
// Go copies the Authorization header across a redirect whenever the
// destination is the same host or a subdomain of it -- net/http's
// shouldCopyHeaderOnRedirect compares hostnames and says nothing about the
// scheme. So `https://api.spritesmith.ai` answering `302 http://api.spritesmith.ai/...`
// puts the credential on the wire in cleartext, having passed a check that
// only ever saw the https base. The multipart body is a bytes.Buffer, so
// GetBody is set and even a 307 replays cleanly: this is reachable, not
// theoretical.
//
// Same-host only, which closes a second copy of the same hole. Go's rule is
// host OR SUBDOMAIN, so `https://api.spritesmith.ai` answering
// `302 https://anything.spritesmith.ai/...` also carries the token -- over TLS, but
// to a host that never had it. That is a subdomain takeover away from being a
// credential handover, and the spritesmith API has no reason to redirect across
// hosts at all. Same host with a different path or port stays allowed, which
// is every legitimate redirect there is.
//
// The client is COPIED. Mutating http.DefaultClient would change redirect
// behaviour for every other caller in the process, and mutating one the caller
// passed in is not this function's to do.
func guardRedirects(client *http.Client) *http.Client {
	guarded := *client
	previous := guarded.CheckRedirect
	guarded.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		// The caller's own policy runs FIRST, so a policy that stops the
		// redirect -- http.ErrUseLastResponse, say -- is honoured rather than
		// overridden by a refusal about a hop that was never going to be
		// followed. Anything this guard refuses below is a hop that WOULD have
		// been followed with the token attached, which is the only case it is
		// about.
		if previous != nil {
			if err := previous(req, via); err != nil {
				return err
			}
		} else if len(via) >= 10 {
			// net/http's own default, which replacing CheckRedirect switches
			// off. Without it the CLI hangs forever on a redirect loop.
			return errors.New("stopped after 10 redirects")
		}

		if err := checkTokenTransport(req.URL.String()); err != nil {
			return err
		}
		// via[0] is the ORIGINAL request, not the previous hop, which is both
		// what Go's own header-copy rule compares against and the only choice
		// that is safe: comparing against the previous hop would allow a chain
		// to launder its way across, one plausible step at a time.
		if origin := via[0].URL.Hostname(); !strings.EqualFold(req.URL.Hostname(), origin) {
			return fmt.Errorf(
				"refusing to follow a redirect from %s to %s while carrying the spritesmith API token: "+
					"%s is not the host you pointed the CLI at.\n"+
					"  Point --api or SPRITESMITH_API_BASE at %s directly, or unset %s to send no token",
				origin, req.URL.Hostname(), req.URL.Hostname(), req.URL.Hostname(), "SPRITESMITH_API_TOKEN")
		}
		return nil
	}
	return &guarded
}

func isLoopback(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func addFile(writer *multipart.Writer, field string, path string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("%s: %w", field, err)
	}
	defer file.Close()

	part, err := writer.CreateFormFile(field, filepath.Base(path))
	if err != nil {
		return err
	}
	_, err = io.Copy(part, file)
	return err
}

func decodeError(reader io.Reader) (string, error) {
	data, err := io.ReadAll(reader)
	if err != nil {
		return "", err
	}
	var payload struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(data, &payload) == nil && payload.Error != "" {
		return payload.Error, nil
	}
	message := strings.TrimSpace(string(data))
	if message == "" {
		return "", fmt.Errorf("empty error body")
	}
	return message, nil
}
