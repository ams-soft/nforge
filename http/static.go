package nhttp

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"os"
	"path"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"unicode/utf8"

	stdhttp "net/http"

	router "github.com/ams-soft/go-ams-router"
)

const (
	// DefaultSPAIndex is the entry point of a single-page application.
	DefaultSPAIndex = "index.html"

	// DefaultStaticCacheControl is the Cache-Control of static files and of the
	// assets of a single-page application: they are revalidated on every use.
	// Long-lived caching is an explicit decision (WithStaticCacheControl,
	// WithSPAAssetCacheControl, WithSPAImmutableDir).
	DefaultStaticCacheControl = "no-cache"

	// ImmutableCacheControl is the policy for files whose name changes with
	// their content (hashed bundles). Never use it for a file that keeps its name.
	ImmutableCacheControl = "public, max-age=31536000, immutable"

	maxFileNameBytes   = 2048 // longest request path (below the mount) that is looked up
	maxBufferedFile    = 32 << 20
	maxCacheControlLen = 256
)

// StaticOption customizes Server.Static and Server.StaticFS.
type StaticOption func(*fileConfig) error

// SPAOption customizes Server.SPA and Server.SPAFS.
type SPAOption func(*fileConfig) error

// fileConfig is the shared configuration of a file mount. The option types are
// distinct so that an option of one mount kind cannot be passed to the other.
type fileConfig struct {
	index        string
	staticCache  string // Cache-Control of static files and SPA assets
	htmlCache    string // Cache-Control of the SPA's HTML documents
	immutableDir string // SPA directory (relative to the root) of hashed, immutable files
	reserved     []string
}

// WithStaticCacheControl sets the Cache-Control of the files of a static mount
// (default "no-cache"). The value is validated: printable ASCII, no line breaks.
func WithStaticCacheControl(value string) StaticOption {
	return func(c *fileConfig) error {
		if err := validCacheControl(value); err != nil {
			return fmt.Errorf("WithStaticCacheControl: %w", err)
		}
		c.staticCache = value
		return nil
	}
}

// WithSPAIndex sets the entry point of a single-page application (default
// "index.html"): a file name inside the root that exists and is readable when
// the application is registered.
func WithSPAIndex(name string) SPAOption {
	return func(c *fileConfig) error {
		if _, _, ok := cleanName("/" + name); !ok || name == "" || strings.HasSuffix(name, "/") || deniedName(name) {
			return fmt.Errorf("WithSPAIndex: %q is not a valid file name", name)
		}
		c.index = name
		return nil
	}
}

// WithSPAHTMLCacheControl sets the Cache-Control of the HTML documents of a
// single-page application, the entry point included (default "no-cache"). The
// entry point must stay revalidatable, so "immutable" is refused.
func WithSPAHTMLCacheControl(value string) SPAOption {
	return func(c *fileConfig) error {
		if err := validCacheControl(value); err != nil {
			return fmt.Errorf("WithSPAHTMLCacheControl: %w", err)
		}
		if strings.Contains(strings.ToLower(value), "immutable") {
			return errors.New("WithSPAHTMLCacheControl: the HTML entry point cannot be immutable")
		}
		c.htmlCache = value
		return nil
	}
}

// WithSPAAssetCacheControl sets the Cache-Control of the other files of a
// single-page application (default "no-cache"). A long lifetime is only safe for
// names that change with their content: prefer WithSPAImmutableDir.
func WithSPAAssetCacheControl(value string) SPAOption {
	return func(c *fileConfig) error {
		if err := validCacheControl(value); err != nil {
			return fmt.Errorf("WithSPAAssetCacheControl: %w", err)
		}
		c.staticCache = value
		return nil
	}
}

