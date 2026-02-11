# Perfect Override

This tool generates a strict, safe, and reproducible DevContainer configuration by merging a corporate base configuration with your personal preferences.

## Features

- **Strict Merging:** Deep merges JSON configurations while validating schema.
- **Extensions:** Add or remove VS Code extensions.
- **Settings:** Patch enabled/disabled settings (e.g., `git.enabled`, `editor.formatOnSave`).
- **Symlinks:** Symlink files from your host machine into the container (supports read-only mount detection).
- **APT Packages:** Install or remove system packages.
- **Shell Commands:** Run arbitrary shell commands during provisioning.
- **DevContainer Features:** Add or remove DevContainer features (e.g. `node`, `docker-in-docker`).
- **Custom Mounts:** Configurable host home mount point.

## Configuration (`override.json`)

The tool reads from `override.json` by default.

### Example Configuration

```json
{
  "checks": {
    "expectedRemoteUser": "vscode",
    "expectedHomeDir": "/home/vscode"
  },
  "hostHomeMount": "/host_home",
  "extensions": {
    "add": [
      "golang.go",
      "martijnhols.jj-code"
    ],
    "remove": [
      "eamodio.gitlens"
    ]
  },
  "settings": {
    "add": {
      "editor.formatOnSave": true,
      "git.enabled": false
    },
    "remove": [
      "workbench.colorTheme"
    ]
  },
  "symlinks": [
    {
      "source": ".bashrc",
      "target": ".bashrc_custom"
    },
    {
      "source": "/usr/local/bin/my-script",
      "target": "/usr/local/bin/my-script"
    }
  ],
  "aptPackages": {
    "add": [
      "ripgrep",
      "fd-find"
    ],
    "remove": [
      "nano"
    ]
  },
  "features": {
    "add": {
      "ghcr.io/devcontainers/features/node:1": {}
    },
    "remove": [
      "ghcr.io/devcontainers/features/docker-in-docker:2"
    ]
  },
  "commands": [
    "curl -sS https://starship.rs/install.sh | sh -s -- --yes",
    "echo 'eval \"$(starship init bash)\"' >> ~/.bashrc"
  ]
}
```

## Usage

```bash
# Basic usage (defaults)
./perfect-override

# Custom paths
./perfect-override \
  -workspace /path/to/project \
  -config .devcontainer/devcontainer.json \
  -override my-config.json
```
