set shell := ["bash", "-eu", "-o", "pipefail", "-c"]

# Build TOPS and install/replace ~/.local/bin/tps without running it.
build:
  #!/usr/bin/env bash
  set -euo pipefail

  install_dir="${HOME}/.local/bin"
  mkdir -p "$install_dir"

  tmp_bin="$(mktemp)"
  trap 'rm -f "$tmp_bin"' EXIT

  go build -o "$tmp_bin" ./cmd/tops
  chmod +x "$tmp_bin"

  install_path="$install_dir/tps"
  mv "$tmp_bin" "$install_path"

  case ":${PATH:-}:" in
    *":$install_dir:"*) ;;
    *)
      echo "Warning: $install_dir is not in PATH." >&2
      ;;
  esac

  echo "Installed: $install_path"

# Build TOPS and install it to ~/.local/bin/tps, then execute it.
run: build
  #!/usr/bin/env bash
  set -euo pipefail
  exec "${HOME}/.local/bin/tps"
