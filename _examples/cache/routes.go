package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	stdhttp "net/http"
	"strconv"
	"sync/atomic"
	"time"

	router "github.com/ams-soft/go-ams-router"
	"github.com/ams-soft/nforge/cache"
	nhttp "github.com/ams-soft/nforge/http"
)

// Item is a cached object. It is encoded with cache.codec (json or cbor)
// through the framework codec registry, with the usual required fields and
// Validate, and stored without an envelope.
type Item struct {
	Name string `json:"name" codec:"required"`
	Note string `json:"note,omitempty"`
}

// Validate runs on encode and decode, so the cache never stores or
// returns an invalid item.
func (i *Item) Validate() error {
	if len(i.Name) > 200 {
		return errors.New("name must have at most 200 bytes")
	}
	return nil
}

// PutItem is the body of PUT /api/v1/items/{key}.
type PutItem struct {
	Item
	TTL string `json:"ttl,omitempty"` // Go duration ("30s"); empty = no expiration
}

// ClaimResult reports whether SetIfAbsent created the entry.
type ClaimResult struct {
	Created bool `json:"created"`
}

// DeleteResult counts the deleted keys.
type DeleteResult struct {
	Deleted int64 `json:"deleted"`
}

// TTLInfo is cache.Expiration in milliseconds.
type TTLInfo struct {
	Exists      bool  `json:"exists"`
	Persistent  bool  `json:"persistent"`
	RemainingMS int64 `json:"remaining_ms"`
}

// ExpireRequest is the body of POST /api/v1/items/{key}/expire.
type ExpireRequest struct {
	TTL string `json:"ttl" codec:"required"` // > 0
}

// ExpireResult reports whether the key existed.
type ExpireResult struct {
	Updated bool `json:"updated"`
}

// CounterRequest is the body of POST /api/v1/counters/{key}.
type CounterRequest struct {
	Delta int64 `json:"delta"` // any sign; 0 reads the counter
}

// CounterResult is the counter after the increment.
type CounterResult struct {
	Value int64 `json:"value"`
}

// Product is what the slow catalog returns and GetOrLoad caches.
type Product struct {
	ID    int64  `json:"id"`
	Name  string `json:"name" codec:"required"`
	Price int64  `json:"price"` // cents
}

// productTTL is how long a loaded product stays cached.
const productTTL = time.Minute

// maxRawBody bounds PUT /raw/{key}; cache.limits.max_value_bytes applies too.
const maxRawBody = 1 << 20

var errProductNotFound = errors.New("product not found")

// catalog is a deliberately slow data source (think of a database or a
// remote API), so concurrent misses visibly share one load.
type catalog struct {
	delay    time.Duration
	loads    atomic.Int64 // how many times the source was really queried
	products map[int64]Product
}

func newCatalog() *catalog {
	return &catalog{delay: 100 * time.Millisecond, products: map[int64]Product{
		1: {ID: 1, Name: "Keyboard", Price: 4990},
		2: {ID: 2, Name: "Mouse", Price: 1990},
	}}
}

func (c *catalog) find(ctx context.Context, id int64) (Product, error) {
	c.loads.Add(1)
	t := time.NewTimer(c.delay)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return Product{}, ctx.Err()
	case <-t.C:
	}
	p, ok := c.products[id]
	if !ok {
		return Product{}, errProductNotFound
	}
	return p, nil
}

