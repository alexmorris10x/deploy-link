#!/bin/bash

set -euo pipefail

script_directory="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
module_directory="$(cd "${script_directory}/.." && pwd -P)"
: "${HOME:?HOME must be set}"
runtime_root="${DEPLOY_LINK_RUNTIME_ROOT:-${HOME}/Library/Application Support/Deploy Link}"
link_core_commit="3ebc297691a9e364772aef027744ebc0c49421a5"
patch_sha256="$(shasum -a 256 "${module_directory}/patches/link-core-tailnet.patch" | awk '{print $1}')"

"${script_directory}/install.sh" --prepare-only

build_root="${DEPLOY_LINK_BUILD_ROOT:-${runtime_root}/build}"
checkout_sha256="$(printf '%s' "${module_directory}" | shasum -a 256 | awk '{print $1}')"
go_binary="${DEPLOY_LINK_TOOLCHAINS_DIR:-${build_root}/toolchains}/go1.26.5/bin/go"
source_root="${DEPLOY_LINK_SOURCES_DIR:-${build_root}/sources}/link-core-${link_core_commit}-${patch_sha256}"
workspace_file="${build_root}/workspaces/${patch_sha256}-${checkout_sha256}/go.work"
go_path="${GOPATH:-${build_root}/cache/gopath}"
go_build_cache="${GOCACHE:-${build_root}/cache/go-build}"
go_module_cache="${GOMODCACHE:-${build_root}/cache/go-mod}"

# Keep direct test invocations from falling back to Go's default $HOME/go path.
export GOPATH="${go_path}"
export GOCACHE="${go_build_cache}"
export GOMODCACHE="${go_module_cache}"

[[ "$("${go_binary}" env GOPATH)" == "${go_path}" ]]
[[ "$("${go_binary}" env GOCACHE)" == "${go_build_cache}" ]]
[[ "$("${go_binary}" env GOMODCACHE)" == "${go_module_cache}" ]]

git -C "${source_root}" apply --unidiff-zero --reverse --check "${module_directory}/patches/link-core-tailnet.patch"
git -C "${source_root}" diff --check

(
  cd "${source_root}"
  GOWORK="${workspace_file}" \
    "${go_binary}" test ./ios ./ios/tunnel ./ios/installationproxy ./ios/zipconduit
)
(
  cd "${module_directory}"
  GOWORK="${workspace_file}" \
    "${go_binary}" test ./...
  GOWORK="${workspace_file}" \
    "${go_binary}" test -race ./...
  GOWORK="${workspace_file}" \
    "${go_binary}" vet ./...
)

"${go_binary}" version
printf 'Deploy Link tests passed\n'