// WithSPAImmutableDir marks a directory of the application (relative to its
// root, such as "assets", where bundlers emit content-hashed files) as
// immutable: its files get ImmutableCacheControl. Everything else keeps its
// policy, so unhashed files (service workers, manifests) are never pinned.
func WithSPAImmutableDir(dir string) SPAOption {
	return func(c *fileConfig) error {
		dir = strings.Trim(dir, "/")
		if _, _, ok := cleanName("/" + dir); !ok || dir == "" || deniedName(dir) {
			return fmt.Errorf("WithSPAImmutableDir: %q is not a valid directory", dir)
		}
		c.immutableDir = dir
		return nil
	}
}

// WithSPAReserved reserves URL prefixes (such as "/api") that the application
// fallback never answers: an unknown path under them is a 404, not the entry
// point. The built-in routes (docs, health, readiness, graph, JWKS) are
// reserved automatically. A prefix is an absolute path with no trailing slash
// and no wildcard.
func WithSPAReserved(prefixes ...string) SPAOption {
	return func(c *fileConfig) error {
		for _, p := range prefixes {
			if err := validPrefix(p); err != nil || p == "/" {
				return fmt.Errorf("WithSPAReserved: %q is not a valid prefix", p)
			}
			c.reserved = append(c.reserved, p)
		}
		return nil
	}
}

func validCacheControl(v string) error {
	if v == "" || len(v) > maxCacheControlLen {
		return fmt.Errorf("Cache-Control must be 1..%d characters", maxCacheControlLen)
	}
	for i := 0; i < len(v); i++ {
		if v[i] < 0x20 || v[i] > 0x7e {
			return errors.New("Cache-Control must be printable ASCII")
		}
	}
	return nil
}

// validPrefix accepts "/" or an absolute, clean path with no trailing slash,
// no dot segments and none of the characters the router or a URL treats
// specially.
func validPrefix(p string) error {
	if p == "/" {
		return nil
	}
	if p == "" || p[0] != '/' || strings.HasSuffix(p, "/") || path.Clean(p) != p {
		return errors.New("must be an absolute clean path without a trailing slash")
	}
	if strings.ContainsAny(p, "{}*?#%\\ ") || !utf8.ValidString(p) {
		return errors.New("contains a character that is not allowed")
	}
	for i := 0; i < len(p); i++ {
		if p[i] < 0x20 || p[i] == 0x7f {
			return errors.New("contains a control character")
		}
	}
	return nil
}

// Static serves the files of directory under the URL prefix ("/assets" serves
// /assets/logo.svg from directory/logo.svg; "/" serves the whole site and
// answers an unknown path with a 404). It handles GET and HEAD only, with the
// conditional and range semantics of net/http.ServeContent, serves no directory
// listing, no directory index, no dotfile, no private key and no symbolic link,
// and cannot reach outside directory.
//
// Call it while the application is initialized (module Init), before the first
// request; an invalid prefix, a missing directory or a conflict with another
// mount or route is an error, so the start fails before any listener opens. The
// directory is opened once: it must not be writable by untrusted users, because
// a path is checked and then opened (the root handle contains it, the check of
// symbolic links is not atomic).
func (s *Server) Static(prefix, directory string, opts ...StaticOption) error {
	cfg := fileConfig{staticCache: DefaultStaticCacheControl}
	for _, o := range opts {
		if o == nil {
			return errors.New("nhttp: Static: nil option")
		}
		if err := o(&cfg); err != nil {
			return fmt.Errorf("nhttp: Static %s: %w", prefix, err)
		}
	}
	fsys, root, err := openDirFS(directory)
	if err != nil {
		return fmt.Errorf("nhttp: Static %s: %w", prefix, err)
	}
	if err := s.mountFiles(prefix, fsys, root, cfg, false); err != nil {
		_ = root.Close()
		return fmt.Errorf("nhttp: Static %s: %w", prefix, err)
	}
	return nil
}