// registerRoutes mounts the cache routes. Handlers only use
// server.Deps.Cache; every error is mapped by sendCacheError.
func registerRoutes(server *nhttp.ServerHttp, products *catalog) {
	c := server.Deps.Cache
	logger := &server.Deps.Logger
	fail := func(w stdhttp.ResponseWriter, req *stdhttp.Request, err error) { sendCacheError(w, req, logger, err) }
	tag := func(description string) nhttp.Opts { return nhttp.Opts{DocTag: "cache", Description: description} }

	server.Typed().Route("/api/v1", func(api *nhttp.TypedRouter) {
		// curl -X PUT localhost:8080/api/v1/items/greeting -H 'Content-Type: application/json' -d '{"name":"hello","ttl":"30s"}'
		api.TPut("/items/{key}", PutItem{}, nil, func(w stdhttp.ResponseWriter, req *stdhttp.Request) {
			in := nhttp.Body[PutItem](req)
			ttl, ok := parseTTL(w, req, in.TTL, true)
			if !ok {
				return
			}
			if err := c.Set(req.Context(), itemKey(req), in.Item, ttl); err != nil {
				fail(w, req, err)
				return
			}
			w.WriteHeader(stdhttp.StatusNoContent)
		}, tag("Stores an object (cache.Set); ttl empty = persistent"))

		// curl localhost:8080/api/v1/items/greeting
		api.TGet("/items/{key}", nil, Item{}, func(w stdhttp.ResponseWriter, req *stdhttp.Request) {
			var it Item
			if err := c.Get(req.Context(), itemKey(req), &it); err != nil {
				fail(w, req, err) // a miss is cache.ErrNotFound → 404
				return
			}
			_ = nhttp.SendJSON(w, req, stdhttp.StatusOK, it)
		}, tag("Reads an object (cache.Get); 404 on a miss"))

		// curl -X POST localhost:8080/api/v1/items/lock/claim -H 'Content-Type: application/json' -d '{"name":"worker-1","ttl":"10s"}'
		api.TPost("/items/{key}/claim", PutItem{}, ClaimResult{}, func(w stdhttp.ResponseWriter, req *stdhttp.Request) {
			in := nhttp.Body[PutItem](req)
			ttl, ok := parseTTL(w, req, in.TTL, true)
			if !ok {
				return
			}
			created, err := c.SetIfAbsent(req.Context(), itemKey(req), in.Item, ttl)
			if err != nil {
				fail(w, req, err)
				return
			}
			_ = nhttp.SendJSON(w, req, stdhttp.StatusOK, ClaimResult{Created: created})
		}, tag("Stores an object only if absent (cache.SetIfAbsent, atomic NX)"))

		// curl localhost:8080/api/v1/items/greeting/ttl
		api.TGet("/items/{key}/ttl", nil, TTLInfo{}, func(w stdhttp.ResponseWriter, req *stdhttp.Request) {
			exp, err := c.TTL(req.Context(), itemKey(req))
			if err != nil {
				fail(w, req, err)
				return
			}
			_ = nhttp.SendJSON(w, req, stdhttp.StatusOK, TTLInfo{
				Exists: exp.Exists, Persistent: exp.Persistent, RemainingMS: exp.Remaining.Milliseconds(),
			})
		}, tag("Reports existence and remaining time (cache.TTL)"))

		// curl -X POST localhost:8080/api/v1/items/greeting/expire -H 'Content-Type: application/json' -d '{"ttl":"5s"}'
		api.TPost("/items/{key}/expire", ExpireRequest{}, ExpireResult{}, func(w stdhttp.ResponseWriter, req *stdhttp.Request) {
			ttl, ok := parseTTL(w, req, nhttp.Body[ExpireRequest](req).TTL, false)
			if !ok {
				return
			}
			updated, err := c.Expire(req.Context(), itemKey(req), ttl)
			if err != nil {
				fail(w, req, err)
				return
			}
			_ = nhttp.SendJSON(w, req, stdhttp.StatusOK, ExpireResult{Updated: updated})
		}, tag("Sets a new TTL on an existing key (cache.Expire)"))

		// curl -X DELETE localhost:8080/api/v1/items/greeting
		api.TDelete("/items/{key}", nil, DeleteResult{}, func(w stdhttp.ResponseWriter, req *stdhttp.Request) {
			n, err := c.Delete(req.Context(), itemKey(req))
			if err != nil {
				fail(w, req, err)
				return
			}
			_ = nhttp.SendJSON(w, req, stdhttp.StatusOK, DeleteResult{Deleted: n})
		}, tag("Deletes a key (cache.Delete)"))

		// curl -X POST localhost:8080/api/v1/counters/visits -H 'Content-Type: application/json' -d '{"delta":1}'
		api.TPost("/counters/{key}", CounterRequest{}, CounterResult{}, func(w stdhttp.ResponseWriter, req *stdhttp.Request) {
			n, err := c.Increment(req.Context(), "counters:"+router.URLParam(req, "key"), nhttp.Body[CounterRequest](req).Delta)
			if err != nil {
				fail(w, req, err)
				return
			}
			_ = nhttp.SendJSON(w, req, stdhttp.StatusOK, CounterResult{Value: n})
		}, tag("Atomically adds delta to a decimal counter (cache.Increment)"))

		// Cache-aside: the first request loads from the catalog and stores the
		// product for a minute; concurrent misses of the same key share that one
		// load. If the cache cannot be READ the loader is not called and the
		// request fails (503): an outage must not turn into a stampede on the
		// source. If only the WRITE of the loaded value fails, the product is
		// still returned and the failure is logged as a warning.
		// curl localhost:8080/api/v1/products/1
		api.TGet("/products/{id}", nil, Product{}, func(w stdhttp.ResponseWriter, req *stdhttp.Request) {
			id, err := strconv.ParseInt(router.URLParam(req, "id"), 10, 64)
			if err != nil || id < 1 {
				_ = nhttp.SendJSON(w, req, stdhttp.StatusBadRequest, "invalid id")
				return
			}
			var p Product
			err = c.GetOrLoad(req.Context(), "products:"+strconv.FormatInt(id, 10), &p, productTTL, func(ctx context.Context) (any, error) {
				return products.find(ctx, id)
			})
			switch {
			case errors.Is(err, errProductNotFound):
				_ = nhttp.SendJSON(w, req, stdhttp.StatusNotFound, "product not found") // loader errors are not cached
			case err != nil:
				fail(w, req, err)
			default:
				_ = nhttp.SendJSON(w, req, stdhttp.StatusOK, p)
			}
		}, tag("Reads a product through the cache (cache.GetOrLoad)"))
	})

	registerRaw(server, c, logger)
}

