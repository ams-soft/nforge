// Command static serves an API, a built single-page application and a
// directory of public files from one nForge HTTP server.
//
// Run from the repository root with:
//
//	go run ./_examples/static -config _examples/static/config.yaml
//	go run ./_examples/static -config _examples/static/config.yaml -embed   # files compiled into the binary
//
// then open http://127.0.0.1:8080/.
package main

import (
	"context"
	"embed"
	"flag"
	"fmt"
	"io/fs"
	"net/http"
	"os"

	"github.com/ams-soft/nforge/app"
	"github.com/ams-soft/nforge/codec"
	"github.com/ams-soft/nforge/config"
	nhttp "github.com/ams-soft/nforge/http"
	"github.com/ams-soft/nforge/logs"
	"github.com/ams-soft/nforge/module"
)

// bundle is the frontend build and the public files, for the -embed mode.
//
//go:embed frontend/dist public
var bundle embed.FS

// sources says where the files come from.
type sources struct {
	dist, public string // directories (relative to the working directory); ignored when embed is set
	embed        bool   // serve the files compiled into the binary instead
}

func main() {
	src := sources{}
	flag.StringVar(&src.dist, "dist", "_examples/static/frontend/dist", "directory of the built frontend")
	flag.StringVar(&src.public, "public", "_examples/static/public", "directory of the public files served under /assets")
	flag.BoolVar(&src.embed, "embed", false, "serve the embedded files instead of the directories")
	cfg, err := config.Load() // parses the flags
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	a, err := newApp(cfg, src)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := a.Run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// newApp builds the application; the tests reuse it.
func newApp(cfg *config.Document, src sources, opts ...app.Option) (*app.Application, error) {
	mods := app.Modules(logs.Module(), codec.Module(), nhttp.Module(), frontendModule(src))
	return app.New(cfg, append([]app.Option{mods}, opts...)...)
}

// healthResponse is what GET /api/health returns in the envelope's data.
type healthResponse struct {
	Status string `json:"status"`
	Source string `json:"source"`
}

func frontendModule(src sources) module.Descriptor {
	return module.New("frontend", module.Definition{
		Requires: []module.Handle{nhttp.Capability},
		Init: func(_ context.Context, scope *module.Scope) (module.Hooks, error) {
			server := module.Require(scope, nhttp.Capability)

			// The API: an explicit route. Routes win over the SPA in any order.
			source := "directory"
			if src.embed {
				source = "embed"
			}
			nhttp.Get(server.Typed(), "/api/health", func(*http.Request, nhttp.Empty) (healthResponse, error) {
				return healthResponse{Status: "ok", Source: source}, nil
			})

			// Public files under /assets, then the application at /. An error here
			// (missing directory or index.html, overlapping mounts) fails the start
			// before any listener opens.
			spa := []nhttp.SPAOption{
				nhttp.WithSPAReserved("/api"),      // an unknown /api/... is a 404, never the app
				nhttp.WithSPAImmutableDir("build"), // content-hashed bundle files: cache for a year
			}
			var err error
			if src.embed {
				err = serveEmbedded(server, spa)
			} else {
				err = serveDirectories(server, src, spa)
			}
			return module.Hooks{}, err
		},
	})
}

func serveDirectories(server *nhttp.Server, src sources, spa []nhttp.SPAOption) error {
	if err := server.Static("/assets", src.public); err != nil {
		return err
	}
	return server.SPA("/", src.dist, spa...)
}

func serveEmbedded(server *nhttp.Server, spa []nhttp.SPAOption) error {
	public, err := fs.Sub(bundle, "public")
	if err != nil {
		return err
	}
	dist, err := fs.Sub(bundle, "frontend/dist")
	if err != nil {
		return err
	}
	if err := server.StaticFS("/assets", public); err != nil {
		return err
	}
	return server.SPAFS("/", dist, spa...)
}
