package nhttp

import (
	"crypto/sha256"
	"encoding/hex"
	"sync"
)

// specCache keeps the rendered OpenAPI documents. The spec endpoints are
// public, and building one walks every route type by reflection, so rebuilding
// it on each request made them a cheap way to burn CPU. Routes are only ever
// appended and registering one after the server started serving panics, so
// the number of routes identifies the content.
type specCache struct {
	mu     sync.Mutex
	routes int
	byType map[string]*renderedSpec
}

type renderedSpec struct {
	data []byte
	etag string
}

// get returns the document for contentType built from routes routes, rendering
// it only when the route count changed since it was cached.
func (c *specCache) get(contentType string, routes int, render func() ([]byte, error)) (*renderedSpec, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.byType == nil || c.routes != routes {
		c.byType, c.routes = map[string]*renderedSpec{}, routes
	}
	if spec, ok := c.byType[contentType]; ok {
		return spec, nil
	}
	data, err := render()
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	spec := &renderedSpec{data: data, etag: `"` + hex.EncodeToString(sum[:16]) + `"`}
	c.byType[contentType] = spec
	return spec, nil
}