// StaticFS is Static over an fs.FS (embed.FS through fs.Sub, for example). The
// FS is trusted as given: nForge validates the request path and refuses
// dotfiles and non-regular files, but symbolic links of an FS you pass in are
// your responsibility (embed.FS has none; os.DirFS is not a sandbox: use Static).
func (s *Server) StaticFS(prefix string, filesystem fs.FS, opts ...StaticOption) error {
	if filesystem == nil {
		return errors.New("nhttp: StaticFS: nil filesystem")
	}
	cfg := fileConfig{staticCache: DefaultStaticCacheControl}
	for _, o := range opts {
		if o == nil {
			return errors.New("nhttp: StaticFS: nil option")
		}
		if err := o(&cfg); err != nil {
			return fmt.Errorf("nhttp: StaticFS %s: %w", prefix, err)
		}
	}
	if err := s.mountFiles(prefix, filesystem, nil, cfg, false); err != nil {
		return fmt.Errorf("nhttp: StaticFS %s: %w", prefix, err)
	}
	return nil
}

// SPA serves a built single-page application from directory under the URL
// prefix, normally "/". A file that exists is served as Static serves it. Any
// other GET or HEAD request gets the entry point (index.html) when it is a
// navigation: the client accepts text/html, the path has no file extension, no
// dotfile segment and is not under a reserved prefix (WithSPAReserved, plus the
// built-in routes). A missing script, stylesheet or image, an API path and any
// other method are never answered with HTML: they are 404 (or the router's
// 405 for a path another method serves).
//
// The root application ("/") is the router's last resort: it runs only when no
// registered route matches, so it can never shadow an API, docs, health or any
// other route; register routes in any order. Under a prefix ("/app") the
// application owns the whole prefix and registering a route under it is a
// conflict. The entry point must exist and be readable when SPA is called.
// Applications reach their API through reserved prefixes: reserve every
// prefix the application serves (an unmatched GET /api/x with Accept: text/html
// would otherwise get the entry point).
//
// Frontend visibility is not authorization: protect data with the API's
// authentication, not by hiding the HTML.
func (s *Server) SPA(prefix, directory string, opts ...SPAOption) error {
	cfg, err := spaConfig(opts)
	if err != nil {
		return fmt.Errorf("nhttp: SPA %s: %w", prefix, err)
	}
	fsys, root, err := openDirFS(directory)
	if err != nil {
		return fmt.Errorf("nhttp: SPA %s: %w", prefix, err)
	}
	if err := s.mountFiles(prefix, fsys, root, cfg, true); err != nil {
		_ = root.Close()
		return fmt.Errorf("nhttp: SPA %s: %w", prefix, err)
	}
	return nil
}

// SPAFS is SPA over an fs.FS, such as an embedded build:
//
//	sub, _ := fs.Sub(bundle, "frontend/dist")
//	err := server.SPAFS("/", sub)
func (s *Server) SPAFS(prefix string, filesystem fs.FS, opts ...SPAOption) error {
	if filesystem == nil {
		return errors.New("nhttp: SPAFS: nil filesystem")
	}
	cfg, err := spaConfig(opts)
	if err != nil {
		return fmt.Errorf("nhttp: SPAFS %s: %w", prefix, err)
	}
	if err := s.mountFiles(prefix, filesystem, nil, cfg, true); err != nil {
		return fmt.Errorf("nhttp: SPAFS %s: %w", prefix, err)
	}
	return nil
}

func spaConfig(opts []SPAOption) (fileConfig, error) {
	cfg := fileConfig{index: DefaultSPAIndex, staticCache: DefaultStaticCacheControl, htmlCache: DefaultStaticCacheControl}
	for _, o := range opts {
		if o == nil {
			return cfg, errors.New("nil option")
		}
		if err := o(&cfg); err != nil {
			return cfg, err
		}
	}
	return cfg, nil
}

// fileMounts is the file mounts of one Server: what they own, to detect
// overlaps, and the directory handles to close with the server.
type fileMounts struct {
	mu       sync.Mutex
	prefixes []string // non-root mounts
	rootUsed bool     // "/" is taken (the router's NotFound)
	roots    []*os.Root
}

