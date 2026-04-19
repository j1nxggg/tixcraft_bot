package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	tea "charm.land/bubbletea/v2"
)

type profileCheckDoneMsg struct {
	projectDir string
	profileDir string
	sourceDir  string
	exists     bool
	err        error
}

type profileCopyDoneMsg struct {
	projectDir string
	profileDir string
	err        error
}

func checkProfileCmd() tea.Cmd {
	return func() tea.Msg {
		projectDir, err := detectProjectDir()
		if err != nil {
			return profileCheckDoneMsg{err: err}
		}

		profileDir := filepath.Join(projectDir, "Profile")

		info, statErr := os.Stat(profileDir)
		switch {
		case statErr == nil && info.IsDir():
			return profileCheckDoneMsg{
				projectDir: projectDir,
				profileDir: profileDir,
				exists:     true,
			}

		case statErr == nil && !info.IsDir():
			return profileCheckDoneMsg{
				projectDir: projectDir,
				profileDir: profileDir,
				err:        fmt.Errorf("%s 已存在，但不是資料夾", profileDir),
			}

		case statErr != nil && !errors.Is(statErr, os.ErrNotExist):
			return profileCheckDoneMsg{
				projectDir: projectDir,
				profileDir: profileDir,
				err:        fmt.Errorf("檢查 Profile 失敗: %w", statErr),
			}
		}

		sourceDir, err := chromeUserDataDir()
		if err != nil {
			return profileCheckDoneMsg{
				projectDir: projectDir,
				profileDir: profileDir,
				err:        err,
			}
		}

		return profileCheckDoneMsg{
			projectDir: projectDir,
			profileDir: profileDir,
			sourceDir:  sourceDir,
			exists:     false,
		}
	}
}

func copyProfileCmd(projectDir, sourceDir string) tea.Cmd {
	return func() tea.Msg {
		profileDir := filepath.Join(projectDir, "Profile")
		err := copyChromeProfile(sourceDir, profileDir)

		return profileCopyDoneMsg{
			projectDir: projectDir,
			profileDir: profileDir,
			err:        err,
		}
	}
}

func detectProjectDir() (string, error) {
	cwd, err := os.Getwd()
	if err == nil {
		for dir := cwd; ; dir = filepath.Dir(dir) {
			if pathExists(filepath.Join(dir, "cli", "go.mod")) {
				return dir, nil
			}

			if filepath.Base(dir) == "cli" && pathExists(filepath.Join(dir, "go.mod")) {
				return filepath.Dir(dir), nil
			}

			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
		}
	}

	exePath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("無法取得執行檔路徑: %w", err)
	}

	exeDir := filepath.Dir(exePath)
	return exeDir, nil
}

func chromeUserDataDir() (string, error) {
	localAppData := os.Getenv("LOCALAPPDATA")
	if localAppData == "" {
		return "", errors.New("找不到 LOCALAPPDATA, 無法定位 Chrome User Data")
	}

	return filepath.Join(localAppData, "Google", "Chrome", "User Data"), nil
}

func copyChromeProfile(sourceDir, destDir string) error {
	sourceInfo, err := os.Stat(sourceDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("找不到 Chrome User Data: %s", sourceDir)
		}
		return fmt.Errorf("讀取 Chrome User Data 失敗: %w", err)
	}

	if !sourceInfo.IsDir() {
		return fmt.Errorf("Chrome User Data 路徑不是資料夾: %s", sourceDir)
	}

	if err := killChromeProcesses(); err != nil {
		return err
	}

	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return fmt.Errorf("建立 Profile 資料夾失敗: %w", err)
	}

	if err := runRobocopy(sourceDir, destDir); err != nil {
		return fmt.Errorf("複製 Chrome 資料失敗: %w", err)
	}

	return nil
}

func killChromeProcesses() error {
	cmd := exec.Command(
		"powershell",
		"-NoProfile",
		"-NonInteractive",
		"-Command",
		"Get-Process chrome -ErrorAction SilentlyContinue | Stop-Process -Force",
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("強制關閉 Chrome 失敗: %v %s", err, string(output))
	}

	time.Sleep(500 * time.Millisecond)
	return nil
}

func runRobocopy(sourceDir, destDir string) error {
	cmd := exec.Command(
		"robocopy",
		sourceDir,
		destDir,
		"/E",
		"/XJ",
		"/R:1",
		"/W:1",
		"/MT:16",
	)

	output, err := cmd.CombinedOutput()
	if err == nil {
		return nil
	}

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		return fmt.Errorf("robocopy 執行失敗: %w\n%s", err, string(output))
	}

	exitCode := exitErr.ExitCode()
	if exitCode < 8 {
		return nil
	}

	return fmt.Errorf("robocopy exit code %d\n%s", exitCode, string(output))
}

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
