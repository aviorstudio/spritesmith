<!-- Generated from private documentation source. Do not edit directly. Source SHA256: bfc0c6fa41c5f40cbbfb48ea1a67f9455f2dfa44350ee8310ef89ff8286dc19f -->

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

## Development

Install the [komizo release](https://github.com/nicodes/komizo#install) matching `komizo.lock`.

```sh
komizo check        # Complete project checks
komizo run build    # Build the declared distributable artifacts
komizo describe     # Inspect tools and focused stages
```
