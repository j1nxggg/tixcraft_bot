package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

type profileCheckDoneMsg struct {
	projectDir string
	profileDir string
	sourceDir  string
	summary    string
	exists     bool
	err        error
}

type profileCopyDoneMsg struct {
	projectDir string
	profileDir string
	err        error
}

type profileResetDoneMsg struct {
	projectDir string
	profileDir string
	err        error
}

type chromeProfileChoice struct {
	Basename string
	Label    string
	GaiaName string
}

type profileMetadata struct {
	InitializedAt         string `json:"initialized_at"`
	SourceDir             string `json:"source_dir"`
	FirstLoginCompletedAt string `json:"first_login_completed_at,omitempty"`
}

type profileDisplayInfo struct {
	TimeLabel string
	TimeValue string
	SourceDir string
}

type chromeLocalState struct {
	Profile struct {
		LastUsed      string                           `json:"last_used"`
		ProfilesOrder []string                         `json:"profiles_order"`
		InfoCache     map[string]chromeLocalStateEntry `json:"info_cache"`
	} `json:"profile"`
}

type chromeLocalStateEntry struct {
	GaiaName string `json:"gaia_name"`
	Name     string `json:"name"`
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
			displayInfo, err := loadProfileDisplayInfo(projectDir, profileDir)
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
				summary:    buildExistingProfileSummary(profileDir, displayInfo),
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

func resetProfileCmd(projectDir string) tea.Cmd {
	return func() tea.Msg {
		profileDir := filepath.Join(projectDir, "Profile")
		err := resetChromeProfile(projectDir)
		return profileResetDoneMsg{
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

	if err := writeProfileMetadata(destDir, sourceDir); err != nil {
		return err
	}

	return nil
}

func resetChromeProfile(projectDir string) error {
	profileDir := filepath.Join(projectDir, "Profile")
	metaPath := profileMetaPath(projectDir)

	if err := killChromeProcesses(); err != nil {
		return err
	}

	if err := os.RemoveAll(profileDir); err != nil {
		return fmt.Errorf("刪除 Profile 資料夾失敗: %w", err)
	}

	if err := os.Remove(metaPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("刪除 Profile metadata 失敗: %w", err)
	}

	return nil
}

func killChromeProcesses() error {
	running, err := chromeProcessesRunning()
	if err != nil {
		return err
	}
	if !running {
		return nil
	}

	cmd := exec.Command("taskkill", "/F", "/IM", "chrome.exe", "/T")
	output, err := cmd.CombinedOutput()
	if err != nil {
		stillRunning, checkErr := chromeProcessesRunning()
		if checkErr == nil && !stillRunning {
			time.Sleep(500 * time.Millisecond)
			return nil
		}
		if checkErr != nil {
			return fmt.Errorf("強制關閉 Chrome 失敗: %v %s (後續檢查也失敗: %v)", err, string(output), checkErr)
		}
		return fmt.Errorf("強制關閉 Chrome 失敗: %v %s", err, string(output))
	}

	time.Sleep(500 * time.Millisecond)
	return nil
}

func chromeProcessesRunning() (bool, error) {
	cmd := exec.Command(
		"tasklist",
		"/FI",
		"IMAGENAME eq chrome.exe",
		"/FO",
		"CSV",
		"/NH",
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return false, fmt.Errorf("檢查 Chrome 行程失敗: %v %s", err, string(output))
	}

	out := strings.TrimSpace(string(output))
	if out == "" || strings.Contains(out, "No tasks are running") || strings.Contains(out, "沒有執行中的工作") {
		return false, nil
	}

	return strings.Contains(strings.ToLower(out), "chrome.exe"), nil
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

func profileMetaPath(projectDir string) string {
	return filepath.Join(projectDir, ".profile-meta.json")
}

func writeProfileMetadata(profileDir, sourceDir string) error {
	meta := profileMetadata{
		InitializedAt: time.Now().Format(time.RFC3339),
		SourceDir:     sourceDir,
	}

	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return fmt.Errorf("建立 Profile metadata 失敗: %w", err)
	}

	metaPath := profileMetaPath(filepath.Dir(profileDir))
	if err := os.WriteFile(metaPath, data, 0o644); err != nil {
		return fmt.Errorf("寫入 Profile metadata 失敗: %w", err)
	}

	return nil
}

func loadProfileDisplayInfo(projectDir, profileDir string) (profileDisplayInfo, error) {
	metaPath := profileMetaPath(projectDir)
	metaData, err := os.ReadFile(metaPath)
	if err == nil {
		var meta profileMetadata
		if json.Unmarshal(metaData, &meta) == nil {
			if initializedAt, parseErr := time.Parse(time.RFC3339, meta.InitializedAt); parseErr == nil {
				return profileDisplayInfo{
					TimeLabel: "初始化時間",
					TimeValue: initializedAt.Local().Format("2006/01/02 15:04:05"),
					SourceDir: strings.TrimSpace(meta.SourceDir),
				}, nil
			}
		}
	}

	info, statErr := os.Stat(profileDir)
	if statErr != nil {
		return profileDisplayInfo{}, fmt.Errorf("讀取 Profile 資料夾資訊失敗: %w", statErr)
	}

	return profileDisplayInfo{
		TimeLabel: "最後更新時間（metadata 缺失）",
		TimeValue: info.ModTime().Format("2006/01/02 15:04:05"),
	}, nil
}

func buildExistingProfileSummary(profileDir string, displayInfo profileDisplayInfo) string {
	lines := []string{
		"專案目錄內已存在獨立 Chrome 副本：",
		profileDir,
		"",
		fmt.Sprintf("%s：%s", displayInfo.TimeLabel, displayInfo.TimeValue),
	}

	if displayInfo.SourceDir != "" {
		lines = append(lines, fmt.Sprintf("初始化來源：%s", displayInfo.SourceDir))
	}

	lines = append(
		lines,
		"",
		"這份 Profile 為獨立副本，之後將持續沿用。",
		"如果登入失效或想換帳號，可按 r 重置 Profile 後重新建立副本。",
	)
	return strings.Join(lines, "\n")
}

func loadChromeProfileChoices(profileDir string) ([]chromeProfileChoice, error) {
	localStatePath := filepath.Join(profileDir, "Local State")
	data, err := os.ReadFile(localStatePath)
	if err != nil {
		return nil, fmt.Errorf("讀取 Chrome Local State 失敗: %w", err)
	}

	var state chromeLocalState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("解析 Chrome Local State 失敗: %w", err)
	}

	seen := map[string]bool{}
	basenames := make([]string, 0, len(state.Profile.InfoCache))

	for _, basename := range state.Profile.ProfilesOrder {
		if _, ok := state.Profile.InfoCache[basename]; ok {
			basenames = append(basenames, basename)
			seen[basename] = true
		}
	}

	for basename := range state.Profile.InfoCache {
		if seen[basename] {
			continue
		}
		basenames = append(basenames, basename)
	}

	sort.Strings(basenames)
	if slices.Contains(basenames, state.Profile.LastUsed) {
		basenames = moveStringToFront(basenames, state.Profile.LastUsed)
	}

	choices := make([]chromeProfileChoice, 0, len(basenames))
	for _, basename := range basenames {
		entry := state.Profile.InfoCache[basename]
		labelName := strings.TrimSpace(entry.GaiaName)
		if labelName == "" {
			labelName = strings.TrimSpace(entry.Name)
		}
		if labelName == "" {
			labelName = basename
		}

		label := labelName
		if label != basename {
			label = fmt.Sprintf("%s (%s)", labelName, basename)
		}
		if basename == state.Profile.LastUsed {
			label += " [上次使用]"
		}

		choices = append(choices, chromeProfileChoice{
			Basename: basename,
			Label:    label,
			GaiaName: strings.TrimSpace(entry.GaiaName),
		})
	}

	if len(choices) == 0 {
		return nil, errors.New("找不到可用的 Chrome 設定檔")
	}

	return choices, nil
}

func moveStringToFront(items []string, target string) []string {
	result := make([]string, 0, len(items))
	result = append(result, target)

	for _, item := range items {
		if item == target {
			continue
		}
		result = append(result, item)
	}

	return result
}
