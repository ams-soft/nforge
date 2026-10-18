package main

import (
	"log/slog"
	stdhttp "net/http"

	nhttp "github.com/ams-soft/nforge/http"
)

// registerPlainRoutes shows plain (untyped) routes: the handler parses and
// answers itself, in each codec format. Typed routes (users.go) do the same
// work for you and document themselves.
func registerPlainRoutes(server *nhttp.ServerHttp, logger *slog.Logger) {
	r := server.Router()

	// curl -X POST localhost:8080/echo/json -H 'Content-Type: application/json' -d '{"name":"Ana"}'
	r.Post("/echo/json", func(w stdhttp.ResponseWriter, req *stdhttp.Request) {
		var u User
		if err := nhttp.ParseJSON(req, &u); err != nil {
			logger.WarnContext(req.Context(), "invalid json payload", "error", err)
			_ = nhttp.SendJSON(w, req, nhttp.ParseStatus(err), err.Error()) // 400, 413, 415 or 422
			return
		}
		_ = nhttp.SendJSON(w, req, stdhttp.StatusOK, u)
	})

	// The same User in XML: the xml tags only say where XML differs from JSON
	// (root element name, id as an attribute).
	// curl -X POST localhost:8080/echo/xml -H 'Content-Type: application/xml' -d '<user id="1"><name>Ana</name></user>'
	r.Post("/echo/xml", func(w stdhttp.ResponseWriter, req *stdhttp.Request) {
		var u User
		if err := nhttp.ParseXML(req, &u); err != nil {
			logger.WarnContext(req.Context(), "invalid xml payload", "error", err)
			_ = nhttp.SendXML(w, req, nhttp.ParseStatus(err), err.Error())
			return
		}
		_ = nhttp.SendXML(w, req, stdhttp.StatusOK, u)
	})

	// CBOR is binary; to try it:
	// printf '\xa1\x64name\x63Ana' | curl -X POST localhost:8080/echo/cbor -H 'Content-Type: application/cbor' --data-binary @- | od -c
	r.Post("/echo/cbor", func(w stdhttp.ResponseWriter, req *stdhttp.Request) {
		var u User
		if err := nhttp.ParseCBOR(req, &u); err != nil {
			logger.WarnContext(req.Context(), "invalid cbor payload", "error", err)
			_ = nhttp.SendCBOR(w, req, nhttp.ParseStatus(err), err.Error())
			return
		}
		_ = nhttp.SendCBOR(w, req, stdhttp.StatusOK, u)
	})

	// Plain routes are not documented automatically; AddDocRoute adds one to
	// /docs (the handler is not involved).
	server.AddDocRoute(nhttp.TypedRoute{Method: "POST", Path: "/echo/json", Tag: "plain",
		Description: "Echoes a user (plain route)", Schema: User{}, Response: User{}})
}
