# Building this is one command, on purpose.
#
# There is no cgo, no C toolchain, no npm and no bundler: the SQLite driver is
# pure Go and the wizard's page is plain HTML/CSS/JS embedded with go:embed. So
# `go build .` produces a working binary on any machine that has Go, and a
# Windows build is the same command with two environment variables.

VERSION ?= dev
LDFLAGS := -s -w -X main.version=$(VERSION)
GOFLAGS := -trimpath
export CGO_ENABLED = 0

.PHONY: build
build:
	go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o nexora-migrate .

.PHONY: run
run:
	go run .

.PHONY: test
test:
	go vet ./...
	go test -shuffle=on -count=1 ./...

.PHONY: race
race:
	CGO_ENABLED=1 go test -race -count=1 ./...

# One binary per platform, ready to hand to an operator.
PLATFORMS := windows/amd64 windows/arm64 linux/amd64 linux/arm64 darwin/arm64

.PHONY: release
release:
	@rm -rf dist && mkdir -p dist
	@for p in $(PLATFORMS); do \
		os=$${p%%/*}; arch=$${p##*/}; ext=""; \
		[ "$$os" = "windows" ] && ext=".exe"; \
		out="dist/nexora-migrate-$$os-$$arch$$ext"; \
		GOOS=$$os GOARCH=$$arch go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $$out . || exit 1; \
		printf "  %-16s %s\n" "$$p" "$$(du -h $$out | cut -f1)"; \
	done

# The archives the release pipeline publishes, built locally: one per platform
# plus checksums. The names carry no version, so the docs' latest-release links
# stay valid; the version is stamped into the binary. `make package VERSION=1.2.0`.
.PHONY: package
package:
	@rm -rf dist && mkdir -p dist/stage
	@for p in $(PLATFORMS); do \
		os=$${p%%/*}; arch=$${p##*/}; ext=""; \
		[ "$$os" = "windows" ] && ext=".exe"; \
		bin="dist/stage/nexora-migrate$$ext"; \
		GOOS=$$os GOARCH=$$arch go build $(GOFLAGS) -ldflags "$(LDFLAGS)" -o $$bin . || exit 1; \
		name="nexora-migrate-$$os-$$arch"; \
		if [ "$$os" = "windows" ]; then \
			(cd dist/stage && zip -q "../$$name.zip" "nexora-migrate$$ext"); \
		else \
			tar -czf "dist/$$name.tar.gz" -C dist/stage "nexora-migrate$$ext"; \
		fi; \
		rm -f "$$bin"; \
		printf "  %-16s %s\n" "$$p" "$$name"; \
	done
	@rmdir dist/stage
	@cd dist && (sha256sum * 2>/dev/null || shasum -a 256 *) > SHA256SUMS
	@echo "  checksums        dist/SHA256SUMS"

.PHONY: clean
clean:
	rm -rf dist nexora-migrate nexora-migrate.exe
