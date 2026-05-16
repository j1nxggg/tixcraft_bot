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
	summary    string
	exists     bool
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
	SourceDir             string `json:"source_dir,omitempty"`
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

const defaultChromeProfileDir = "Default"

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

		if err := initializeDedicatedProfile(profileDir); err != nil {
			return profileCheckDoneMsg{
				projectDir: projectDir,
				profileDir: profileDir,
				err:        err,
			}
		}

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

func initializeDedicatedProfile(profileDir string) error {
	if err := os.MkdirAll(profileDir, 0o755); err != nil {
		return fmt.Errorf("建立 Profile 資料夾失敗: %w", err)
	}

	if err := writeProfileMetadata(profileDir); err != nil {
		return err
	}

	return nil
}

func resetChromeProfile(projectDir string) error {
	profileDir := filepath.Join(projectDir, "Profile")

	if err := killChromeProcesses(); err != nil {
		return err
	}

	// metadata / CDP sentinel 都已放進 Profile/ 裡(.bot-meta.json / .bot-cdp.json),
	// RemoveAll 一次帶走所有狀態
	if err := os.RemoveAll(profileDir); err != nil {
		return fmt.Errorf("刪除 Profile 資料夾失敗: %w", err)
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

	// 先送 graceful close (WM_CLOSE) 給 Chrome,讓它有機會正常存資料、寫 cookie
	// 等最多 gracefulTimeout 秒,仍在跑才用 /F 強殺
	if err := gracefullyCloseChrome(); err == nil {
		if waitChromeExit(gracefulCloseTimeout) {
			time.Sleep(300 * time.Millisecond)
			return nil
		}
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

const gracefulCloseTimeout = 5 * time.Second

// gracefullyCloseChrome 用不帶 /F 的 taskkill 送 WM_CLOSE 給 Chrome 主視窗,
// 等同使用者自己點 Chrome 右上角叉叉,讓 Chrome 正常關閉
func gracefullyCloseChrome() error {
	cmd := exec.Command("taskkill", "/IM", "chrome.exe", "/T")
	output, err := cmd.CombinedOutput()
	if err != nil {
		// exit code 128 = 找不到行程,視為已經沒在跑
		if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ExitCode() == 128 {
			return nil
		}
		return fmt.Errorf("送出 graceful close 失敗: %v %s", err, string(output))
	}
	return nil
}

// waitChromeExit 每 250ms 檢查一次,直到 Chrome 完全退出或逾時
func waitChromeExit(timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		running, err := chromeProcessesRunning()
		if err == nil && !running {
			return true
		}
		time.Sleep(250 * time.Millisecond)
	}
	return false
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

func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// profileMetaPath 回傳 Profile 附加 metadata 檔的完整路徑。
// 放在 Profile/ 裡面讓 RemoveAll(Profile) 能一次清乾淨,
// 同時不污染專案根目錄。Chrome 只認固定檔名,看到 .bot-meta.json 會直接忽略。
func profileMetaPath(projectDir string) string {
	return filepath.Join(projectDir, "Profile", ".bot-meta.json")
}

func writeProfileMetadata(profileDir string) error {
	meta := profileMetadata{
		InitializedAt: time.Now().Format(time.RFC3339),
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
		"專案目錄內已存在專用 Chrome Profile：",
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
		"這份 Profile 由 Chrome 自行建立,不會複製你的日常 Chrome 資料。",
		"每次登入仍會由你在瀏覽器中手動完成;如需清乾淨可按 r 重置 Profile。",
	)
	return strings.Join(lines, "\n")
}

func loadChromeProfileChoices(profileDir string) ([]chromeProfileChoice, error) {
	localStatePath := filepath.Join(profileDir, "Local State")
	data, err := os.ReadFile(localStatePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return defaultChromeProfileChoices(), nil
		}
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
		return defaultChromeProfileChoices(), nil
	}

	return choices, nil
}

func defaultChromeProfileChoices() []chromeProfileChoice {
	return []chromeProfileChoice{
		{
			Basename: defaultChromeProfileDir,
			Label:    "Default (專用 Profile)",
		},
	}
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
