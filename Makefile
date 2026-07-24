# Convenience wrapper over the cross-compile / packaging scripts. build.sh and
# install.sh remain the source of truth; this just gives the usual `make`
# entry points.

.PHONY: dist release install build test test-js clean bump-patch bump-minor bump-major

# Build the local server + admin + player binaries into ./bin (quick dev build).
build:
	go build -o ./bin/hespera ./cmd/hespera
	go build -o ./bin/hescli ./cmd/hescli
	go build -o ./bin/hesplay ./cmd/hesplay

# Cross-compile every platform + build the .deb packages into dist/.
dist:
	./build.sh

# Build everything, push main, and publish dist/ as GitHub release v<VERSION>
# (the channel the in-app update pill checks). Needs gh + nfpm + a clean tree.
release:
	./build.sh --publish

# Install the prebuilt .deb for this machine (Debian/Ubuntu) — run `make dist` first.
install:
	./install.sh

test:
	go test ./...

# Browser-JS tests (dev-only; needs `npm install` once for jsdom). Not part of
# the Go build — the binary embeds web/ and never runs Node.
test-js:
	npm test

clean:
	rm -rf dist bin

# Bump the semantic version (X.Y.Z) in VERSION — see bump.sh.
# patch = a minor change/fix, minor = a major feature, major = a breaking release.
bump-patch:
	./bump.sh patch
bump-minor:
	./bump.sh minor
bump-major:
	./bump.sh major
