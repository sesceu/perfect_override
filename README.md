# 🛡️ Perfect Override

**Perfect Override** is a lightweight, headless DevContainer manager designed to decouple your development environment from the VS Code window.

It solves the "Staircase Problem" (disconnects killing your terminal) and the "Compliance vs. Preference" conflict (corporate settings vs. personal tools like `jj`).

## 🧐 Why Perfect Override?

We work in repositories with strict corporate `devcontainer.json` files that mandate specific extensions, linters, and settings. While necessary for compliance, this creates friction:
1.  **Fragility:** If VS Code disconnects (e.g., walking between meeting rooms), the dev container and all running terminals die.
2.  **Pollution:** Corporate settings (like aggressive GitLens) conflict with personal workflows (like `jj`).
3.  **Inflexibility:** You cannot easily "patch" the environment (e.g., adding `ripgrep` or mounting `~/bin`) without modifying tracked files.

**Perfect Override** solves this by treating the DevContainer as a persistent **Server**, not a VS Code plugin.

## 🎯 Goals

1.  **Persistence:** The container runs as a background daemon. You can quit VS Code, disconnect Wi-Fi, or reboot your laptop—the container (and your `tmux` session inside it) stays alive.
2.  **Overlay Configuration:** Apply a personal "Patch" on top of the corporate `devcontainer.json` at runtime.
    * Disable specific extensions (e.g., `gitlens`).
    * Add personal tools (e.g., `jj`, `fzf`).
    * Inject personal VS Code settings (e.g., `"git.enabled": false`).
3.  **Zero Trace:** All personal configs live outside the repo or in git-ignored files. No accidental commits of local hacks.

## 🏗️ Architecture

Perfect Override is a single Go binary that:
1.  Reads the corporate `.devcontainer/devcontainer.json`.
2.  Reads your `override.json`.
3.  Merges them in memory (handling Arrays and Objects intelligently).
4.  Generates a `.devcontainer.json` (git-ignored).
5.  Launches the container using `devcontainer up` (run `npm install -g @devcontainers/cli` to install).
6.  Provisions the running container (installs APT packages, symlinks dotfiles, injects settings).

## 🚀 Getting Started

### 1. Installation

Build the binary from source:

> [!TIP]
> Check the `samples/` directory for a complete example of a corporate setup with personal overrides.

> [!TIP]
> Check the `samples/` directory for a complete example of a corporate setup with personal overrides.

```bash
# In this directory
go build -o perfect-override main.go
```

### 2. Launching

Run the tool in your repository root (default):

```bash
./perfect-override
```

### ⚙️ Configuration Flags

You can customize the workspace and config paths using flags:

| Flag | Default | Description |
| :--- | :--- | :--- |
| `-workspace` | `.` | Path to the workspace folder (where `.git` lives) |
| `-config` | `.devcontainer/devcontainer.json` | Path to the base corporate config (relative to `-workspace`) |
| `-override` | `override.json` | Path to your personal override file (relative to CWD) |
| `-name-prefix` | `pf` | Prefix for the container name (e.g., `pf-my-project`) |

**Example:**
```bash
./perfect-override -workspace samples/basic-project -override samples/basic-project/override.json
```

---

### 🧪 Testing

You can run the automated end-to-end test suite to verify the tool's functionality:

```bash
./tests/e2e_test.sh
```

---

### 🤖 AI-Assisted Setup

Need help creating your `override.json`? Check out [PROMPT.md](file:///usr/local/google/home/sesc/git/perfect_override/PROMPT.md) for a ready-to-use prompt that you can give to an LLM (like Gemini or ChatGPT) to help you tailor your environment.

This test will:
1. Build the binary.
2. Launch a sample project with overrides.
3. Verify extensions, tool installation, and settings patching.
4. Shut down the test container.

---

## 🔌 Connecting to the Container

Once the container is running and provisioned, you can connect to it using VS Code's **Dev Containers: Attach to Running Container** command.

### 🏠 Case 1: Local Machine
If Perfect Override is running on your local machine:
1. Open VS Code.
2. Press `F1` and select **Dev Containers: Attach to Running Container...**.
3. Select the container (usually named after your folder).

### ☁️ Case 2: Remote Cloud VM (The "Pro" Setup)
If Perfect Override is running on a remote server (e.g., a powerful Cloud VM):

#### Option A: SSH with Port Forwarding
You can forward the container's SSH port (if configured) or the Docker socket.
1. **At the VM Level:** Connect your local VS Code to the VM using the **Remote - SSH** extension.
2. **Inside the VM SSH Session:** Open the project folder on the VM.
3. **Attach:** Press `F1` and select **Dev Containers: Attach to Running Container...**. VS Code will use the Docker agent running on the VM to discover and attach to the container.

#### Option B: Direct SSH into Container (Port Forwarding)
If you've mapped an SSH port in your `devcontainer.json` (e.g., `-p 2222:22`):
1. **Forward the port:** `ssh -L 2222:localhost:2222 your-cloud-vm`
2. **Connect VS Code:** Use the **Remote - SSH** extension to connect directly to `localhost:2222`.
   * *Tip:* Ensure your `override.json` symlinks your `~/.ssh/authorized_keys` so you can login without a password.

#### Option C: SSH Jumphost (Recommended)
This is the most secure and "headless" way, as it avoids exposing ports publicly on the VM.
1. **Configure SSH Config:** Add this to your local `~/.ssh/config`:
   ```ssh
   Host cloud-vm
       HostName your-cloud-vm-ip
       User your-vm-user

   Host devcontainer
       # If port 2222 is mapped in devcontainer.json (-p 2222:22)
       HostName localhost
       Port 2222
       # OR if using the internal container IP (no port mapping needed)
       # HostName 172.17.0.X 
       
       User vscode          # Or the remoteUser in devcontainer.json
       ProxyJump cloud-vm
   ```
2. **Connect VS Code:** Simply use **Remote - SSH: Connect to Host...** and select `devcontainer`. VS Code will tunnel through the VM to reach the container.
   * *Tip:* Ensure your `override.json` symlinks your `~/.ssh/authorized_keys` into the container for a seamless login.
