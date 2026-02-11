package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
	Symlinks []Symlink `json:"symlinks"`
	AptPackages struct {
		Add    []string `json:"add"`
		Remove []string `json:"remove"`
	} `json:"aptPackages"`
	Commands []string `json:"commands"`
	HostHomeMount string `json:"hostHomeMount"`
	Features struct {
		Add    map[string]interface{} `json:"add"`
		Remove []string               `json:"remove"`
	} `json:"features"`
}

type Symlink struct {
	Source string `json:"source"` // Relative to Host Home
	Target string `json:"target"` // Relative to Container Home
}

// --- CONSTANTS ---
const DefaultHostMountPoint = "/host_home"

var (
	workspaceDir   string
	devcontainer   string
	overrideConfig string
)

func fixRelativePaths(base map[string]interface{}, baseConfigPath string) {
	build, ok := base["build"].(map[string]interface{})
	if !ok { return }
	
	dockerfile, ok := build["dockerfile"].(string)
	if !ok { return }
	
	if !filepath.IsAbs(dockerfile) {
		absConfigPath, _ := filepath.Abs(baseConfigPath)
		// The dockerfile path in devcontainer.json is relative to the config file itself.
		absDockerfile := filepath.Join(filepath.Dir(absConfigPath), dockerfile)
		build["dockerfile"] = absDockerfile
		fmt.Printf("📍 Absolutized Dockerfile: %s\n", absDockerfile)
	}
}

func main() {
	flag.StringVar(&workspaceDir, "workspace", ".", "Path to the workspace folder")
	flag.StringVar(&devcontainer, "config", ".devcontainer/devcontainer.json", "Path to the base devcontainer.json")
	flag.StringVar(&overrideConfig, "override", "override.json", "Path to the override.json config")
	flag.Parse()

	fmt.Println("🚀 Starting DevContainer Manager...")

	// 1. LOAD CONFIGS
	pConfig := loadPersonalConfig(overrideConfig)
	
	// Ensure we have absolute paths for everything to avoid relative path hell
	absWorkspace, _ := filepath.Abs(workspaceDir)
	baseConfigPath := filepath.Join(absWorkspace, devcontainer)
	baseMap := loadBaseConfig(baseConfigPath)

	// 2. CHECK ASSUMPTIONS
	checkAssumptions(baseMap, pConfig)

	// 2b. APPLY DYNAMIC NAME
	folderName := filepath.Base(absWorkspace)
	effectiveName := fmt.Sprintf("%s-%s", "pf", folderName)
	baseMap["name"] = effectiveName
	fmt.Printf("🏷️  Container Name: %s\n", effectiveName)

	// 3. PREPARE EFFECTIVE CONFIG
	// Note: The devcontainer CLI is extremely strict: the config MUST be named 
	// 'devcontainer.json' or '.devcontainer.json'. 
	// To preserve the corporate file, we use '.devcontainer.json' at the workspace root.
	effectiveFile := ".devcontainer.json"
	effectivePath := filepath.Join(absWorkspace, effectiveFile)
	
	fixRelativePaths(baseMap, baseConfigPath)
	applyExtensionChanges(baseMap, pConfig)
	applyFeatureChanges(baseMap, pConfig)
	injectHostMount(baseMap, pConfig)
	saveJSON(effectivePath, baseMap)
	addToGitIgnore(absWorkspace, effectiveFile)

	// 4. LAUNCH CONTAINER
	fmt.Println("🐳 Launching Container...")
	runCommand(absWorkspace, "devcontainer", "up",
		"--config", effectiveFile,
		"--workspace-folder", ".",
	)

	// 5. PROVISIONING
	fmt.Println("⚙️  Provisioning Environment...")
	
	if len(pConfig.AptPackages.Add) > 0 || len(pConfig.AptPackages.Remove) > 0 {
		installAptPackages(pConfig.AptPackages, ".", effectiveFile, absWorkspace)
	}

	if len(pConfig.Commands) > 0 {
		runCustomCommands(pConfig.Commands, ".", effectiveFile, absWorkspace)
	}

	// PATCH SETTINGS (Read -> Modify -> Write)
	patchMachineSettings(pConfig, pConfig.Checks.ExpectedRemoteUser, ".", effectiveFile, absWorkspace)

	// CREATE SYMLINKS
	createSymlinks(pConfig, pConfig.Checks.ExpectedHomeDir, ".", effectiveFile, absWorkspace)

	// 6. CLEANUP (Zero Trace)
	fmt.Println("🧹 Cleaning up temporary config...")
	_ = os.Remove(effectivePath)

	fmt.Println("✅ DONE! Connect to Port 2222.")
}

// --- CORE LOGIC ---

func patchMachineSettings(pConfig PersonalConfig, user string, workspace string, config string, cwd string) {
	targetDir := fmt.Sprintf("/home/%s/.vscode-server/data/Machine", user)
	targetFile := filepath.Join(targetDir, "settings.json")

	fmt.Println("📝 Patching VS Code settings...")

	// A. Ensure Dir Exists
	runCommand(cwd, "devcontainer", "exec", "--config", config, "--workspace-folder", workspace, "mkdir", "-p", targetDir)

	// B. Read Existing Settings (if any)
	// We use 'cat' via exec. If file fails, we assume empty JSON object.
	cmd := exec.Command("devcontainer", "exec", "--config", config, "--workspace-folder", workspace, "cat", targetFile)
	cmd.Dir = cwd
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
	runCommand(cwd, "devcontainer", "exec", "--config", config, "--workspace-folder", workspace, "bash", "-c", writeCmd)
}

