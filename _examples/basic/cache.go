package main

import (
	"errors"
	stdhttp "net/http"
	"time"

	router "github.com/ams-soft/go-ams-router"
	"github.com/ams-soft/nforge/cache"
	nhttp "github.com/ams-soft/nforge/http"
)

// CacheEntry is the object stored under /cache/{key}. It is encoded with
// cache.codec through the framework codec registry.
type CacheEntry struct {
	Value string `json:"value" codec:"required"`
}

// PutCacheEntry is the body of PUT /cache/{key}.
type PutCacheEntry struct {
	CacheEntry
	TTL string `json:"ttl,omitempty"` // Go duration ("30s"); empty = no expiration
}

// registerCacheRoutes registers Set/Get/Delete over deps.Cache (the memory
// driver in config.yaml, so no Redis is needed). See the cache example for
// counters, NX, TTL inspection, raw bytes, GetOrLoad and Redis.
func registerCacheRoutes(server *nhttp.ServerHttp) {
	c := server.Deps.Cache
	logger := &server.Deps.Logger
	fail := func(w stdhttp.ResponseWriter, r *stdhttp.Request, err error) {
		status, detail := stdhttp.StatusInternalServerError, "internal error"
		switch {
		case errors.Is(err, cache.ErrNotFound):
			status, detail = stdhttp.StatusNotFound, "not found"
		case errors.Is(err, cache.ErrInvalidKey):
			status, detail = stdhttp.StatusBadRequest, "invalid key"
		case errors.Is(err, cache.ErrValueTooLarge):
			status, detail = stdhttp.StatusRequestEntityTooLarge, "value too large"
		default:
			logger.ErrorContext(r.Context(), "cache error", "error", err) // sanitized: no key or value
		}
		_ = nhttp.SendJSON(w, r, status, detail)
	}
	key := func(r *stdhttp.Request) string { return "entries:" + router.URLParam(r, "key") }

	server.Typed().Route("/cache", func(api *nhttp.TypedRouter) {
		// curl -X PUT localhost:8080/cache/greeting -H 'Content-Type: application/json' -d '{"value":"hello","ttl":"30s"}'
		api.TPut("/{key}", PutCacheEntry{}, nil, func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
			in := nhttp.Body[PutCacheEntry](r)
			var ttl time.Duration
			if in.TTL != "" {
				d, err := time.ParseDuration(in.TTL)
				if err != nil || d <= 0 {
					_ = nhttp.SendJSON(w, r, stdhttp.StatusBadRequest, "ttl must be a positive duration such as 30s")
					return
				}
				ttl = d
			}
			if err := c.Set(r.Context(), key(r), in.CacheEntry, ttl); err != nil {
				fail(w, r, err)
				return
			}
			w.WriteHeader(stdhttp.StatusNoContent)
		}, nhttp.Opts{DocTag: "cache", Description: "Stores a value (cache.Set); ttl empty = persistent"})

		// curl localhost:8080/cache/greeting
		api.TGet("/{key}", nil, CacheEntry{}, func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
			var e CacheEntry
			if err := c.Get(r.Context(), key(r), &e); err != nil {
				fail(w, r, err) // a miss is cache.ErrNotFound → 404
				return
			}
			_ = nhttp.SendJSON(w, r, stdhttp.StatusOK, e)
		}, nhttp.Opts{DocTag: "cache", Description: "Reads a value (cache.Get); 404 on a miss"})

		// curl -X DELETE localhost:8080/cache/greeting
		api.TDelete("/{key}", nil, nil, func(w stdhttp.ResponseWriter, r *stdhttp.Request) {
			if _, err := c.Delete(r.Context(), key(r)); err != nil {
				fail(w, r, err)
				return
			}
			w.WriteHeader(stdhttp.StatusNoContent)
		}, nhttp.Opts{DocTag: "cache", Description: "Deletes a value (cache.Delete)"})
	})
}
