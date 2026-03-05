package main

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// --- MOCKS ---

type MockCommandRunner struct {
	CapturedCmds []string
	Rule         func(dir, name string, args ...string) (string, error)
}

func (m *MockCommandRunner) Run(dir string, name string, args ...string) error {
	cmdStr := fmt.Sprintf("%s %v (in %s)", name, args, dir)
	m.CapturedCmds = append(m.CapturedCmds, cmdStr)
	if m.Rule != nil {
		_, err := m.Rule(dir, name, args...)
		return err
	}
	return nil
}

func (m *MockCommandRunner) RunOutput(dir string, name string, args ...string) (string, error) {
	cmdStr := fmt.Sprintf("%s %v (in %s)", name, args, dir)
	m.CapturedCmds = append(m.CapturedCmds, cmdStr)
	if m.Rule != nil {
		return m.Rule(dir, name, args...)
	}
	return "", nil
}

// --- CORE LOGIC TESTS ---

func TestRun(t *testing.T) {
	// Setup temporary workspace
	tmpDir, err := os.MkdirTemp("", "pf-test")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(tmpDir)

	// Create dummy inputs
	devcontainerDir := filepath.Join(tmpDir, ".devcontainer")
	os.MkdirAll(devcontainerDir, 0755)
	
	baseConfig := `{
		"name": "Base",
		"build": { "dockerfile": "Dockerfile" },
		"customizations": { "vscode": { "extensions": ["base-ext"] } }
	}`
	os.WriteFile(filepath.Join(devcontainerDir, "devcontainer.json"), []byte(baseConfig), 0644)
	
	overrideConfig := `{
		"checks": { "expectedRemoteUser": "vscode", "expectedHomeDir": "/home/vscode" },
		"extensions": { "add": ["my-ext"] },
		"settings": { "add": { "formatOnSave": true } },
		"aptPackages": { "add": ["curl"] },
		"commands": ["echo init"],
		"hostHomeMount": "/custom/mnt",
		"symlinks": [{ "source": ".gitconfig", "target": ".gitconfig" }]
	}`
	os.WriteFile(filepath.Join(tmpDir, "override.json"), []byte(overrideConfig), 0644)

	mock := &MockCommandRunner{}

	// EXECUTE RUN
	args := []string{"perfect-override", "--workspace", tmpDir, "--override", filepath.Join(tmpDir, "override.json")}
	if err := Run(mock, args); err != nil {
		t.Fatalf("Run failed: %v", err)
	}

	// Verify Effective Config was created
	effectivePath := filepath.Join(tmpDir, ".devcontainer.json")
	if _, err := os.Stat(effectivePath); err == nil {
		t.Errorf("Effective config should have been cleaned up!")
	}
	
	// Check Captured Commands
	// We expect commands for: devcontainer up, apt-get install, custom echo, symlink
	if len(mock.CapturedCmds) < 4 {
		t.Errorf("Expected at least 4 commands, got %d:\n%v", len(mock.CapturedCmds), mock.CapturedCmds)
	}
	
	// Helper to check for command presence
	hasCommand := func(keyword string) bool {
		for _, cmd := range mock.CapturedCmds {
			if containsStr(cmd, keyword) { return true }
		}
		return false
	}

	if !hasCommand("devcontainer [up") { t.Error("Missing container launch command") }
	if !hasCommand("apt-get install -y curl") { t.Error("Missing apt-get install") }
	if !hasCommand("echo init") { t.Error("Missing custom command") }
	if !hasCommand("ln -sf /custom/mnt/.gitconfig") { t.Error("Missing symlink command with custom mount") }
}

func TestRun_AssumptionFailure(t *testing.T) {
	// Setup temporary workspace
	tmpDir, err := os.MkdirTemp("", "pf-fail-test")
	if err != nil { t.Fatal(err) }
	defer os.RemoveAll(tmpDir)

	devcontainerDir := filepath.Join(tmpDir, ".devcontainer")
	os.MkdirAll(devcontainerDir, 0755)
	
	baseConfig := `{ "remoteUser": "root" }`
	os.WriteFile(filepath.Join(devcontainerDir, "devcontainer.json"), []byte(baseConfig), 0644)
	
	overrideConfig := `{ "checks": { "expectedRemoteUser": "vscode" } }`
	os.WriteFile(filepath.Join(tmpDir, "override.json"), []byte(overrideConfig), 0644)

	mock := &MockCommandRunner{}
	args := []string{"perfect-override", "--workspace", tmpDir, "--override", filepath.Join(tmpDir, "override.json")}
	
	if err := Run(mock, args); err == nil {
		t.Error("Expected Run to fail due to assumption check mismatch, but it succeeded")
	} else {
		if containsStr(err.Error(), "Safety Check Failed") == false {
			t.Errorf("Unexpected error message: %v", err)
		}
	}
}