func createSymlinks(pConfig PersonalConfig, homeDir string, workspace string, config string, cwd string) {
	mountPoint := pConfig.HostHomeMount
	if mountPoint == "" {
		mountPoint = DefaultHostMountPoint
	}

	for _, link := range pConfig.Symlinks {
		srcPath := filepath.Join(mountPoint, link.Source)
		tgtPath := link.Target
		if !filepath.IsAbs(tgtPath) {
			tgtPath = filepath.Join(homeDir, link.Target)
		}
		tgtDir := filepath.Dir(tgtPath)

		fmt.Printf("🔗 Linking %s -> %s\n", link.Source, link.Target)
		
		// We use sudo for everything here because targets like /usr/local/bin 
		// or /home/builder/.intrinsic/bin might have restricted permissions or be mounts.
		script := fmt.Sprintf(
			"sudo mkdir -p %s && sudo rm -rf %s && sudo ln -sf %s %s", 
			tgtDir, tgtPath, srcPath, tgtPath,
		)
		
		// Capture output to check for "Read-only file system"
		cmd := exec.Command("devcontainer", "exec", "--config", config, "--workspace-folder", workspace, "bash", "-c", script)
		cmd.Dir = cwd
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		err := cmd.Run()

		if err != nil {
			errStr := stderr.String()
			if strings.Contains(errStr, "Read-only file system") {
				fmt.Printf("⚠️  Warning: Could not create symlink %s (Read-only file system). Skipping.\n", link.Target)
			} else {
				fmt.Printf("❌ Failed to create symlink %s: %v\n%s\n", link.Target, err, errStr)
				// We don't exit here to allow other provisioning to potentially succeed
			}
		}
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

func applyFeatureChanges(base map[string]interface{}, pConfig PersonalConfig) {
	// Initialize features map if missing
	features, ok := base["features"].(map[string]interface{})
	if !ok {
		features = make(map[string]interface{})
		base["features"] = features
	}

	// 1. Apply Removals
	for _, id := range pConfig.Features.Remove {
		delete(features, id)
	}

	// 2. Apply Adds/Overrides
	for id, val := range pConfig.Features.Add {
		features[id] = val
	}
}

func injectHostMount(base map[string]interface{}, pConfig PersonalConfig) {
	mountPoint := pConfig.HostHomeMount
	if mountPoint == "" {
		mountPoint = DefaultHostMountPoint
	}
	mountString := fmt.Sprintf("source=${localEnv:HOME},target=%s,type=bind,consistency=cached", mountPoint)
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

func installAptPackages(apt struct {
	Add    []string `json:"add"`
	Remove []string `json:"remove"`
}, workspace string, config string, cwd string) {
	if len(apt.Remove) > 0 {
		fmt.Printf("�️ Removing packages: %v\n", apt.Remove)
		pkgList := strings.Join(apt.Remove, " ")
		script := fmt.Sprintf("sudo apt-get remove -y %s", pkgList)
		runCommand(cwd, "devcontainer", "exec", "--config", config, "--workspace-folder", workspace, "bash", "-c", script)
	}
	if len(apt.Add) > 0 {
		fmt.Printf("�📦 Installing packages: %v\n", apt.Add)
		pkgList := strings.Join(apt.Add, " ")
		script := fmt.Sprintf("sudo apt-get update && sudo apt-get install -y %s", pkgList)
		runCommand(cwd, "devcontainer", "exec", "--config", config, "--workspace-folder", workspace, "bash", "-c", script)
	}
}

func runCustomCommands(commands []string, workspace string, config string, cwd string) {
	fmt.Println("🛠️ Running custom commands...")
	for _, cmd := range commands {
		fmt.Printf("  > %s\n", cmd)
		runCommand(cwd, "devcontainer", "exec", "--config", config, "--workspace-folder", workspace, "bash", "-c", cmd)
	}
}

// --- BOILERPLATE UTILS ---

func stripComments(data []byte) []byte {
	// Remove single-line comments // ... but only if preceded by space or start of line
	// (Avoids stripping https://URLs)
	reSingle := regexp.MustCompile(`(?m)(^|\s)//.*$`)
	// Remove multi-line comments /* ... */
	reMulti := regexp.MustCompile(`(?s)/\*.*?\*/`)
	
	res := reMulti.ReplaceAll(data, []byte(""))
	res = reSingle.ReplaceAll(res, []byte("$1")) // Preserve the prefixing whitespace
	return res
}

func loadPersonalConfig(path string) PersonalConfig {
	file, err := os.ReadFile(path)
	if err != nil {
		fmt.Printf("❌ Could not read %s.\n", path)
		os.Exit(1)
	}
	cleanJSON := stripComments(file)
	var config PersonalConfig
	if err := json.Unmarshal(cleanJSON, &config); err != nil {
		fmt.Printf("❌ Failed to parse %s: %v\n", path, err)
		os.Exit(1)
	}
	return config
}

func loadBaseConfig(path string) map[string]interface{} {
	file, err := os.ReadFile(path)
	if err != nil {
		fmt.Printf("❌ Could not read base config %s: %v\n", path, err)
		os.Exit(1)
	}
	cleanJSON := stripComments(file)
	var config map[string]interface{}
	if err := json.Unmarshal(cleanJSON, &config); err != nil {
		fmt.Printf("❌ Failed to parse base config %s: %v\n", path, err)
		os.Exit(1)
	}
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

func runCommand(cwd string, name string, args ...string) {
	cmd := exec.Command(name, args...)
	cmd.Dir = cwd
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fmt.Printf("❌ Command failed: %s %v (in %s)\n", name, args, cwd)
		os.Exit(1)
	}
}

func contains(slice []string, item string) bool {
	for _, s := range slice { if s == item { return true } }
	return false
}