# Spritesmith

Command-line client for the Spritesmith API. Create raster or vector sprites and download finished PNG or SVG files.

The client requires an API service and an account token. It does not contain a local renderer or generation engine.

## Install

With Go 1.27.2 or later:

```sh
go install github.com/aviorstudio/spritesmith/cmd/spritesmith@latest
```

## Configure

Set `SPRITESMITH_API_BASE` to your service URL and `SPRITESMITH_API_TOKEN` to your account's Clerk API key. The token can also live in a private `spritesmith/token` file under your operating system's user configuration directory. Run `spritesmith --help` to see its path. Token files must not be accessible to other users.

Hosted tokens require HTTPS. Loopback HTTP is supported for local development.

## Create sprites

```sh
spritesmith prompt --operation-id hero-v1 --out hero.png 'A small forest ranger'
spritesmith prompt --kind vector --format svg --operation-id emblem-v1 --out emblem.svg 'A golden shield emblem'
spritesmith prompt --kind vector --format png --out emblem.png 'A golden shield emblem'
spritesmith image --out ranger.png reference.png
```

Flags come before the prompt or image filename. Vector creation requires a prompt; an image may be supplied as a reference.

Raster sprites export as PNG. Vector sprites export as SVG or PNG. SVG contains editable artwork geometry.

Generation runs as a server job. The client waits for completion and downloads the requested file. Retain `--operation-id` and reuse it with the same input after an interruption. Existing output files are never overwritten. An uncertain job requires reconciliation; the client does not automatically submit another paid request.

The default API is `http://127.0.0.1:8080`; configure your hosted service explicitly. Default output is `spritesmith-alpha.png`, or `spritesmith.svg` for SVG downloads.

## Development

```sh
make install
make check
```

Checks include Go race tests, HTTP client integration tests, five platform archives, checksums, source identity verification, and vulnerability scanning. The client builds independently of private repositories.