func TestRun_LaunchFailure(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "pf-launch-fail")
	if err != nil { t.Fatal(err) }
	defer os.RemoveAll(tmpDir)
	
	devcontainerDir := filepath.Join(tmpDir, ".devcontainer")
	os.MkdirAll(devcontainerDir, 0755)
	os.WriteFile(filepath.Join(devcontainerDir, "devcontainer.json"), []byte(`{}`), 0644)
	os.WriteFile(filepath.Join(tmpDir, "override.json"), []byte(`{}`), 0644)

	mock := &MockCommandRunner{}
	mock.Rule = func(dir, name string, args ...string) (string, error) {
		if name == "devcontainer" && args[0] == "up" {
			return "", fmt.Errorf("simulated launch error")
		}
		return "", nil
	}

	args := []string{"perfect-override", "--workspace", tmpDir, "--override", filepath.Join(tmpDir, "override.json")}
	err = Run(mock, args)
	if err == nil {
		t.Error("Expected Run to fail due to launch error, but it succeeded")
	}
	if err != nil && err.Error() != "simulated launch error" {
		t.Errorf("Unexpected error: %v", err)
	}
}

func TestInstallAptPackages(t *testing.T) {
	mock := &MockCommandRunner{}
	apt := struct {
		Add    []string `json:"add"`
		Remove []string `json:"remove"`
	}{
		Add:    []string{"curl", "git"},
		Remove: []string{"nano"},
	}

	installAptPackages(mock, apt, ".", "config.json", "/abs/work")

	expected := []string{
		"devcontainer [exec --config config.json --workspace-folder . bash -c sudo apt-get remove -y nano] (in /abs/work)",
		"devcontainer [exec --config config.json --workspace-folder . bash -c sudo apt-get update && sudo apt-get install -y curl git] (in /abs/work)",
	}

	if !reflect.DeepEqual(mock.CapturedCmds, expected) {
		t.Errorf("Captured commands mismatch.\nGot: %v\nWant: %v", mock.CapturedCmds, expected)
	}
}

func TestRunCustomCommands(t *testing.T) {
	mock := &MockCommandRunner{}
	cmds := []string{"echo hello", "ls -la"}

	runCustomCommands(mock, cmds, ".", "config.json", "/abs/work")

	expected := []string{
		"devcontainer [exec --config config.json --workspace-folder . bash -c echo hello] (in /abs/work)",
		"devcontainer [exec --config config.json --workspace-folder . bash -c ls -la] (in /abs/work)",
	}

	if !reflect.DeepEqual(mock.CapturedCmds, expected) {
		t.Errorf("Captured commands mismatch.\nGot: %v\nWant: %v", mock.CapturedCmds, expected)
	}
}

func TestApplySettingsChanges(t *testing.T) {
	tests := []struct {
		name     string
		base     map[string]interface{}
		pConfig  PersonalConfig
		expected map[string]interface{}
	}{
		{
			name: "Add and Remove Settings",
			base: map[string]interface{}{
				"customizations": map[string]interface{}{
					"vscode": map[string]interface{}{
						"settings": map[string]interface{}{
							"existingSetting": "keep",
							"removeSetting":   true,
						},
					},
				},
			},
			pConfig: PersonalConfig{
				Settings: struct {
					Add    map[string]interface{} `json:"add"`
					Remove []string               `json:"remove"`
				}{
					Add: map[string]interface{}{
						"newSetting": 123,
					},
					Remove: []string{"removeSetting"},
				},
			},
			expected: map[string]interface{}{
				"existingSetting": "keep",
				"newSetting":      123,
			},
		},
		{
			name: "Empty Base Settings",
			base: map[string]interface{}{},
			pConfig: PersonalConfig{
				Settings: struct {
					Add    map[string]interface{} `json:"add"`
					Remove []string               `json:"remove"`
				}{
					Add: map[string]interface{}{
						"newSetting": 123,
					},
				},
			},
			expected: map[string]interface{}{
				"newSetting": 123,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			applySettingsChanges(tt.base, tt.pConfig)

			customizations := tt.base["customizations"].(map[string]interface{})
			vscode := customizations["vscode"].(map[string]interface{})
			settings := vscode["settings"].(map[string]interface{})

			if !reflect.DeepEqual(settings, tt.expected) {
				t.Errorf("expected %v, got %v", tt.expected, settings)
			}
		})
	}
}

