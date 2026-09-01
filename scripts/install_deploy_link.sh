#!/bin/bash

set -euo pipefail

script_directory="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
repository_root="$(cd "${script_directory}/.." && pwd -P)"

: "${HOME:?HOME must be set}"
export DEPLOY_LINK_RUNTIME_ROOT="${DEPLOY_LINK_RUNTIME_ROOT:-${HOME}/.local/share/deploy-link}"

"${repository_root}/deploy-link/scripts/install.sh"
exec "${script_directory}/install_deploy_link_status.sh"
