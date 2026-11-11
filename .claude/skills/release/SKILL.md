---
name: release
description: Release a new nForge version - bump config.yaml, AGENTS.md and CHANGELOG.md to the given version, run every quality gate, commit chore(release), create the annotated tag and, after explicit confirmation, push main and the tag and verify CI and the Go module proxy. Use only when the user invokes /release with a version.
argument-hint: vX.Y.Z
disable-model-invocation: true
allowed-tools: Bash, Read, Edit
---

# Release nForge $ARGUMENTS

Release the framework as version `$ARGUMENTS` (normalize to `vX.Y.Z`; a bare
`X.Y.Z` gets the `v`). If no version was given, stop and ask for one: never
guess it.

A published tag is permanent: the Go module proxy keeps the first content it
sees for a version forever, and a tag is never moved or recreated (AGENTS.md
§1). Everything before the push is local and can be undone; the push cannot.
So stop at the first failure, and never push without the user's explicit
confirmation in this run.

Commit messages and tags are in English and carry **no** Co-Authored-By or
other Claude/Anthropic attribution.

## 1. Preflight (stop on any failure)

Run from the repository root and check each point:

- `git branch --show-current` is `main` and `git status --porcelain` is empty.
- `git fetch origin --tags` and `git rev-parse HEAD` equals
  `git rev-parse origin/main` (nothing to pull, nothing unpushed).
- The tag does not exist: `git rev-parse -q --verify "refs/tags/$VERSION"`
  prints nothing and `git ls-remote --tags origin "$VERSION"` is empty.
- The last CI run of `origin/main` succeeded:
  `gh run list --branch main --limit 1 --json conclusion,headSha` shows
  `success` for the current HEAD. If it is still running, wait for it
  (`gh run watch <id> --exit-status`). If it failed, find out why
  (`gh run view <id> --log-failed`) before anything else: a failure that a
  small fix cures (a vulnerable toolchain or dependency reported by
  `govulncheck`, a lint rule) is fixed in its own commit **before** the bump,
  with its changelog entry; anything else stops the release.
- Commits that are not on `origin/main` yet (the release often carries them)
  are allowed, but nothing is pushed until step 5 and the local gates of
  step 3 stand in for CI until then. Uncommitted work is never swept into the
  release: stop and ask how to commit it.
- Semver sanity: compare `$VERSION` with the current `version` in
  `config.yaml`. A version with breaking changes (`BREAKING CHANGE`, `feat!`
  or `### Removed` in the Unreleased notes) needs a MINOR bump while the
  major is 0 (MAJOR from 1.0.0 on); a PATCH bump then is wrong. If the
  requested version does not fit, say why and ask before continuing.
- `## [Unreleased]` in CHANGELOG.md has the release notes. Show the user a
  short summary of them.

Dry-run the edits: `python3 -I .claude/skills/release/bump.py "$VERSION" --check`.

## 2. Bump the files

`python3 -I .claude/skills/release/bump.py "$VERSION"` edits:

- `config.yaml`: `version: "X.Y.Z"` (embedded as `nforge.Version`);
- `AGENTS.md`: `(currently \`X.Y.Z\`)`;
- `CHANGELOG.md`: the notes move under `## [vX.Y.Z] - <today>`, an empty
  `## [Unreleased]` stays on top, and the compare link
  `[vX.Y.Z]: https://github.com/ams-soft/nforge/compare/vPREV...vX.Y.Z` is
  added to the link list.

Then check that nothing else cites the old framework version as current:
`grep -rn "<old X.Y.Z>" --include=*.md --include=*.txt --include=*.yaml .`
must only hit CHANGELOG.md history. Fix any other hit (and mention it).
`git diff` must show only these files.

## 3. Quality gates (all must pass; report each one)

```bash
# every example (`./...` skips `_` directories); keep this list equal to AGENTS.md §15
EXAMPLES='./_examples/basic/... ./_examples/complete/... ./_examples/jose/... ./_examples/cache/... ./_examples/database/... ./_examples/message/... ./_examples/qrcode/... ./_examples/static/...'
test -z "$(gofmt -l .)"
go build ./... && go build -tags nforge_graph ./...
go vet ./... $EXAMPLES && go vet -tags nforge_graph ./http/...
golangci-lint run ./... $EXAMPLES
golangci-lint run --build-tags nforge_graph ./http/...
go test -race ./... $EXAMPLES          # includes docs_test.go: AGENTS.md must cite nforge.Version
go test -race -tags nforge_graph ./http/...
govulncheck ./...          # with the toolchain pinned in go.mod: a stdlib fix needs a newer pin
```

`go version` must print the toolchain pinned in `go.mod` (`GOTOOLCHAIN=auto`
downloads it); `govulncheck` must say `No vulnerabilities found`.

Integration tests need PostgreSQL, Redis and RabbitMQ. Each example owns its
Compose file (`_examples/database`, `_examples/cache`, `_examples/message`, and
`_examples/complete` for the three; copy `.env.example` to `.env` first). If
the services are healthy (or the user agrees to start them), run:

```bash
NFORGE_TEST_DATABASE_URL='postgres://app:app@localhost:5432/app?sslmode=disable' \
NFORGE_TEST_REDIS_URL='redis://localhost:6379/15' \
NFORGE_TEST_RABBITMQ_URL='amqp://nforge:nforge@localhost:5672/' \
  go test -race -p 1 -tags integration ./database/... ./cache/ ./app/ ./message/ $EXAMPLES
```

Otherwise say plainly that they did not run locally (CI runs them after the
push). Never report a gate as passed without its output. On any failure:
`git checkout -- config.yaml AGENTS.md CHANGELOG.md`, report, stop.

## 4. Commit and tag (local)

```bash
git add config.yaml AGENTS.md CHANGELOG.md
git commit -m "chore(release): $VERSION"
git tag -a "$VERSION" -m "$VERSION"
```

Show `git show --stat HEAD` and `git tag -n1 "$VERSION"`.

## 5. Confirm, then publish

Ask the user, with AskUserQuestion, whether to push now, stating that the tag
becomes permanent on the Go module proxy. Recommend the safe order: push
`main` first, wait for its CI, and push the tag only when it is green (a red
CI after the tag cannot be undone, only fixed forward). If they decline, leave
the commit and the tag local and tell them how to undo (`git tag -d $VERSION
&& git reset --hard <the commit before the release commit>`) or publish later.

On confirmation, re-check that `origin/main` did not move (`git fetch origin`;
its tip must be an ancestor of HEAD), then:

```bash
git push origin main
gh run list --branch main --limit 1     # then: gh run watch <id> --exit-status
git push origin "$VERSION"              # only after the CI of main succeeded
```

If the user chose to push both at once, push `main` and the tag together and
watch the CI as in step 6.

## 6. Verify

- Watch the CI run of the release commit to the end
  (`gh run list --branch main --limit 1`, then `gh run watch <id> --exit-status`).
  If it fails before the tag was pushed, fix forward with a new commit and
  re-run the gates; the release commit may be amended only while the tag is
  still local. If it fails after the tag was pushed, do **not** touch the tag:
  report it; the fix is a new patch release.
- The module proxy serves the version:
  `GOPROXY=https://proxy.golang.org GOFLAGS=-mod=mod go list -m github.com/ams-soft/nforge@$VERSION`
  (it can take a minute; retry a few times).
- Optionally create the GitHub release from the CHANGELOG section if the user
  wants it: `gh release create "$VERSION" --title "$VERSION" --notes-file <section>`.

Finish with a short report: version, commit SHA, the gates that ran (and the
ones that did not), CI result and proxy status.