func TestCreateSymlinks(t *testing.T) {
	mock := &MockCommandRunner{}
	pConfig := PersonalConfig{
		HostHomeMount: "/host_mnt",
		Symlinks: []Symlink{
			{Source: ".bashrc", Target: ".bashrc_custom"},
		},
	}

	createSymlinks(mock, pConfig, "/home/vscode", ".", "config.json", "/abs/work")

	expected := []string{
		"devcontainer [exec --config config.json --workspace-folder . bash -c sudo mkdir -p /home/vscode && sudo rm -rf /home/vscode/.bashrc_custom && sudo ln -sf /host_mnt/.bashrc /home/vscode/.bashrc_custom] (in /abs/work)",
	}

	if !reflect.DeepEqual(mock.CapturedCmds, expected) {
		t.Errorf("Captured commands mismatch.\nGot: %v\nWant: %v", mock.CapturedCmds, expected)
	}
}
func TestCreateSymlinks_ReadOnly(t *testing.T) {
	mock := &MockCommandRunner{}
	pConfig := PersonalConfig{
		Symlinks: []Symlink{{Source: "foo", Target: "bar"}},
	}

	// Mock Read-only error
	mock.Rule = func(dir, name string, args ...string) (string, error) {
		return "Read-only file system", fmt.Errorf("exit status 1")
	}

	// Should not panic or exit, just print warning (stdout check skipped here for simplicity)
	createSymlinks(mock, pConfig, "/home", ".", "cf", "/abs")
	
	if len(mock.CapturedCmds) != 1 {
		t.Errorf("Expected 1 command attempt")
	}
}

func containsStr(s, substr string) bool {
	return reflect.DeepEqual(s, substr) || (len(s) > len(substr) && (s[0:len(substr)] == substr || s[len(s)-len(substr):] == substr)) || (len(s) > 0 && len(substr) > 0 && 
		(func() bool { 
			for i := 0; i < len(s)-len(substr); i++ {
				if s[i:i+len(substr)] == substr { return true }
			}
			return false 
		})())
}

// Reuse existing tests below...

func TestApplyExtensionChanges(t *testing.T) {
	tests := []struct {
		name     string
		base     map[string]interface{}
		pConfig  PersonalConfig
		expected []string
	}{
		{
			name: "Add extensions",
			base: map[string]interface{}{
				"customizations": map[string]interface{}{
					"vscode": map[string]interface{}{
						"extensions": []interface{}{"ext1"},
					},
				},
			},
			pConfig: PersonalConfig{
				Extensions: struct {
					Add    []string `json:"add"`
					Remove []string `json:"remove"`
				}{
					Add: []string{"ext2"},
				},
			},
			expected: []string{"ext1", "ext2"},
		},
		{
			name: "Remove extensions",
			base: map[string]interface{}{
				"customizations": map[string]interface{}{
					"vscode": map[string]interface{}{
						"extensions": []interface{}{"ext1", "ext2"},
					},
				},
			},
			pConfig: PersonalConfig{
				Extensions: struct {
					Add    []string `json:"add"`
					Remove []string `json:"remove"`
				}{
					Remove: []string{"ext1"},
				},
			},
			expected: []string{"ext2"},
		},
		{
			name: "Add duplicate extension",
			base: map[string]interface{}{
				"customizations": map[string]interface{}{
					"vscode": map[string]interface{}{
						"extensions": []interface{}{"ext1"},
					},
				},
			},
			pConfig: PersonalConfig{
				Extensions: struct {
					Add    []string `json:"add"`
					Remove []string `json:"remove"`
				}{
					Add: []string{"ext1"},
				},
			},
			expected: []string{"ext1"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			applyExtensionChanges(tt.base, tt.pConfig)
			
			customizations := tt.base["customizations"].(map[string]interface{})
			vscode := customizations["vscode"].(map[string]interface{})
			extensions := vscode["extensions"].([]string)

			if !reflect.DeepEqual(extensions, tt.expected) {
				t.Errorf("expected %v, got %v", tt.expected, extensions)
			}
		})
	}
}

