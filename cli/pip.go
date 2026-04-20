package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	tea "charm.land/bubbletea/v2"
)

const (
	getPipURL       = "https://bootstrap.pypa.io/get-pip.py"
	getPipFileName  = "get-pip.py"
	requirementsTxt = "requirements.txt"
	nodriverCDPPath = "Lib\\site-packages\\nodriver\\cdp\\network.py"
)

type pipInstallDoneMsg struct {
	venvDir string
	reqPath string
	err     error
}

type requirementsCheckDoneMsg struct {
	venvDir         string
	reqPath         string
	missingPackages []string
	err             error
}

func checkRequirementsCmd(rootDir, venvDir string) tea.Cmd {
	return func() tea.Msg {
		reqPath := filepath.Join(rootDir, requirementsTxt)

		if _, err := os.Stat(reqPath); err != nil {
			if os.IsNotExist(err) {
				return requirementsCheckDoneMsg{
					venvDir: venvDir,
					reqPath: reqPath,
					err:     fmt.Errorf("找不到 %s", reqPath),
				}
			}
			return requirementsCheckDoneMsg{venvDir: venvDir, reqPath: reqPath, err: err}
		}

		if err := enableSitePackages(venvDir); err != nil {
			return requirementsCheckDoneMsg{venvDir: venvDir, reqPath: reqPath, err: fmt.Errorf("啟用 site-packages 失敗:%w", err)}
		}

		if err := ensurePip(venvDir); err != nil {
			return requirementsCheckDoneMsg{venvDir: venvDir, reqPath: reqPath, err: fmt.Errorf("安裝 pip 失敗:%w", err)}
		}

		missing, err := findMissingPackages(venvDir, reqPath)
		if err != nil {
			return requirementsCheckDoneMsg{venvDir: venvDir, reqPath: reqPath, err: err}
		}

		if len(missing) == 0 {
			if err := patchNodriverNetworkFile(venvDir); err != nil {
				return requirementsCheckDoneMsg{venvDir: venvDir, reqPath: reqPath, err: fmt.Errorf("修補 nodriver 失敗:%w", err)}
			}
		}

		return requirementsCheckDoneMsg{
			venvDir:         venvDir,
			reqPath:         reqPath,
			missingPackages: missing,
		}
	}
}

func installRequirementsCmd(rootDir, venvDir string) tea.Cmd {
	return func() tea.Msg {
		reqPath := filepath.Join(rootDir, requirementsTxt)

		if _, err := os.Stat(reqPath); err != nil {
			if os.IsNotExist(err) {
				return pipInstallDoneMsg{
					venvDir: venvDir,
					reqPath: reqPath,
					err:     fmt.Errorf("找不到 %s", reqPath),
				}
			}
			return pipInstallDoneMsg{venvDir: venvDir, reqPath: reqPath, err: err}
		}

		if err := enableSitePackages(venvDir); err != nil {
			return pipInstallDoneMsg{venvDir: venvDir, reqPath: reqPath, err: fmt.Errorf("啟用 site-packages 失敗:%w", err)}
		}

		if err := ensurePip(venvDir); err != nil {
			return pipInstallDoneMsg{venvDir: venvDir, reqPath: reqPath, err: fmt.Errorf("安裝 pip 失敗:%w", err)}
		}

		if err := runPipInstall(venvDir, reqPath); err != nil {
			return pipInstallDoneMsg{venvDir: venvDir, reqPath: reqPath, err: fmt.Errorf("安裝套件失敗:%w", err)}
		}

		if err := patchNodriverNetworkFile(venvDir); err != nil {
			return pipInstallDoneMsg{venvDir: venvDir, reqPath: reqPath, err: fmt.Errorf("修補 nodriver 失敗:%w", err)}
		}

		return pipInstallDoneMsg{venvDir: venvDir, reqPath: reqPath}
	}
}