// registerRaw mounts the byte routes (plain routes: raw bytes have no
// envelope and no codec).
func registerRaw(server *nhttp.ServerHttp, c *cache.Cache, logger *slog.Logger) {
	r := server.Router()

	// curl -X PUT 'localhost:8080/raw/blob?ttl=1m' --data-binary 'any bytes'
	r.Put("/raw/{key}", func(w stdhttp.ResponseWriter, req *stdhttp.Request) {
		ttl, ok := parseTTL(w, req, req.URL.Query().Get("ttl"), true)
		if !ok {
			return
		}
		body, err := io.ReadAll(stdhttp.MaxBytesReader(w, req.Body, maxRawBody))
		if err != nil {
			_ = nhttp.SendJSON(w, req, stdhttp.StatusRequestEntityTooLarge, "body too large")
			return
		}
		if err := c.SetBytes(req.Context(), router.URLParam(req, "key"), body, ttl); err != nil {
			sendCacheError(w, req, logger, err)
			return
		}
		w.WriteHeader(stdhttp.StatusNoContent)
	})

	// Any key, including object keys: `GET /raw/items:greeting` shows the
	// stored JSON (or CBOR bytes) exactly as written.
	// curl localhost:8080/raw/blob
	r.Get("/raw/{key}", func(w stdhttp.ResponseWriter, req *stdhttp.Request) {
		data, err := c.GetBytes(req.Context(), router.URLParam(req, "key"))
		if err != nil {
			sendCacheError(w, req, logger, err)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(data)
	})

	server.AddDocRoute(nhttp.TypedRoute{Method: "PUT", Path: "/raw/{key}", Tag: "cache",
		Description: "Stores raw bytes (cache.SetBytes); ?ttl=1m"})
	server.AddDocRoute(nhttp.TypedRoute{Method: "GET", Path: "/raw/{key}", Tag: "cache",
		Description: "Reads raw bytes (cache.GetBytes) as application/octet-stream"})
}

func itemKey(req *stdhttp.Request) string { return "items:" + router.URLParam(req, "key") }

// parseTTL reads a Go duration; empty means 0 (persistent) when allowed.
func parseTTL(w stdhttp.ResponseWriter, req *stdhttp.Request, s string, allowZero bool) (time.Duration, bool) {
	if s == "" && allowZero {
		return 0, true
	}
	d, err := time.ParseDuration(s)
	if err != nil || d < 0 || (d == 0 && !allowZero) {
		_ = nhttp.SendJSON(w, req, stdhttp.StatusBadRequest, "ttl must be a positive duration such as 30s")
		return 0, false
	}
	return d, true
}

// sendCacheError maps cache errors to static responses: keys and backend
// details never reach the client.
func sendCacheError(w stdhttp.ResponseWriter, req *stdhttp.Request, logger *slog.Logger, err error) {
	status, detail := stdhttp.StatusInternalServerError, "internal error"
	switch {
	case errors.Is(err, cache.ErrNotFound):
		status, detail = stdhttp.StatusNotFound, "not found"
	case errors.Is(err, cache.ErrInvalidKey), errors.Is(err, cache.ErrInvalidTTL):
		status, detail = stdhttp.StatusBadRequest, "invalid key or ttl"
	case errors.Is(err, cache.ErrValueTooLarge):
		status, detail = stdhttp.StatusRequestEntityTooLarge, "value too large"
	case errors.Is(err, cache.ErrNotInteger), errors.Is(err, cache.ErrCounterOverflow):
		status, detail = stdhttp.StatusConflict, "the key does not hold a counter in range"
	case errors.Is(err, cache.ErrOutcomeUnknown):
		// the write may or may not have been applied; it was not retried
		status, detail = stdhttp.StatusServiceUnavailable, "cache unavailable (outcome unknown)"
	case errors.Is(err, cache.ErrUnavailable), errors.Is(err, cache.ErrLoadCapacity):
		status, detail = stdhttp.StatusServiceUnavailable, "cache unavailable"
	case errors.Is(err, cache.ErrDecode):
		status, detail = stdhttp.StatusConflict, "the key holds data of another type"
	}
	if status >= 500 {
		logger.WarnContext(req.Context(), "cache error", "error", err) // sanitized by the cache package
	}
	_ = nhttp.SendJSON(w, req, status, detail)
}
