<!-- Generated from private documentation source. Do not edit directly. Source SHA256: 589de7d710c66ee60b62b4b887563486e68b5f43ddca9151cf586e4a610c8319 -->

# spritesmith

Command-line client for the spritesmith API. Create animated vector sprites and download finished PNG or SVG files.

The client requires an API service and an account token. It does not contain a local renderer or generation engine.

## Install

Linux and macOS (x86_64 or arm64), using prebuilt binaries:

With curl:

```sh
curl -fsSL https://spritesmith.ai/install | sh
```

Or with mise:

```sh
mise use --global github:aviorstudio/spritesmith@latest
```

The shell installer installs to `~/.local/bin`; add that directory to your PATH if necessary. Mise manages its own installation directory and PATH through its shell activation.

## Configure

Set `SPRITESMITH_API_BASE` to your service URL and `SPRITESMITH_API_TOKEN` to your account's Clerk API key. The token can also live in a private `spritesmith/token` file under your operating system's user configuration directory. Run `spritesmith --help` to see its path. Token files must not be accessible to other users.

Hosted tokens require HTTPS. Loopback HTTP is supported for local development.

## Create sprites

```sh
spritesmith prompt --operation-id hero-v1 --out hero.png 'A small forest ranger'
spritesmith prompt --kind vector --format svg --operation-id emblem-v1 --out emblem.svg 'A golden shield emblem'
spritesmith prompt --kind vector --format png --out emblem.png 'A golden shield emblem'
```

Use `--model haiku` (the default) or `--model opus` to choose the reasoning model. The API freezes this choice with the request, including retries. Both use medium reasoning effort.

Flags come before the prompt. Use the web app to attach reference images, work with several sprites in a session, and publish revisions.

The service creates and reviews vector animations through its agent workflow. This client downloads the default pose as SVG or PNG; the web app also exports animation sheets and frames.

Generation runs as a server job. The client waits for completion and downloads the requested file. Retain `--operation-id` and reuse it with the same input after an interruption. Existing output files are never overwritten. An uncertain job requires reconciliation; the client does not automatically submit another paid request.

The default API is `http://127.0.0.1:8080`; configure your hosted service explicitly. Default output is `spritesmith.png`, or `spritesmith.svg` for SVG downloads.

## Contributor verification

`mise.toml` pins contributor SDKs. The canonical generated task block in root `mise.toml` selects an immutable revision of the shared cookbook and binds it to `cookbook.toml`.

```sh
mise run help
mise run setup
mise run build
mise run test
```

`mise run help` works offline before setup. `mise run setup` explicitly prepares the pinned SDKs, locked dependencies and declared fixtures. `mise run build` compiles the executable, packages the native archive and verifies its source and output identity. `mise run test` runs lint, deterministic native unit tests, security checks and the declared integration journeys against those verified artifacts. Run build again after changing source; tests do not install tools or build missing shipping artifacts.

Use `mise run fmt` to apply formatting, `mise run test:lint`, `mise run test:unit`, `mise run test:security` or `mise run test:integration` to select a group, and `mise run clean` to remove the declared generated outputs. Build and test evidence is recorded in `.artifacts/verification`.

The verified executable is `.artifacts/build/spritesmith` (`spritesmith.exe` on Windows). CI retains all five published targets: Linux and macOS on x86_64 and arm64, and Windows x86_64. Published Linux binaries use musl, and Windows binaries include the C runtime. Release archives retain their existing contents, checksums and source-workflow build provenance. Integration journeys exercise the real client against disposable fixtures and verify the installer. They do not submit paid generation requests.

## Machine results and configuration

Use `spritesmith --json prompt [flags] PROMPT` or `spritesmith prompt --json [flags] PROMPT` for one JSON result. Successful results identify the saved image path, format and actual operation ID. Retain that ID for safe retries. Version and help also accept the leading `--json` flag. Diagnostics use stderr; invalid syntax exits 2 and a failed operation exits 1.

`SPRITESMITH_CONFIG_DIR` overrides the OS configuration directory and contains the `token` file. Without it, the platform's existing application configuration path is used. `SPRITESMITH_API_TOKEN` takes precedence over the file. No token appears in command arguments or machine results.
