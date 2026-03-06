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

// --- INTERFACES ---

type CommandRunner interface {
	Run(dir string, name string, args ...string) error
	RunOutput(dir string, name string, args ...string) (string, error)
}

type RealCommandRunner struct{}

func (r *RealCommandRunner) Run(dir string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func (r *RealCommandRunner) RunOutput(dir string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout = &out
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	err := cmd.Run()
	if err != nil {
		return stderr.String(), err
	}
	return out.String(), nil
}

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



func main() {
	if err := Run(&RealCommandRunner{}, os.Args); err != nil {
		os.Exit(1)
	}
}

func Run(runner CommandRunner, args []string) error {
	// Custom FlagSet to avoid polluting global state and allow testing
	fs := flag.NewFlagSet("perfect-override", flag.ContinueOnError)
	var (
		workspaceDir   string
		devcontainer   string
		overrideConfig string
	)
	fs.StringVar(&workspaceDir, "workspace", ".", "Path to the workspace folder")
	fs.StringVar(&devcontainer, "config", ".devcontainer/devcontainer.json", "Path to the base devcontainer.json (relative to workspace)")
	fs.StringVar(&overrideConfig, "override", "override.json", "Path to the override.json config")
	
	// Parse args (excluding program name)
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}

	fmt.Println("🚀 Starting DevContainer Manager...")

	// 1. LOAD CONFIGS
	pConfig := loadPersonalConfig(overrideConfig)
	
	// Ensure we have absolute paths for everything to avoid relative path hell
	absWorkspace, _ := filepath.Abs(workspaceDir)
	baseConfigPath := filepath.Join(absWorkspace, devcontainer)
	baseMap := loadBaseConfig(baseConfigPath)

	// 2. CHECK ASSUMPTIONS
	if err := checkAssumptions(baseMap, pConfig); err != nil {
		fmt.Println(err)
		return err
	}

	// 2b. APPLY DYNAMIC NAME
	folderName := filepath.Base(absWorkspace)
	effectiveName := fmt.Sprintf("%s-%s", "pf", folderName)
	baseMap["name"] = effectiveName
	fmt.Printf("🏷️  Container Name: %s\n", effectiveName)

	// 3. PREPARE EFFECTIVE CONFIG
	// The devcontainer CLI is extremely strict: the config MUST be named 
	// 'devcontainer.json' or '.devcontainer.json'. 
	// To preserve the context paths and native .dockerignore behavior, we write 
	// the overlay file completely adjacent to the base config file. 
	// If the base is .devcontainer/devcontainer.json, our effective file becomes .devcontainer/.devcontainer.json
	baseConfigDir := filepath.Dir(baseConfigPath)
	effectiveFile := ".devcontainer.json"
	effectivePath := filepath.Join(baseConfigDir, effectiveFile)
	
	// We need the relative path from the workspace for the --config flag
	relEffectivePath, _ := filepath.Rel(absWorkspace, effectivePath)
	
	applyExtensionChanges(baseMap, pConfig)
	applyFeatureChanges(baseMap, pConfig)
	applySettingsChanges(baseMap, pConfig)
	injectHostMount(baseMap, pConfig)
	saveJSON(effectivePath, baseMap)
	addToGitIgnore(absWorkspace, relEffectivePath)

	// 4. LAUNCH CONTAINER
	fmt.Println("🐳 Launching Container...")
	if err := runner.Run(absWorkspace, "devcontainer", "up",
		"--config", relEffectivePath,
		"--workspace-folder", ".",
	); err != nil {
		fmt.Printf("❌ Container launch failed: %v\n", err)
		return err
	}

	// 5. PROVISIONING
	fmt.Println("⚙️  Provisioning Environment...")
	
	if len(pConfig.AptPackages.Add) > 0 || len(pConfig.AptPackages.Remove) > 0 {
		installAptPackages(runner, pConfig.AptPackages, ".", relEffectivePath, absWorkspace)
	}

	if len(pConfig.Commands) > 0 {
		runCustomCommands(runner, pConfig.Commands, ".", relEffectivePath, absWorkspace)
	}

	// CREATE SYMLINKS
	createSymlinks(runner, pConfig, pConfig.Checks.ExpectedHomeDir, ".", relEffectivePath, absWorkspace)

	// 6. CLEANUP (Zero Trace)
	fmt.Println("🧹 Cleaning up temporary config...")
	_ = os.Remove(effectivePath)

	fmt.Println("✅ DONE!")
	return nil
}

