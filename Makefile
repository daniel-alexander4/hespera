# Convenience wrapper over the cross-compile / packaging scripts. build.sh and
# install.sh remain the source of truth; this just gives the usual `make`
# entry points.

.PHONY: dist install build test clean bump-patch bump-minor bump-major

# Build the local server + admin binaries into ./bin (quick dev build).
build:
	go build -o ./bin/hespera ./cmd/hespera
	go build -o ./bin/hescli ./cmd/hescli

# Cross-compile every platform + build the .deb packages into dist/.
dist:
	./build.sh

# Build, package, and install Hespera on this machine (Debian/Ubuntu).
install:
	./install.sh

test:
	go test ./...

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
