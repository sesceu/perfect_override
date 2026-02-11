package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// --- CONFIGURATION STRUCTS ---

type PersonalConfig struct {
	Checks struct {
		ExpectedRemoteUser string `json:"expectedRemoteUser"`
		ExpectedHomeDir    string `json:"expectedHomeDir"`
	} `json:"checks"`
	Extensions struct {
		Add    []string `json:"add"`
		Remove []string `json:"remove"`
	} `json:"extensions"`
	Settings struct {
		Add    map[string]interface{} `json:"add"`
		Remove []string               `json:"remove"`
	} `json:"settings"`
	Symlinks    []Symlink `json:"symlinks"`
	AptPackages []string  `json:"aptPackages"`
}

type Symlink struct {
	Source string `json:"source"` // Relative to Host Home
	Target string `json:"target"` // Relative to Container Home
}

// --- CONSTANTS ---
const HostMountPoint = "/host_home"

var (
	workspaceDir   string
	devcontainer   string
	overrideConfig string
)

func main() {
	flag.StringVar(&workspaceDir, "workspace", ".", "Path to the workspace folder")
	flag.StringVar(&devcontainer, "config", ".devcontainer/devcontainer.json", "Path to the base devcontainer.json")
	flag.StringVar(&overrideConfig, "override", "override.json", "Path to the override.json config")
	flag.Parse()

	fmt.Println("🚀 Starting DevContainer Manager...")

	// 1. LOAD CONFIGS
	pConfig := loadPersonalConfig(overrideConfig)
	baseMap := loadBaseConfig(filepath.Join(workspaceDir, devcontainer))

	// 2. CHECK ASSUMPTIONS
	checkAssumptions(baseMap, pConfig)

	// 3. PREPARE EFFECTIVE CONFIG
	effectivePath := filepath.Join(workspaceDir, ".devcontainer.json")
	applyExtensionChanges(baseMap, pConfig)
	injectHostMount(baseMap)
	saveJSON(effectivePath, baseMap)
	addToGitIgnore(workspaceDir, ".devcontainer.json")

	// 4. LAUNCH CONTAINER
	fmt.Println("🐳 Launching Container...")
	runCommand("devcontainer", "up",
		"--config", effectivePath,
		"--workspace-folder", workspaceDir,
	)

	// 5. PROVISIONING
	fmt.Println("⚙️  Provisioning Environment...")
	
	if len(pConfig.AptPackages) > 0 {
		installAptPackages(pConfig.AptPackages, workspaceDir, effectivePath)
	}

	// PATCH SETTINGS (Read -> Modify -> Write)
	patchMachineSettings(pConfig, pConfig.Checks.ExpectedRemoteUser, workspaceDir, effectivePath)

	// CREATE SYMLINKS
	createSymlinks(pConfig.Symlinks, pConfig.Checks.ExpectedHomeDir, workspaceDir, effectivePath)

	fmt.Println("✅ DONE! Connect to Port 2222.")
}

// --- CORE LOGIC ---

func patchMachineSettings(pConfig PersonalConfig, user string, workspace string, config string) {
	targetDir := fmt.Sprintf("/home/%s/.vscode-server/data/Machine", user)
	targetFile := filepath.Join(targetDir, "settings.json")

	fmt.Println("📝 Patching VS Code settings...")

	// A. Ensure Dir Exists
	runCommand("devcontainer", "exec", "--config", config, "--workspace-folder", workspace, "mkdir", "-p", targetDir)

	// B. Read Existing Settings (if any)
	// We use 'cat' via exec. If file fails, we assume empty JSON object.
	cmd := exec.Command("devcontainer", "exec", "--config", config, "--workspace-folder", workspace, "cat", targetFile)
	var out bytes.Buffer
	cmd.Stdout = &out
	_ = cmd.Run() // Ignore error (file might not exist)

	currentSettings := make(map[string]interface{})
	if out.Len() > 0 {
		// Try to parse existing. If fail (e.g. valid json but empty), start fresh.
		_ = json.Unmarshal(out.Bytes(), &currentSettings)
	}

	// C. Apply Removals
	for _, key := range pConfig.Settings.Remove {
		delete(currentSettings, key)
	}

	// D. Apply Adds (Upsert)
	for key, val := range pConfig.Settings.Add {
		currentSettings[key] = val
	}

	// E. Write Back
	jsonBytes, _ := json.MarshalIndent(currentSettings, "", "  ")
	writeCmd := fmt.Sprintf("cat > %s <<EOF\n%s\nEOF", targetFile, string(jsonBytes))
	runCommand("devcontainer", "exec", "--config", config, "--workspace-folder", workspace, "bash", "-c", writeCmd)
}

