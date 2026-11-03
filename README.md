<!-- Generated from private documentation source. Do not edit directly. Source SHA256: fdc0a03bc9002222edf92db7eed99ad2d3c4f5fe62721885ba4392d60b8e7d3b -->

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

Tool versions and native tasks live in `mise.toml`. Prepare the pinned helper source and run the complete gate:

```sh
git clone https://github.com/nicodes/tools.git .ci-tools
git -C .ci-tools checkout --detach 091101094fedf5ce2e67c339c1c9c842d6a662dc
mise install
mise run check
```

Native build and test commands retain source and output checks, with receipts in `.artifacts/verification`. Existing product release rules and artifacts remain owned by this repository. End users install the product’s published distribution.

The client is implemented in Rust. To build it from source with the pinned Rust 1.94.0 toolchain, run `cargo build --release --locked`; the executable is `target/release/spritesmith` (`spritesmith.exe` on Windows). Local checks validate the native archive. GitHub Actions tests and packages all five published platforms before accepting a release.

## Contributor task interface

Contributor tool versions and task entry points live in the native Mise configuration. Use `mise run lint` for source checks, `mise run test` for the applicable behavioral tests, and `mise run check` for the complete supported gate. `mise run clean` removes declared generated outputs while preserving source, credentials and retained data. This package has no separate Mise development service; use its native entry points directly.

## Machine results and configuration

Use `spritesmith --json prompt [flags] PROMPT` or `spritesmith prompt --json [flags] PROMPT` for one JSON result. Successful results identify the saved image path, format and actual operation ID. Retain that ID for safe retries. Version and help also accept the leading `--json` flag. Diagnostics use stderr; invalid syntax exits 2 and a failed operation exits 1.

`SPRITESMITH_CONFIG_DIR` overrides the OS configuration directory and contains the `token` file. Without it, the platform's existing application configuration path is used. `SPRITESMITH_API_TOKEN` takes precedence over the file. No token appears in command arguments or machine results.
