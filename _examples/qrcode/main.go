// Command qrcode shows the helper/qrcode package: a QR Code printed in the
// terminal and a local gallery (PNG, SVG, Base64, Data URI and an index.html)
// of its styles, with and without a logo.
//
//	go run ./_examples/qrcode                                  # payload teste123
//	go run ./_examples/qrcode -payload 'https://example.com/test' -size 512 -out ./qrcode-output
//	go run ./_examples/qrcode -logo ./_examples/qrcode/assets/logo.svg
//	go run ./_examples/qrcode -terminal-only                   # only the QR, no files
//	go run ./_examples/qrcode -terminal=false                  # only the gallery
//
// The helper works in memory; reading -logo and writing the gallery happen
// here, in the example.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/ams-soft/nforge/helper/qrcode"
)

const defaultPayload = "teste123"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// run is main without the process: it returns the exit code.
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("qrcode", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		payload      = fs.String("payload", defaultPayload, "text to encode (any payload; default "+defaultPayload+")")
		out          = fs.String("out", "./qrcode-output", "directory for the gallery")
		size         = fs.Int("size", qrcode.DefaultSize, "image side in pixels")
		logo         = fs.String("logo", "", "path of a PNG, JPEG or SVG logo for the logo examples")
		terminal     = fs.Bool("terminal", true, "print the QR Code in the terminal")
		terminalOnly = fs.Bool("terminal-only", false, "print only the QR Code in the terminal: no files, no other output")
		invert       = fs.Bool("invert", false, "terminal: draw the dark modules (for a light terminal theme)")
	)
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(stderr, "qrcode: unexpected argument %q\n", fs.Arg(0))
		return 2
	}
	// An explicit -payload "" is passed to the helper, which rejects it; only
	// an absent flag takes the default.
	if *terminalOnly && !*terminal {
		fmt.Fprintln(stderr, "qrcode: -terminal-only needs -terminal")
		return 2
	}

	if *terminal || *terminalOnly {
		// Traditional style, default level: the most compatible rendering. No
		// logo in the text version.
		q, err := qrcode.New(*payload)
		if err != nil {
			fmt.Fprintf(stderr, "qrcode: encode terminal code: %v\n", err)
			return 1
		}
		fmt.Fprint(stdout, renderTerminal(q.Modules(), *invert))
	}
	if *terminalOnly {
		return 0
	}

	files, err := writeGallery(*out, *payload, *size, *logo)
	if err != nil {
		fmt.Fprintf(stderr, "qrcode: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "\nwrote %d files to %s\n", len(files), *out)
	for _, f := range files {
		fmt.Fprintln(stdout, "  "+f)
	}
	fmt.Fprintln(stdout, "open", *out+"/index.html to compare the PNG and SVG of every style")
	return 0
}
