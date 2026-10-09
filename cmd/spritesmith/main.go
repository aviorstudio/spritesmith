package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/aviorstudio/spritesmith/internal/client"
	"github.com/aviorstudio/spritesmith/internal/token"
)

var version = "dev"
var revision = "unknown"

const defaultAPIBase = "http://127.0.0.1:8080"

type commonFlags struct {
	operationID *string
	apiBase     *string
	out         *string
	kind        *string
	format      *string
}

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 {
		printUsage(os.Stderr)
		return 2
	}

	switch args[0] {
	case "--version", "version":
		fmt.Fprintf(os.Stdout, "spritesmith %s (%s)\n", version, revision)
		return 0
	case "image":
		return runImage(args[1:])
	case "prompt":
		return runPrompt(args[1:])
	case "-h", "--help", "help":
		printUsage(os.Stdout)
		return 0
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", args[0])
		printUsage(os.Stderr)
		return 2
	}
}

func runImage(args []string) int {
	apiBase, req, ok := parseImage(args)
	if !ok {
		return 2
	}
	return submit(apiBase, req)
}

func runPrompt(args []string) int {
	apiBase, req, ok := parsePrompt(args)
	if !ok {
		return 2
	}
	return submit(apiBase, req)
}

// parseImage and parsePrompt are separate from the submit they feed so that
// something can test the seam between a flag and the request it builds.
// Folded into run* they were untestable, and losing a line there is invisible:
// dropping OutputPath makes --out do nothing, and the CLI still prints a path
// and still writes a PNG, just never the one that was asked for.
func parseImage(args []string) (apiBase string, req client.Request, ok bool) {
	fs, flags := newCommandFlags("image")
	if err := fs.Parse(args); err != nil {
		return "", client.Request{}, false
	}
	if fs.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: spritesmith image [flags] IMAGE")
		return "", client.Request{}, false
	}

	return *flags.apiBase, client.Request{
		OperationID: *flags.operationID,
		Mode:        client.ModeImage,
		SourcePath:  fs.Arg(0),
		OutputPath:  outputPath(flags),
		Kind:        *flags.kind, Format: *flags.format,
	}, true
}

func parsePrompt(args []string) (apiBase string, req client.Request, ok bool) {
	fs, flags := newCommandFlags("prompt")
	terminated := promptFlagsTerminated(args, fs)
	if err := fs.Parse(args); err != nil {
		return "", client.Request{}, false
	}
	if misplaced := misplacedPromptFlag(fs, terminated); misplaced != "" {
		fmt.Fprintf(os.Stderr, "misplaced flag %q: flags must come before PROMPT; try: spritesmith prompt --out x.png \"a duck\"\n", misplaced)
		return "", client.Request{}, false
	}
	prompt := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if prompt == "" {
		fmt.Fprintln(os.Stderr, "usage: spritesmith prompt [flags] PROMPT")
		return "", client.Request{}, false
	}

	return *flags.apiBase, client.Request{
		OperationID: *flags.operationID,
		Mode:        client.ModePrompt,
		Prompt:      prompt,
		OutputPath:  outputPath(flags),
		Kind:        *flags.kind, Format: *flags.format,
	}, true
}

func misplacedPromptFlag(fs *flag.FlagSet, terminated bool) string {
	remaining := fs.Args()
	if len(remaining) == 0 || terminated {
		return ""
	}

	for _, arg := range remaining[1:] {
		name := strings.TrimLeft(arg, "-")
		if name == arg || name == "" {
			continue
		}
		if before, _, found := strings.Cut(name, "="); found {
			name = before
		}
		if fs.Lookup(name) != nil {
			return arg
		}
	}
	return ""
}

func promptFlagsTerminated(args []string, fs *flag.FlagSet) bool {
	for len(args) > 0 {
		arg := args[0]
		if arg == "--" {
			return true
		}
		if len(arg) < 2 || arg[0] != '-' {
			return false
		}

		name := strings.TrimPrefix(strings.TrimPrefix(arg, "-"), "-")
		name, _, hasValue := strings.Cut(name, "=")
		defined := fs.Lookup(name)
		if defined == nil {
			return false
		}
		args = args[1:]
		if hasValue {
			continue
		}
		if boolean, ok := defined.Value.(interface{ IsBoolFlag() bool }); ok && boolean.IsBoolFlag() {
			continue
		}
		if len(args) == 0 {
			return false
		}
		args = args[1:]
	}
	return false
}

