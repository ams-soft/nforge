// Package jwks serves a public JSON Web Key Set as a raw protocol endpoint:
// application/jwk-set+json, no envelope, strong ETag and conditional GET.
package jwks

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	stdhttp "net/http"
	"strconv"
	"strings"
	"time"
)

// Handler answers GET and HEAD with the bytes returned by load. When load
// fails, onError is called and the response is a non-cacheable 503.
func Handler(load func(context.Context) ([]byte, error), maxAge time.Duration, onError func(context.Context, error)) stdhttp.HandlerFunc {
	cacheControl := "public, max-age=" + strconv.FormatInt(int64(maxAge/time.Second), 10)
	return func(w stdhttp.ResponseWriter, req *stdhttp.Request) {
		data, err := load(req.Context())
		if err != nil {
			onError(req.Context(), err)
			w.Header().Set("Cache-Control", "no-store")
			stdhttp.Error(w, "service unavailable", stdhttp.StatusServiceUnavailable)
			return
		}
		sum := sha256.Sum256(data)
		etag := `"` + hex.EncodeToString(sum[:16]) + `"`

		h := w.Header()
		h.Set("ETag", etag)
		h.Set("Cache-Control", cacheControl)
		h.Set("X-Content-Type-Options", "nosniff")
		if etagMatches(req.Header.Get("If-None-Match"), etag) {
			w.WriteHeader(stdhttp.StatusNotModified)
			return
		}
		h.Set("Content-Type", "application/jwk-set+json")
		h.Set("Content-Length", strconv.Itoa(len(data)))
		w.WriteHeader(stdhttp.StatusOK)
		if req.Method != stdhttp.MethodHead {
			_, _ = w.Write(data)
		}
	}
}

// etagMatches implements the weak comparison of If-None-Match (RFC 9110).
func etagMatches(header, etag string) bool {
	if header == "" {
		return false
	}
	for candidate := range strings.SplitSeq(header, ",") {
		candidate = strings.TrimSpace(candidate)
		if candidate == "*" || strings.TrimPrefix(candidate, "W/") == etag {
			return true
		}
	}
	return false
}