// mountFiles registers one mount. It is all-or-nothing: on an error nothing was
// added to the router.
func (s *Server) mountFiles(prefix string, fsys fs.FS, root *os.Root, cfg fileConfig, spa bool) error {
	if err := validPrefix(prefix); err != nil {
		return fmt.Errorf("invalid prefix %q: %w", prefix, err)
	}
	if spa {
		if err := checkIndex(fsys, cfg.index); err != nil {
			return err
		}
	}
	fm := &s.files
	fm.mu.Lock()
	defer fm.mu.Unlock()
	if prefix == "/" {
		if fm.rootUsed {
			return errors.New(`"/" is already served by another static or SPA mount`)
		}
	} else {
		for _, p := range fm.prefixes {
			if prefix == p || strings.HasPrefix(prefix, p+"/") || strings.HasPrefix(p, prefix+"/") {
				return fmt.Errorf("overlaps the mount %s", p)
			}
		}
		if err := s.checkRoutesUnder(prefix); err != nil {
			return err
		}
	}

	h := &fileServer{
		fsys:   fsys,
		prefix: strings.TrimSuffix(prefix, "/"),
		spa:    spa,
		cfg:    cfg,
		logger: s.logger,
	}
	if spa {
		h.reserved = append(s.reservedPaths(), cfg.reserved...)
	}

	err := registerSafely(func() {
		rt := s.Router()
		if prefix == "/" {
			rt.NotFound(h.fallback)
			return
		}
		for _, p := range []string{prefix, prefix + "/*"} {
			rt.Get(p, h.serveMount)
			rt.Head(p, h.serveMount)
		}
	})
	if err != nil {
		return err
	}
	if prefix == "/" {
		fm.rootUsed = true
	} else {
		fm.prefixes = append(fm.prefixes, prefix)
	}
	if root != nil {
		fm.roots = append(fm.roots, root)
	}
	return nil
}

func registerSafely(fn func()) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("the router refused the registration (is the server already serving?): %v", r)
		}
	}()
	fn()
	return nil
}

// closeFiles releases the directory handles; Stop calls it.
func (s *Server) closeFiles() {
	s.files.mu.Lock()
	roots := s.files.roots
	s.files.roots = nil
	s.files.mu.Unlock()
	for _, r := range roots {
		_ = r.Close()
	}
}

// reservedPaths are the built-in routes, which a SPA fallback never answers.
func (s *Server) reservedPaths() []string {
	var out []string
	if s.cfg.Docs.Enabled {
		out = append(out, docsBase(s.cfg.Docs))
	}
	if s.cfg.Routes.Health.Enabled {
		out = append(out, s.cfg.Routes.Health.Path)
	}
	if s.cfg.Routes.Readiness.Enabled {
		out = append(out, s.cfg.Routes.Readiness.Path)
	}
	if s.cfg.Routes.Graph.Enabled {
		out = append(out, s.cfg.Routes.Graph.Path)
	}
	if s.jose != nil && s.jose.JWKS().Enabled {
		out = append(out, s.jose.JWKS().Path)
	}
	return out
}

// checkRoutesUnder reports a registered route that overlaps prefix.
func (s *Server) checkRoutesUnder(prefix string) error {
	own := map[string]bool{}
	for _, p := range s.files.prefixes {
		own[p], own[p+"/*"] = true, true
	}
	var found string
	walkRoutes(s.Router(), "", func(pattern string) {
		if found == "" && !own[pattern] && routeOverlaps(prefix, pattern) {
			found = pattern
		}
	})
	if found != "" {
		return fmt.Errorf("conflicts with the route %s", found)
	}
	return nil
}

// checkFileMounts is run by Start, when every module has registered its routes:
// a route added after a mount that overlaps it is a configuration error.
func (s *Server) checkFileMounts() error {
	s.files.mu.Lock()
	defer s.files.mu.Unlock()
	var errs []error
	for _, p := range s.files.prefixes {
		if err := s.checkRoutesUnder(p); err != nil {
			errs = append(errs, fmt.Errorf("nhttp: static mount %s: %w", p, err))
		}
	}
	return errors.Join(errs...)
}