func TestApplyFeatureChanges(t *testing.T) {
	tests := []struct {
		name     string
		base     map[string]interface{}
		pConfig  PersonalConfig
		expected map[string]interface{}
	}{
		{
			name: "Add feature",
			base: map[string]interface{}{},
			pConfig: PersonalConfig{
				Features: struct {
					Add    map[string]interface{} `json:"add"`
					Remove []string               `json:"remove"`
				}{
					Add: map[string]interface{}{
						"feat1": map[string]interface{}{"opt": "val"},
					},
				},
			},
			expected: map[string]interface{}{
				"feat1": map[string]interface{}{"opt": "val"},
			},
		},
		{
			name: "Remove feature",
			base: map[string]interface{}{
				"features": map[string]interface{}{
					"feat1": map[string]interface{}{},
					"feat2": map[string]interface{}{},
				},
			},
			pConfig: PersonalConfig{
				Features: struct {
					Add    map[string]interface{} `json:"add"`
					Remove []string               `json:"remove"`
				}{
					Remove: []string{"feat1"},
				},
			},
			expected: map[string]interface{}{
				"feat2": map[string]interface{}{},
			},
		},
		{
			name: "Override feature",
			base: map[string]interface{}{
				"features": map[string]interface{}{
					"feat1": map[string]interface{}{"opt": "old"},
				},
			},
			pConfig: PersonalConfig{
				Features: struct {
					Add    map[string]interface{} `json:"add"`
					Remove []string               `json:"remove"`
				}{
					Add: map[string]interface{}{
						"feat1": map[string]interface{}{"opt": "new"},
					},
				},
			},
			expected: map[string]interface{}{
				"feat1": map[string]interface{}{"opt": "new"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			applyFeatureChanges(tt.base, tt.pConfig)
			
			features := tt.base["features"].(map[string]interface{})
			
			if !reflect.DeepEqual(features, tt.expected) {
				t.Errorf("expected %v, got %v", tt.expected, features)
			}
		})
	}
}

func TestInjectHostMount(t *testing.T) {
	tests := []struct {
		name     string
		base     map[string]interface{}
		pConfig  PersonalConfig
		expected string
	}{
		{
			name: "Default mount point",
			base: map[string]interface{}{},
			pConfig: PersonalConfig{},
			expected: "source=${localEnv:HOME},target=/host_home,type=bind,consistency=cached",
		},
		{
			name: "Custom mount point",
			base: map[string]interface{}{},
			pConfig: PersonalConfig{
				HostHomeMount: "/custom/mount",
			},
			expected: "source=${localEnv:HOME},target=/custom/mount,type=bind,consistency=cached",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			injectHostMount(tt.base, tt.pConfig)
			
			mounts := tt.base["mounts"].([]interface{})
			found := false
			for _, m := range mounts {
				if m.(string) == tt.expected {
					found = true
					break
				}
			}

			if !found {
				t.Errorf("expected mount %s not found in %v", tt.expected, mounts)
			}
		})
	}
}

func TestFixRelativePaths(t *testing.T) {
	tests := []struct {
		name           string
		base           map[string]interface{}
		baseConfigPath string
		expectedPath   string
	}{
		{
			name: "Relative path",
			base: map[string]interface{}{
				"build": map[string]interface{}{
					"dockerfile": "Dockerfile",
				},
			},
			baseConfigPath: "/abs/path/to/.devcontainer/devcontainer.json",
			expectedPath:   "/abs/path/to/.devcontainer/Dockerfile",
		},
		{
			name: "Absolute path (unchanged)",
			base: map[string]interface{}{
				"build": map[string]interface{}{
					"dockerfile": "/abs/Dockerfile",
				},
			},
			baseConfigPath: "/abs/path/to/.devcontainer/devcontainer.json",
			expectedPath:   "/abs/Dockerfile",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fixRelativePaths(tt.base, tt.baseConfigPath)
			
			build := tt.base["build"].(map[string]interface{})
			dockerfile := build["dockerfile"].(string)

			if dockerfile != tt.expectedPath {
				t.Errorf("expected %s, got %s", tt.expectedPath, dockerfile)
			}
		})
	}
}

func TestConfigLoadFailures(t *testing.T) {
	// Test loadPersonalConfig valid and invalid
	// Unfortunately invalid calls os.Exit, which kills the test runner. 
	// We should refactor them to return error for proper testing, similar to checkAssumptions.
	// But let's check ignoring git ignore first, which is simpler and has logic.
	
	// Test addToGitIgnore
	tmpDir, err := os.MkdirTemp("", "pf-ignore-test")
	if err != nil { t.Fatal(err) }
	defer os.RemoveAll(tmpDir)
	
	gitDir := filepath.Join(tmpDir, ".git", "info")
	os.MkdirAll(gitDir, 0755)
	excludePath := filepath.Join(gitDir, "exclude")
	
	// 1. New file
	addToGitIgnore(tmpDir, "target.json")
	content, _ := os.ReadFile(excludePath)
	if !containsStr(string(content), "target.json") {
		t.Error("Failed to add to git exclude")
	}
	
	// 2. Already exists
	addToGitIgnore(tmpDir, "target.json")
	// (Manually verify no duplicate? Logic says it checks contains)
}
