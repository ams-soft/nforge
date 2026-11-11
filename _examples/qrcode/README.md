# qrcode

Demonstrates `github.com/ams-soft/nforge/helper/qrcode`: QR Codes as PNG, SVG,
Base64 and Data URI, with rounded modules and finders and a centered logo, plus
a QR Code printed straight in the terminal. No database, cache, Docker or
network. The helper works in memory; reading `-logo` and writing the gallery
happen only in this program.

```bash
go run ./_examples/qrcode                          # payload teste123: QR in the terminal + gallery
go run ./_examples/qrcode -payload 'https://example.com/test' -size 512 -out ./qrcode-output
go run ./_examples/qrcode -logo ./_examples/qrcode/assets/logo.svg
go run ./_examples/qrcode -terminal-only           # only the QR (stdout), no files
go run ./_examples/qrcode -terminal-only -payload "https://example.com"
go run ./_examples/qrcode -terminal=false          # only the gallery
```

| Flag | Default | Meaning |
|---|---|---|
| `-payload` | `teste123` | Text to encode. Generic: nothing Pix- or BR Code-specific. An explicit empty value is an error, not the default. |
| `-out` | `./qrcode-output` | Gallery directory, relative to where you run the command. |
| `-size` | `512` | Image side in pixels; the PNG uses a whole number of pixels per module, so the real side is a multiple of the module count. |
| `-logo` | built-in demo | PNG, JPEG or SVG used by the logo cases. |
| `-terminal` | `true` | Print the QR Code as text. |
| `-terminal-only` | `false` | Print only the QR Code: no progress messages, no files. Errors go to stderr with a non-zero exit. |
| `-invert` | `false` | Terminal: draw the dark modules instead (for a light terminal theme). |

## The gallery

`qrcode-output/index.html` is a static page (no JavaScript, no server) showing
the PNG and the SVG of every configuration side by side: traditional, rounded
modules, rounded finders, both, with a logo, with a rounded logo, with an SVG
logo and custom colors. Next to it: `NN-name.png`, `NN-name.svg`,
`base64.txt` and `datauri.txt`. Open `index.html` and scan the codes with a
phone; every style is expected to scan, and none is guaranteed to on every
device.

## Scanning the terminal QR Code

The text version uses the half-block characters `▀ ▄ █`, one module per column
and half a line tall, with a 4-module quiet zone on every side, traditional
style and no logo. By default the **glyphs are the light modules**: terminals
paint them in the foreground color, usually light on a dark background, so a
dark-theme terminal shows dark modules on a light field. On a light theme use
`-invert`. Point the phone at the terminal:

- legibility depends on the terminal: font (some fonts leave gaps between
  block characters), line spacing, zoom and contrast. Increase the font size,
  or the window, if the camera does not lock on;
- a very long payload makes a big symbol that may not fit your window;
- no ANSI colors are used; it works over any pipe.

The matrix comes from the same encoder as the PNG and SVG
(`QRCode.Modules()`), not from another implementation.

## Tests

```bash
go test -race ./_examples/qrcode/...
```

They check the terminal rendering (dimensions, quiet zone, inversion, no ANSI),
decode it back with an independent decoder, check that `-terminal-only` writes
no files, that an explicit empty payload fails, and decode every PNG of the
gallery.