func createSymlinks(links []Symlink, homeDir string, workspace string, config string) {
	for _, link := range links {
		srcPath := filepath.Join(HostMountPoint, link.Source)
		tgtPath := filepath.Join(homeDir, link.Target)
		tgtDir := filepath.Dir(tgtPath)

		fmt.Printf("🔗 Linking %s -> %s\n", link.Source, link.Target)
		
		// 1. Ensure target directory exists
		// 2. Remove existing file/link at target (ln -sf isn't always enough if it's a directory)
		// 3. Create symlink
		script := fmt.Sprintf(
			"mkdir -p %s && rm -rf %s && ln -sf %s %s", 
			tgtDir, tgtPath, srcPath, tgtPath,
		)
		runCommand("devcontainer", "exec", "--config", config, "--workspace-folder", workspace, "bash", "-c", script)
	}
}

func applyExtensionChanges(base map[string]interface{}, pConfig PersonalConfig) {
	// Initialize map structure if missing
	customizations, _ := base["customizations"].(map[string]interface{})
	if customizations == nil { customizations = make(map[string]interface{}); base["customizations"] = customizations }
	vscode, _ := customizations["vscode"].(map[string]interface{})
	if vscode == nil { vscode = make(map[string]interface{}); customizations["vscode"] = vscode }

	// Get existing extensions safely
	existingExts := []string{}
	if rawList, ok := vscode["extensions"].([]interface{}); ok {
		for _, v := range rawList {
			if s, ok := v.(string); ok { existingExts = append(existingExts, s) }
		}
	}

	// Filter Logic
	finalList := []string{}
	// Add existing ones UNLESS they are in remove list
	for _, ext := range existingExts {
		if !contains(pConfig.Extensions.Remove, ext) {
			finalList = append(finalList, ext)
		}
	}
	// Add new ones (avoid duplicates)
	for _, add := range pConfig.Extensions.Add {
		if !contains(finalList, add) {
			finalList = append(finalList, add)
		}
	}

	vscode["extensions"] = finalList
}

func injectHostMount(base map[string]interface{}) {
	mountString := fmt.Sprintf("source=${localEnv:HOME},target=%s,type=bind,consistency=cached", HostMountPoint)
	mounts := []interface{}{}
	if existing, ok := base["mounts"].([]interface{}); ok {
		mounts = existing
	}
	mounts = append(mounts, mountString)
	base["mounts"] = mounts
}

func checkAssumptions(base map[string]interface{}, pConfig PersonalConfig) {
	remoteUser, ok := base["remoteUser"].(string)
	if !ok { return } // If not defined, we can't check.
	
	if remoteUser != pConfig.Checks.ExpectedRemoteUser {
		fmt.Printf("❌ Safety Check Failed!\nExpected remoteUser: '%s'\nFound in config:   '%s'\n", pConfig.Checks.ExpectedRemoteUser, remoteUser)
		fmt.Println("Update personal-config.json or the devcontainer.json to match.")
		os.Exit(1)
	}
}

func installAptPackages(pkgs []string, workspace string, config string) {
	fmt.Printf("📦 Installing packages: %v\n", pkgs)
	pkgList := strings.Join(pkgs, " ")
	script := fmt.Sprintf("sudo apt-get update && sudo apt-get install -y %s", pkgList)
	runCommand("devcontainer", "exec", "--config", config, "--workspace-folder", workspace, "bash", "-c", script)
}

// --- BOILERPLATE UTILS ---

func loadPersonalConfig(path string) PersonalConfig {
	file, err := os.ReadFile(path)
	if err != nil {
		fmt.Printf("❌ Could not read %s.\n", path)
		os.Exit(1)
	}
	var config PersonalConfig
	if err := json.Unmarshal(file, &config); err != nil { panic(err) }
	return config
}

func loadBaseConfig(path string) map[string]interface{} {
	file, err := os.ReadFile(path)
	if err != nil { panic(err) }
	var config map[string]interface{}
	if err := json.Unmarshal(file, &config); err != nil { panic(err) }
	return config
}

func saveJSON(path string, data interface{}) {
	file, _ := json.MarshalIndent(data, "", "  ")
	os.WriteFile(path, file, 0644)
}

func addToGitIgnore(workspace string, filename string) {
	excludeFile := filepath.Join(workspace, ".git/info/exclude")
	f, err := os.OpenFile(excludeFile, os.O_APPEND|os.O_WRONLY|os.O_CREATE, 0600)
	if err != nil { return }
	defer f.Close()
	// Check if already exists (naive check)
	content, _ := os.ReadFile(excludeFile)
	if !strings.Contains(string(content), filename) {
		if _, err = f.WriteString("\n" + filename + "\n"); err != nil { panic(err) }
	}
}

func runCommand(name string, args ...string) {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Printf("❌ Command failed: %s %v\n", name, args)
		os.Exit(1)
	}
}

func contains(slice []string, item string) bool {
	for _, s := range slice { if s == item { return true } }
	return false
}