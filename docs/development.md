# Development

For people changing nexora-migrate. Using it is covered in the
[guide](guide.en.md).

## Build

Only Go is needed: no cgo, no C toolchain, no Node, no bundler. The SQLite
driver is pure Go and the wizard's page is plain HTML, CSS and JavaScript
embedded with `go:embed`.

```sh
go build .            # or: ./build.sh   or: make build
```

Any platform from any platform:

```sh
GOOS=windows GOARCH=amd64 go build .      # on Windows: .\build.ps1
```

`make release` writes a binary per platform into `dist/`, and
`make package VERSION=1.2.0` writes the same archives and `SHA256SUMS` the
release pipeline publishes.

## Test

```sh
make test      # go vet + the suite
make race      # the suite under -race
gofmt -l .     # must print nothing
```

## Layout

```
main.go                    flags, and the blank imports that register readers
internal/bundle/           the canonical item and the nested selection tree
internal/normalize/        name cleaning and collisions
internal/convert/          Xray → sing-box, share-link parsing, the client shape
internal/sqlitex/          read-only SQLite with per-column probing
internal/httpx/            JSON HTTP that quotes the server in its errors
internal/source/<panel>/   one reader per source panel
internal/source/xraysql/   what the two x-ui lines share
internal/source/dbfetch/   the backup-download channel for s-ui, 3x-ui and x-ui
internal/target/           the Nexora client: login, preflight, create
internal/apply/            ordering, id late-binding, node pause/resume
internal/web/              the wizard: server, guards, upload, SSE, assets/
```

The design and the reasoning behind it are in [approach.md](approach.md).

## Pipelines

- **CI** (`.github/workflows/ci.yml`) runs on every push to `main` and every
  pull request: `go vet`, the suite (shuffled and under `-race`) and `gofmt`.
  It builds nothing.
- **Release** (`.github/workflows/release.yml`) runs only when a `v*` tag is
  pushed and takes no input. The version is the tag without its `v`. It tests,
  builds five platforms in a matrix (Windows and Linux on amd64 and arm64,
  macOS on arm64 only), then publishes one GitHub release with the archives and
  `SHA256SUMS`.

```sh
git tag v1.2.0 && git push origin v1.2.0
```

Archive names carry no version (`nexora-migrate-linux-amd64.tar.gz`), so the
`releases/latest/download/…` links in the README and the guides stay valid for
every release. The version is stamped into the binary: `nexora-migrate -version`.
