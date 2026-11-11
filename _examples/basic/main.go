// Command basic is a small HTTP API with logging and an in-memory CRUD example.
//
// Run from the repository root with:
//
// \tgo run ./_examples/basic -config _examples/basic/config.yaml
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/ams-soft/nforge/app"
	"github.com/ams-soft/nforge/codec"
	"github.com/ams-soft/nforge/config"
	nhttp "github.com/ams-soft/nforge/http"
	"github.com/ams-soft/nforge/logs"
	"github.com/ams-soft/nforge/module"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	a, err := app.New(cfg, app.Modules(logs.Module(), codec.Module(), nhttp.Module(), apiModule()))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := a.Run(context.Background()); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func apiModule() module.Descriptor {
	return module.New("api", module.Definition{
		Requires: []module.Handle{nhttp.Capability},
		Init: func(_ context.Context, scope *module.Scope) (module.Hooks, error) {
			server := module.Require(scope, nhttp.Capability)
			store := newUserStore()
			api := server.Typed()
			nhttp.Get(api, "/users", listUsers(store))
			nhttp.Post(api, "/users", createUser(store), nhttp.Opts{Status: 201})
			nhttp.Get(api, "/users/{id}", getUser(store))
			nhttp.Put(api, "/users/{id}", updateUser(store))
			nhttp.Delete(api, "/users/{id}", deleteUser(store))
			return module.Hooks{}, nil
		},
	})
}