// --- CORE LOGIC ---

func applySettingsChanges(base map[string]interface{}, pConfig PersonalConfig) {
	// Initialize map structure if missing
	customizations, _ := base["customizations"].(map[string]interface{})
	if customizations == nil {
		customizations = make(map[string]interface{})
		base["customizations"] = customizations
	}
	vscode, _ := customizations["vscode"].(map[string]interface{})
	if vscode == nil {
		vscode = make(map[string]interface{})
		customizations["vscode"] = vscode
	}

	settings, _ := vscode["settings"].(map[string]interface{})
	if settings == nil {
		settings = make(map[string]interface{})
		vscode["settings"] = settings
	}

	// Apply removals
	for _, key := range pConfig.Settings.Remove {
		delete(settings, key)
	}

	// Apply adds/overrides
	for key, val := range pConfig.Settings.Add {
		settings[key] = val
	}
}

func createSymlinks(runner CommandRunner, pConfig PersonalConfig, homeDir string, workspace string, config string, cwd string) {
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
		stderr, err := runner.RunOutput(cwd, "devcontainer", "exec", "--config", config, "--workspace-folder", workspace, "bash", "-c", script)

		if err != nil {
			if strings.Contains(stderr, "Read-only file system") {
				fmt.Printf("⚠️  Warning: Could not create symlink %s (Read-only file system). Skipping.\n", link.Target)
			} else {
				fmt.Printf("❌ Failed to create symlink %s: %v\n%s\n", link.Target, err, stderr)
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

func checkAssumptions(base map[string]interface{}, pConfig PersonalConfig) error {
	remoteUser, ok := base["remoteUser"].(string)
	if !ok { return nil } // If not defined, we can't check.
	
	if remoteUser != pConfig.Checks.ExpectedRemoteUser {
		return fmt.Errorf("❌ Safety Check Failed!\nExpected remoteUser: '%s'\nFound in config:   '%s'\nUpdate personal-config.json or the devcontainer.json to match.", pConfig.Checks.ExpectedRemoteUser, remoteUser)
	}
	return nil
}

func installAptPackages(runner CommandRunner, apt struct {
	Add    []string `json:"add"`
	Remove []string `json:"remove"`
}, workspace string, config string, cwd string) {
	if len(apt.Remove) > 0 {
		fmt.Printf("️ Removing packages: %v\n", apt.Remove)
		pkgList := strings.Join(apt.Remove, " ")
		script := fmt.Sprintf("sudo apt-get remove -y %s", pkgList)
		runner.Run(cwd, "devcontainer", "exec", "--config", config, "--workspace-folder", workspace, "bash", "-c", script)
	}
	if len(apt.Add) > 0 {
		fmt.Printf("📦 Installing packages: %v\n", apt.Add)
		pkgList := strings.Join(apt.Add, " ")
		script := fmt.Sprintf("sudo apt-get update && sudo apt-get install -y %s", pkgList)
		runner.Run(cwd, "devcontainer", "exec", "--config", config, "--workspace-folder", workspace, "bash", "-c", script)
	}
}

func runCustomCommands(runner CommandRunner, commands []string, workspace string, config string, cwd string) {
	fmt.Println("🛠️ Running custom commands...")
	for _, cmd := range commands {
		fmt.Printf("  > %s\n", cmd)
		runner.Run(cwd, "devcontainer", "exec", "--config", config, "--workspace-folder", workspace, "bash", "-c", cmd)
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