func newCommandFlags(name string) (*flag.FlagSet, commonFlags) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	flags := commonFlags{
		operationID: fs.String("operation-id", "", "reuse the same logical generation after an interrupted submission"),
		apiBase:     fs.String("api", envDefault("SPRITESMITH_API_BASE", defaultAPIBase), "spritesmith API base URL"),
		out:         fs.String("out", "", "output filename"),
		kind:        fs.String("kind", "raster", "raster or vector"),
		format:      fs.String("format", "png", "png or svg (vector only)"),
	}
	return fs, flags
}

func outputPath(flags commonFlags) string {
	if *flags.out != "" {
		return *flags.out
	}
	if *flags.format == "svg" {
		return "spritesmith.svg"
	}
	return client.DefaultOutputPath
}

func submit(apiBase string, req client.Request) int {
	loader := token.New()
	credential, err := loader.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if credential.Warning != "" {
		fmt.Fprintln(os.Stderr, "warning: "+credential.Warning)
	}

	resp, err := client.Client{BaseURL: apiBase, Token: credential.Value}.Process(req)
	if err != nil {
		// A bare "401 Unauthorized" tells somebody nothing they can act on.
		// This is the one error the CLI can explain, so it does.
		if errors.Is(err, client.ErrUnauthorized) {
			fmt.Fprintln(os.Stderr, unauthorizedHelp(loader, credential))
			return 1
		}
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Fprintln(os.Stdout, resp.OutputPath)
	return 0
}

// unauthorizedHelp turns a 401 into the fix. It names the source of the token
// and never the token itself.
func unauthorizedHelp(loader token.Loader, credential token.Token) string {
	if credential.Value == "" {
		path := loader.PathOrHint()
		return "unauthorized: this API requires a spritesmith API token and none is configured.\n" +
			"  Create a Clerk API key for your spritesmith account, then either:\n" +
			"    export " + token.EnvVar + "=<token>\n" +
			"  or write the token to " + path + " and run: chmod 600 " + path
	}
	return "unauthorized: the API rejected the spritesmith API token from " + credential.Source + ".\n" +
		"  It may have been revoked, or it may have expired. Replace it and try again."
}

// printUsage writes the help. Takes an io.Writer rather than *os.File so a
// test can read what it says -- the authentication section below is the only
// place anybody is told how to get a token, so it is worth asserting on.
func printUsage(out io.Writer) {
	fmt.Fprintln(out, "usage: spritesmith COMMAND [flags]")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Commands:")
	fmt.Fprintln(out, "  image IMAGE                 make a sprite from a reference image through the API")
	fmt.Fprintln(out, "  prompt PROMPT               make a sprite from a text prompt through the API")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Flags:")
	fmt.Fprintln(out, "  --kind raster|vector        creation method, default raster")
	fmt.Fprintln(out, "  --format png|svg            download format, default png; SVG requires vector")
	fmt.Fprintln(out, "  --operation-id ID           retain this ID for safe retries")
	fmt.Fprintln(out, "  --api URL                   API base URL, default http://127.0.0.1:8080")
	fmt.Fprintf(out, "  --out FILE                  where to write the sprite, default %s\n", client.DefaultOutputPath)
	fmt.Fprintln(out)
	tokenPath := token.New().PathOrHint()
	fmt.Fprintln(out, "Authentication:")
	fmt.Fprintln(out, "  A hosted spritesmith API requires a token: a Clerk API key created for your")
	fmt.Fprintln(out, "  spritesmith account. An explicitly configured local development API needs none.")
	fmt.Fprintln(out)
	fmt.Fprintf(out, "    export %s=<token>\n", token.EnvVar)
	// -D, so this works on a machine that has never had the directory.
	fmt.Fprintf(out, "    # or:  install -D -m 600 /dev/null %s && $EDITOR %s\n", tokenPath, tokenPath)
	fmt.Fprintln(out)
	fmt.Fprintf(out, "  %s wins when both are set. There is no --token flag on purpose:\n", token.EnvVar)
	fmt.Fprintln(out, "  an argument is visible in the shell history and in the process list.")
	fmt.Fprintln(out, "  The token is only ever sent over https, or to a loopback address.")
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Output:")
	fmt.Fprintf(out, "  Each run downloads one PNG or SVG, to %s unless --out says\n", client.DefaultOutputPath)
	fmt.Fprintln(out, "  otherwise. An existing file is never overwritten: spritesmith stops and says so,")
	fmt.Fprintln(out, "  because the same request does not produce the same image twice.")
}

func envDefault(key string, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
