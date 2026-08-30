#!/bin/bash

set -euo pipefail

script_directory="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
repository_root="$(cd "${script_directory}/.." && pwd -P)"

: "${HOME:?HOME must be set}"
export DEPLOY_LINK_RUNTIME_ROOT="${DEPLOY_LINK_RUNTIME_ROOT:-${HOME}/.local/share/deploy-link}"

exec "${repository_root}/deploy-link/scripts/install.sh"
