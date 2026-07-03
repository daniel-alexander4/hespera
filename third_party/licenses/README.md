# Vendored third-party license texts

Verbatim license texts for every module compiled into the Hespera binaries,
one directory per module path — the texts that `THIRD_PARTY_LICENSES.md` (repo
root) names by type. Embedded into the binary (`embed.go`) and served at
`/about/licenses`, and shipped in the `.deb` under
`/usr/share/doc/hespera/licenses`.

Regenerate after adding or updating a dependency:

    go-licenses save ./cmd/hespera --save_path /tmp/hespera-licenses
    # copy the module dirs (NOT the hespera/ main-module copy) into
    # third_party/licenses/ — never point --save_path inside the repo
    # (go-licenses copies the main module too and recurses).

Two known manual cases:
- modernc.org/mathutil — go-licenses can't classify its LICENSE; copy it from
  the module cache.
- github.com/gcottom/audiometa/v3 — the published module omits the license;
  fetch LICENSE.md from the upstream repo (MIT, per THIRD_PARTY_LICENSES.md).

`TestThirdPartyLicenseTexts` (cmd/hespera) fails the build when a go.mod
require-block module has no text here.
