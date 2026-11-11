// A stand-in for a bundler's output: the file name carries a content hash, so it
// can be served as immutable (WithSPAImmutableDir("build")).
document.getElementById("route").textContent = location.pathname;
fetch("/api/health")
  .then((r) => r.json())
  .then((j) => { document.getElementById("api").textContent = JSON.stringify(j.data); })
  .catch(() => { document.getElementById("api").textContent = "unreachable"; });