// 把 pythonXXX._pth 裡面 "#import site" 的註解拿掉
func enableSitePackages(venvDir string) error {
	pthPath, err := findPthFile(venvDir)
	if err != nil {
		return err
	}

	data, err := os.ReadFile(pthPath)
	if err != nil {
		return err
	}

	// 處理 "#import site" 或 "# import site" 這類變體
	re := regexp.MustCompile(`(?m)^\s*#\s*import\s+site\s*$`)
	if re.Match(data) {
		newData := re.ReplaceAll(data, []byte("import site"))
		return os.WriteFile(pthPath, newData, 0o644)
	}

	// 沒註解行就直接追加一行,確保有效
	if !regexp.MustCompile(`(?m)^import\s+site\s*$`).Match(data) {
		if len(data) > 0 && data[len(data)-1] != '\n' {
			data = append(data, '\n')
		}
		data = append(data, []byte("import site\n")...)
		return os.WriteFile(pthPath, data, 0o644)
	}

	return nil
}

func findPthFile(venvDir string) (string, error) {
	entries, err := os.ReadDir(venvDir)
	if err != nil {
		return "", err
	}

	re := regexp.MustCompile(`^python\d+\._pth$`)
	for _, e := range entries {
		if !e.IsDir() && re.MatchString(e.Name()) {
			return filepath.Join(venvDir, e.Name()), nil
		}
	}
	return "", fmt.Errorf("在 %s 找不到 python*._pth", venvDir)
}

func ensurePip(venvDir string) error {
	python := filepath.Join(venvDir, "python.exe")
	getPipPath := filepath.Join(venvDir, getPipFileName)

	// 檢查 pip 是否已經可用
	if err := exec.Command(python, "-m", "pip", "--version").Run(); err == nil {
		return nil
	}

	if err := downloadFile(getPipURL, getPipPath); err != nil {
		return fmt.Errorf("下載 get-pip.py 失敗:%w", err)
	}
	defer os.Remove(getPipPath)

	cmd := exec.Command(python, getPipPath, "--no-warn-script-location")
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("執行 get-pip.py 失敗:%w\n%s", err, string(output))
	}
	return nil
}

func runPipInstall(venvDir, reqPath string) error {
	python := filepath.Join(venvDir, "python.exe")

	cmd := exec.Command(
		python,
		"-m", "pip", "install",
		"--no-warn-script-location",
		"-r", reqPath,
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w\n%s", err, string(output))
	}
	return nil
}

func findMissingPackages(venvDir, reqPath string) ([]string, error) {
	requirements, err := parseRequirementNames(reqPath)
	if err != nil {
		return nil, err
	}

	python := filepath.Join(venvDir, "python.exe")
	var missing []string

	for _, requirement := range requirements {
		cmd := exec.Command(python, "-m", "pip", "show", requirement)
		if err := cmd.Run(); err == nil {
			continue
		} else if _, ok := err.(*exec.ExitError); ok {
			missing = append(missing, requirement)
			continue
		} else {
			return nil, fmt.Errorf("檢查套件 %s 失敗:%w", requirement, err)
		}
	}

	return missing, nil
}

func parseRequirementNames(reqPath string) ([]string, error) {
	data, err := os.ReadFile(reqPath)
	if err != nil {
		return nil, err
	}

	lines := strings.Split(string(data), "\n")
	nameRe := regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*`)
	seen := make(map[string]struct{})
	var requirements []string

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		if strings.HasPrefix(line, "-") {
			continue
		}

		if idx := strings.Index(line, "#"); idx >= 0 {
			line = strings.TrimSpace(line[:idx])
		}
		if line == "" {
			continue
		}

		name := nameRe.FindString(line)
		if name == "" {
			return nil, fmt.Errorf("無法解析 requirements.txt 內容: %s", line)
		}

		normalized := strings.ToLower(name)
		if _, ok := seen[normalized]; ok {
			continue
		}

		seen[normalized] = struct{}{}
		requirements = append(requirements, name)
	}

	return requirements, nil
}

func patchNodriverNetworkFile(venvDir string) error {
	networkPath := filepath.Join(venvDir, nodriverCDPPath)

	data, err := os.ReadFile(networkPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("找不到 %s", networkPath)
		}
		return err
	}

	cleaned := bytes.ReplaceAll(data, []byte{0xB1}, nil)
	cleaned = bytes.ReplaceAll(cleaned, []byte("\uFFFD"), nil)
	if bytes.Equal(cleaned, data) {
		return nil
	}

	return os.WriteFile(networkPath, cleaned, 0o644)
}