func walkRoutes(rt router.Routes, base string, visit func(pattern string)) {
	for _, r := range rt.Routes() {
		full := base + r.Pattern
		if r.SubRoutes != nil {
			walkRoutes(r.SubRoutes, base+strings.TrimSuffix(r.Pattern, "/*"), visit)
		}
		visit(full)
	}
}

// routeOverlaps reports whether a route pattern can match a path under prefix,
// or (a catch-all) contains the prefix. Routes with only a root catch-all do
// not count: explicit mounts win over them.
func routeOverlaps(prefix, pattern string) bool {
	stem, wild := strings.CutSuffix(pattern, "/*")
	if i := strings.Index(stem, "{"); i >= 0 { // only the literal part before a parameter
		stem = stem[:strings.LastIndex(stem[:i], "/")+1]
		stem = strings.TrimSuffix(stem, "/")
	}
	if stem == prefix || strings.HasPrefix(stem, prefix+"/") {
		return true
	}
	return wild && stem != "" && strings.HasPrefix(prefix, stem+"/")
}

// checkIndex requires the entry point to be a readable regular file.
func checkIndex(fsys fs.FS, name string) error {
	f, err := fsys.Open(name)
	if err != nil {
		return fmt.Errorf("the entry point %s is not readable: %w", name, err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return fmt.Errorf("the entry point %s is not a regular file", name)
	}
	var b [1]byte
	if _, err := f.Read(b[:]); err != nil && !errors.Is(err, io.EOF) {
		return fmt.Errorf("the entry point %s is not readable: %w", name, err)
	}
	return nil
}

// fileServer answers the requests of one mount.
type fileServer struct {
	fsys     fs.FS
	prefix   string // "" for the root mount
	spa      bool
	cfg      fileConfig
	reserved []string
	logger   *slog.Logger
}

// serveMount is the handler of a prefixed mount (GET and HEAD routes).
func (f *fileServer) serveMount(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	rel, ok := strings.CutPrefix(r.URL.Path, f.prefix)
	if !ok || (rel != "" && rel[0] != '/') {
		stdhttp.NotFound(w, r)
		return
	}
	f.serve(w, r, rel)
}

// fallback is the router's NotFound handler of the root mount.
func (f *fileServer) fallback(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	if r.Method != stdhttp.MethodGet && r.Method != stdhttp.MethodHead {
		routeNotFound(w, r)
		return
	}
	if f.reservedPath(r.URL.Path) { // an unknown API route: the envelope, never HTML or text
		routeNotFound(w, r)
		return
	}
	f.serve(w, r, r.URL.Path)
}

func (f *fileServer) serve(w stdhttp.ResponseWriter, r *stdhttp.Request, rel string) {
	name, trailing, ok := cleanName(rel)
	if !ok || (name != "" && deniedName(name)) {
		stdhttp.NotFound(w, r)
		return
	}
	if name == "" { // the mount root
		if f.spa {
			f.serveIndex(w, r)
			return
		}
		stdhttp.NotFound(w, r)
		return
	}
	if !trailing {
		file, info, err := f.open(name)
		switch {
		case err == nil:
			f.serveFile(w, r, file, info, name)
			return
		case !missing(err):
			f.logger.ErrorContext(r.Context(), "[HTTPModule] static file unavailable", "file", name, "err", err)
			stdhttp.Error(w, "internal error", stdhttp.StatusInternalServerError)
			return
		}
	}
	if f.spa && f.navigation(r, name) {
		f.serveIndex(w, r)
		return
	}
	stdhttp.NotFound(w, r)
}

// serveIndex answers with the entry point. Unlike an ordinary file, a missing
// entry point is a server error, not a 404: the deployment is broken.
func (f *fileServer) serveIndex(w stdhttp.ResponseWriter, r *stdhttp.Request) {
	file, info, err := f.open(f.cfg.index)
	if err != nil {
		f.logger.ErrorContext(r.Context(), "[HTTPModule] SPA entry point unavailable", "file", f.cfg.index, "err", err)
		stdhttp.Error(w, "internal error", stdhttp.StatusInternalServerError)
		return
	}
	f.serveFile(w, r, file, info, f.cfg.index)
}

// navigation reports whether a missing path is a client-side route: the client
// accepts HTML, the last segment has no extension and the path is not reserved.
func (f *fileServer) navigation(r *stdhttp.Request, name string) bool {
	if strings.Contains(path.Base(name), ".") {
		return false
	}
	if f.reservedPath(r.URL.Path) {
		return false
	}
	return acceptsHTML(r.Header.Values("Accept"))
}

// reservedPath reports whether p is under a reserved prefix (an API).
func (f *fileServer) reservedPath(p string) bool {
	for _, prefix := range f.reserved {
		if p == prefix || strings.HasPrefix(p, prefix+"/") {
			return true
		}
	}
	return false
}

func (f *fileServer) open(name string) (fs.File, fs.FileInfo, error) {
	file, err := f.fsys.Open(name)
	if err != nil {
		return nil, nil, err
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	if !info.Mode().IsRegular() { // a directory (no listing) or a special file
		_ = file.Close()
		return nil, nil, fs.ErrNotExist
	}
	return file, info, nil
}

// missing reports an error that means "no such public file".
func missing(err error) bool {
	return errors.Is(err, fs.ErrNotExist) || errors.Is(err, fs.ErrPermission) || errors.Is(err, fs.ErrInvalid) ||
		errors.Is(err, syscall.ENOTDIR) || errors.Is(err, syscall.ENAMETOOLONG) || errors.Is(err, syscall.ELOOP)
}

func (f *fileServer) serveFile(w stdhttp.ResponseWriter, r *stdhttp.Request, file fs.File, info fs.FileInfo, name string) {
	defer file.Close()
	h := w.Header()

	ext := strings.ToLower(path.Ext(name))
	ctype := mime.TypeByExtension(ext)
	if ctype == "" {
		ctype = "application/octet-stream" // never sniffed
	}
	h.Set("Content-Type", ctype)
	if h.Get("X-Content-Type-Options") == "" {
		h.Set("X-Content-Type-Options", "nosniff")
	}
	h.Set("Cache-Control", f.cacheControl(name, ext))
	if mt := info.ModTime(); !mt.IsZero() { // embedded files have none: no validators then
		h.Set("ETag", `W/"`+strconv.FormatInt(info.Size(), 16)+"-"+strconv.FormatInt(mt.UnixNano(), 16)+`"`)
	}

	rs, ok := file.(io.ReadSeeker)
	if !ok {
		data, err := io.ReadAll(io.LimitReader(file, maxBufferedFile+1))
		if err != nil || len(data) > maxBufferedFile {
			f.logger.ErrorContext(r.Context(), "[HTTPModule] static file cannot be served", "file", name, "err", err)
			h.Del("Cache-Control")
			h.Del("ETag")
			stdhttp.Error(w, "internal error", stdhttp.StatusInternalServerError)
			return
		}
		rs = bytes.NewReader(data)
	}
	stdhttp.ServeContent(w, r, info.Name(), info.ModTime(), rs)
}

func (f *fileServer) cacheControl(name, ext string) string {
	if !f.spa {
		return f.cfg.staticCache
	}
	if ext == ".html" || ext == ".htm" {
		return f.cfg.htmlCache
	}
	if f.cfg.immutableDir != "" && strings.HasPrefix(name, f.cfg.immutableDir+"/") {
		return ImmutableCacheControl
	}
	return f.cfg.staticCache
}

// cleanName turns the URL path below a mount ("" or "/a/b") into an fs.FS name
// ("" for the root, "a/b"), refusing every ambiguous path: control characters,
// NUL, backslashes, invalid UTF-8, empty, "." and ".." segments (decoded
// traversal included), and the trailing dots or spaces some systems ignore.
// trailing reports a final slash, which never names a file.
func cleanName(rel string) (name string, trailing, ok bool) {
	if len(rel) > maxFileNameBytes || !utf8.ValidString(rel) {
		return "", false, false
	}
	for i := 0; i < len(rel); i++ {
		if c := rel[i]; c < 0x20 || c == 0x7f || c == '\\' {
			return "", false, false
		}
	}
	if rel == "" || rel == "/" {
		return "", false, true
	}
	if rel[0] != '/' {
		return "", false, false
	}
	rel = rel[1:]
	if strings.HasSuffix(rel, "/") {
		trailing = true
		rel = rel[:len(rel)-1]
	}
	for seg := range strings.SplitSeq(rel, "/") {
		if seg == "" || seg == "." || seg == ".." || strings.HasSuffix(seg, ".") || strings.HasSuffix(seg, " ") {
			return "", false, false
		}
	}
	return rel, trailing, true
}

// deniedExt are extensions of files that hold secrets.
var deniedExt = []string{".pem", ".key", ".p12", ".pfx", ".jks", ".env"}

// deniedName reports a name that is never served: any dotfile or dot directory,
// private keys, environment files and application configuration.
func deniedName(name string) bool {
	for seg := range strings.SplitSeq(name, "/") {
		if strings.HasPrefix(seg, ".") {
			return true
		}
	}
	base := strings.ToLower(path.Base(name))
	if slices.Contains(deniedExt, path.Ext(base)) {
		return true
	}
	switch base {
	case "config.yaml", "config.yml", "config.toml", "config.ini", "id_rsa", "id_dsa", "id_ecdsa", "id_ed25519":
		return true
	}
	return false
}

// acceptsHTML reports whether an Accept header lists text/html with a non-zero
// quality. A wildcard does not count: only a browser navigation asks for HTML
// by name, and a script or an API client that accepts anything must not be
// handed the entry point.
func acceptsHTML(values []string) bool {
	for _, v := range values {
		for part := range strings.SplitSeq(v, ",") {
			media, params, _ := strings.Cut(part, ";")
			if !strings.EqualFold(strings.TrimSpace(media), "text/html") {
				continue
			}
			q := 1.0
			for p := range strings.SplitSeq(params, ";") {
				if k, val, ok := strings.Cut(strings.TrimSpace(p), "="); ok && strings.EqualFold(k, "q") {
					q, _ = strconv.ParseFloat(strings.TrimSpace(val), 64)
				}
			}
			if q > 0 {
				return true
			}
		}
	}
	return false
}

// dirFS serves a directory through an os.Root: the kernel refuses a path that
// leaves the root, and Open additionally refuses any symbolic link on the way,
// so a link inside the tree cannot expose a dotfile or another directory.
type dirFS struct{ root *os.Root }

func openDirFS(directory string) (fs.FS, *os.Root, error) {
	if directory == "" {
		return nil, nil, errors.New("empty directory")
	}
	info, err := os.Stat(directory)
	if err != nil {
		return nil, nil, fmt.Errorf("directory %q: %w", directory, err)
	}
	if !info.IsDir() {
		return nil, nil, fmt.Errorf("%q is not a directory", directory)
	}
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, nil, fmt.Errorf("directory %q: %w", directory, err)
	}
	return dirFS{root}, root, nil
}

// Open implements fs.FS.
func (d dirFS) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) || name == "." {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	parts := strings.Split(name, "/")
	for i := range parts {
		sub := strings.Join(parts[:i+1], "/")
		info, err := d.root.Lstat(sub)
		if err != nil {
			return nil, &fs.PathError{Op: "open", Path: name, Err: err}
		}
		if info.Mode()&fs.ModeSymlink != 0 || (i < len(parts)-1 && !info.IsDir()) {
			return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
		}
	}
	f, err := d.root.Open(name)
	if err != nil {
		return nil, err
	}
	return f, nil
}
