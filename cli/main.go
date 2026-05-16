package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"cli/ui"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
)

type appStage int

const (
	stageChecking appStage = iota
	stageProfileMenu
	stageProfileResetConfirm
	stageProfileResetting
	stageConfirm
	stageWarnChromeClose
	stageRunning
	stagePythonCheck
	stagePythonRunning
	stageDepsSyncing
	stageConfigCheck
	stageConfigConfirmOverwrite
	stageConfigForm
	stageConfigReview
	stageConfigSaving
	stageDone
)

type model struct {
	stage             appStage
	quitting          bool
	quitMsg           string
	projectDir        string
	profileDir        string
	rootDir           string
	venvDir           string
	envPath           string
	oldConfig         botConfig
	cfg               *botConfig
	form              *huh.Form
	chromeProfiles    []chromeProfileChoice
	profileMenuDetail string
	statusText        string
	detailText        string
	success           bool
	spinnerFrame      int
}

type spinnerTickMsg struct{}

type botProcessDoneMsg struct {
	err error
}

// 手寫 spinner,避免多引入 bubbles 當 direct dep
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

const spinnerTickInterval = 100 * time.Millisecond

func spinnerTickCmd() tea.Cmd {
	return tea.Tick(spinnerTickInterval, func(time.Time) tea.Msg {
		return spinnerTickMsg{}
	})
}

func initialModel() model {
	return model{
		stage:      stageChecking,
		statusText: "正在檢查專案目錄中的專用 Profile/...",
	}
}

func (m model) Init() tea.Cmd {
	return tea.Batch(checkProfileCmd(), spinnerTickCmd())
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// 表單 stage 時,把所有事件優先交給 form 處理
	if m.stage == stageConfigForm && m.form != nil {
		return m.updateConfigForm(msg)
	}

	switch msg := msg.(type) {
	case profileCheckDoneMsg:
		return m.handleCheckDone(msg)

	case profileResetDoneMsg:
		return m.handleResetDone(msg)

	case pythonCheckDoneMsg:
		return m.handlePythonCheck(msg)

	case pythonSetupDoneMsg:
		return m.handlePythonSetup(msg)

	case uvSyncDoneMsg:
		return m.handleUvSync(msg)

	case spinnerTickMsg:
		m.spinnerFrame = (m.spinnerFrame + 1) % len(spinnerFrames)
		return m, spinnerTickCmd()

	case botProcessDoneMsg:
		return m.handleBotProcessDone(msg)

	case configCheckDoneMsg:
		return m.handleConfigCheck(msg)

	case configSaveDoneMsg:
		return m.handleConfigSave(msg)

	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

// 轉發事件給 huh.Form,form 結束後觸發存檔
func (m model) updateConfigForm(msg tea.Msg) (tea.Model, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyPressMsg); ok {
		if strings.ToLower(keyMsg.String()) == "ctrl+c" {
			m.quitting = true
			m.quitMsg = "已取消。"
			return m, tea.Quit
		}
	}

	newForm, cmd := m.form.Update(msg)
	if f, ok := newForm.(*huh.Form); ok {
		m.form = f
	}

	if m.form.State == huh.StateCompleted {
		m.stage = stageConfigReview
		m.statusText = "請確認以下輸入資料"
		m.detailText = m.renderConfigReview(*m.cfg)
		return m, nil
	}

	if m.form.State == huh.StateAborted {
		m.quitting = true
		m.quitMsg = "已取消填寫。"
		return m, tea.Quit
	}

	return m, cmd
}

func (m model) handleCheckDone(msg profileCheckDoneMsg) (tea.Model, tea.Cmd) {
	m.projectDir = msg.projectDir
	m.profileDir = msg.profileDir

	if msg.err != nil {
		m.stage = stageDone
		m.success = false
		m.statusText = "Profile 檢查失敗"
		m.detailText = msg.err.Error()
		return m, nil
	}

	if msg.exists {
		if err := m.loadChromeProfiles(); err != nil {
			m.stage = stageDone
			m.success = false
			m.statusText = "Chrome 設定檔讀取失敗"
			m.detailText = err.Error()
			return m, nil
		}

		m.profileMenuDetail = msg.summary
		m.stage = stageProfileMenu
		m.statusText = "Profile 已就緒"
		m.detailText = msg.summary
		return m, nil
	}

	m.stage = stageConfirm
	m.statusText = "目前目錄內沒有專用 Profile"
	m.detailText = m.confirmDetail()
	return m, nil
}

func (m model) handleResetDone(msg profileResetDoneMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.stage = stageDone
		m.success = false
		m.statusText = "Profile 重置失敗"
		m.detailText = msg.err.Error()
		return m, nil
	}

	m.stage = stageChecking
	m.statusText = "Profile 已重置,正在重新檢查..."
	m.detailText = fmt.Sprintf("已刪除：\n%s\n%s", msg.profileDir, profileMetaPath(msg.projectDir))
	return m, checkProfileCmd()
}

func (m model) handlePythonCheck(msg pythonCheckDoneMsg) (tea.Model, tea.Cmd) {
	m.rootDir = msg.rootDir
	m.venvDir = msg.venvDir

	if msg.err != nil {
		m.stage = stageDone
		m.success = false
		m.statusText = "Python 檢查失敗"
		m.detailText = msg.err.Error()
		return m, nil
	}

	if msg.exists {
		m.stage = stageDepsSyncing
		m.statusText = "venv/ 內已有 Python 與 uv,正在同步 requirements.txt..."
		m.detailText = fmt.Sprintf("已偵測到 python.exe 與 uv.exe：\n%s", msg.venvDir)
		return m, uvSyncCmd(m.rootDir, m.venvDir)
	}

	m.stage = stagePythonRunning
	m.statusText = "正在下載並解壓 Python 與 uv..."
	m.detailText = fmt.Sprintf("目的地：%s", msg.venvDir)
	return m, setupPythonCmd(msg.rootDir, false)
}

func (m model) handlePythonSetup(msg pythonSetupDoneMsg) (tea.Model, tea.Cmd) {
	m.venvDir = msg.venvDir

	if msg.err != nil {
		m.stage = stageDone
		m.success = false
		m.statusText = "Python / uv 安裝失敗"
		m.detailText = msg.err.Error()
		return m, nil
	}

	m.stage = stageDepsSyncing
	m.statusText = "正在用 uv 同步 Python 套件..."
	m.detailText = fmt.Sprintf("Python 與 uv 已就緒：\n%s", msg.venvDir)
	return m, uvSyncCmd(m.rootDir, m.venvDir)
}

func (m model) handleUvSync(msg uvSyncDoneMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.stage = stageDone
		m.success = false
		m.statusText = "套件同步失敗"
		m.detailText = msg.err.Error()
		return m, nil
	}

	m.stage = stageConfigCheck
	m.statusText = "套件同步完成,正在檢查設定檔..."
	return m, checkConfigCmd(m.rootDir)
}

func (m model) handleConfigCheck(msg configCheckDoneMsg) (tea.Model, tea.Cmd) {
	m.envPath = msg.envPath
	m.oldConfig = msg.oldConfig

	if msg.err != nil {
		m.stage = stageDone
		m.success = false
		m.statusText = "設定檔讀取失敗"
		m.detailText = msg.err.Error()
		return m, nil
	}

	if msg.exists {
		if !m.hasChromeProfileOption(msg.oldConfig.ChromeProfileDir) ||
			!m.hasLoginProviderOption(msg.oldConfig.LoginProvider) ||
			!isValidQuantityOption(msg.oldConfig.Quantity) {
			m.stage = stageConfigForm
			m.statusText = "設定檔缺少必要選項"
			m.detailText = "請先選擇要使用的 Chrome 設定檔、登入方式與票數。"
			return m.enterConfigForm(m.defaultConfig(msg.oldConfig))
		}

		m.stage = stageConfigConfirmOverwrite
		m.statusText = "偵測到既有設定檔"
		m.detailText = fmt.Sprintf(
			"路徑：%s\n\n既有設定：\n  Chrome 設定檔：%s\n  登入方式：%s\n  票券網址：%s\n  票名：%s\n  票價：%s\n  票數：%s\n  場次時間：%s\n  若無票則：%s\n  設定搶票時間：%s\n\n是否要重新填寫? (y 會預設帶入舊值)",
			msg.envPath,
			m.chromeProfileLabel(msg.oldConfig.ChromeProfileDir),
			m.loginProviderLabel(msg.oldConfig.LoginProvider),
			msg.oldConfig.URL,
			msg.oldConfig.TicketName,
			msg.oldConfig.Price,
			msg.oldConfig.Quantity,
			msg.oldConfig.ShowTime,
			msg.oldConfig.FallbackPolicy,
			msg.oldConfig.GrabTime,
		)
		return m, nil
	}

	return m.enterConfigForm(m.defaultConfig(botConfig{}))
}

// 進入表單 stage 把 cfg 初始化 建 form 觸發 form.Init
func (m model) enterConfigForm(initial botConfig) (tea.Model, tea.Cmd) {
	cfg := m.defaultConfig(initial)
	m.cfg = &cfg
	m.form = newConfigForm(m.cfg, m.chromeProfiles)
	m.stage = stageConfigForm

	cmd := m.form.Init()
	return m, cmd
}

func (m model) handleConfigSave(msg configSaveDoneMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.stage = stageDone
		m.success = false
		m.statusText = "設定檔寫入失敗"
		m.detailText = msg.err.Error()
		return m, nil
	}

	saved := msg.cfg
	m.cfg = &saved
	return m.launchBot(fmt.Sprintf("設定已寫入 %s", msg.envPath))
}

func (m model) handleBotProcessDone(msg botProcessDoneMsg) (tea.Model, tea.Cmd) {
	if msg.err == nil {
		m.quitting = true
		m.quitMsg = "Python bot 已結束。"
		return m, tea.Quit
	}

	m.stage = stageDone
	m.success = false
	m.statusText = "Python bot 執行失敗"
	m.detailText = msg.err.Error()
	return m, nil
}

func (m model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	key := strings.ToLower(msg.String())

	if key == "ctrl+c" || key == "q" {
		m.quitting = true
		if m.quitMsg == "" {
			m.quitMsg = "已結束。"
		}
		return m, tea.Quit
	}

	switch m.stage {
	case stageProfileMenu:
		return m.handleProfileMenuKey(key)
	case stageProfileResetConfirm:
		return m.handleProfileResetConfirmKey(key)
	case stageConfirm:
		return m.handleConfirmKey(key)
	case stageWarnChromeClose:
		return m.handleWarnKey(key)
	case stageConfigConfirmOverwrite:
		return m.handleConfigOverwriteKey(key)
	case stageConfigReview:
		return m.handleConfigReviewKey(key)
	}
	return m, nil
}

func (m model) handleProfileMenuKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "c":
		m.stage = stagePythonCheck
		m.statusText = "Profile 已就緒,正在檢查 Python..."
		m.detailText = m.profileMenuDetail
		return m, checkPythonCmd(m.projectDir)
	case "r":
		m.stage = stageProfileResetConfirm
		m.statusText = "即將重置 Profile"
		m.detailText = fmt.Sprintf(
			"這會刪除目前專案內的專用 Chrome Profile，並刪除 %s。\n\n只會刪除 ./Profile 與 metadata，bot/.env 會保留。\n接下來會先關閉 Chrome，再重新回到初始化流程。\n\n刪除目標：%s",
			profileMetaPath(m.projectDir),
			m.profileDir,
		)
		return m, nil
	}
	return m, nil
}

func (m model) handleProfileResetConfirmKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "y":
		m.stage = stageProfileResetting
		m.statusText = "正在關閉 Chrome 並重置 Profile..."
		m.detailText = fmt.Sprintf(
			"刪除：%s\n刪除：%s",
			m.profileDir,
			profileMetaPath(m.projectDir),
		)
		return m, resetProfileCmd(m.projectDir)
	case "n":
		m.stage = stageProfileMenu
		m.statusText = "Profile 已就緒"
		m.detailText = m.profileMenuDetail
		return m, nil
	}
	return m, nil
}

func (m model) handleConfirmKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "y":
		m.stage = stageChecking
		m.statusText = "正在建立專用 Profile..."
		m.detailText = fmt.Sprintf("目的地：%s", m.profileDir)
		return m, checkProfileCmd()
	case "n":
		m.quitting = true
		m.quitMsg = "已取消,不建立專用 Profile。"
		return m, tea.Quit
	}
	return m, nil
}

func (m model) handleWarnKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "y":
		m.stage = stageChecking
		m.statusText = "正在建立專用 Profile..."
		m.detailText = fmt.Sprintf("目的地：%s", m.profileDir)
		return m, checkProfileCmd()
	case "n":
		m.stage = stageConfirm
		m.statusText = "目前目錄內沒有專用 Profile"
		m.detailText = m.confirmDetail()
		return m, nil
	}
	return m, nil
}

func (m model) handleConfigOverwriteKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "y":
		return m.enterConfigForm(m.oldConfig)
	case "n":
		return m.launchBot(fmt.Sprintf("沿用既有設定檔：%s", m.envPath))
	}
	return m, nil
}

func (m model) handleConfigReviewKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "y":
		m.stage = stageConfigSaving
		m.statusText = "正在寫入 .env..."
		m.detailText = m.envPath
		return m, saveConfigCmd(m.envPath, *m.cfg)
	case "n":
		return m.enterConfigForm(*m.cfg)
	}
	return m, nil
}

func (m model) renderConfigReview(cfg botConfig) string {
	return fmt.Sprintf(
		"Chrome 設定檔：%s\n登入方式    ：%s\n票券網址    ：%s\n票名        ：%s\n票價        ：%s\n票數        ：%s\n場次時間    ：%s\n若無票則    ：%s\n設定搶票時間：%s\n\n以上資料是否正確?",
		m.chromeProfileLabel(cfg.ChromeProfileDir),
		m.loginProviderLabel(cfg.LoginProvider),
		cfg.URL,
		cfg.TicketName,
		cfg.Price,
		cfg.Quantity,
		cfg.ShowTime,
		cfg.FallbackPolicy,
		cfg.GrabTime,
	)
}

func (m model) launchBot(reason string) (tea.Model, tea.Cmd) {
	python := filepath.Join(m.venvDir, "python.exe")
	script := filepath.Join(m.rootDir, botDirName, "main.py")

	if err := patchNodriverNetworkFile(m.venvDir); err != nil {
		m.stage = stageDone
		m.success = false
		m.statusText = "啟動前修補 nodriver network.py 失敗"
		m.detailText = err.Error()
		return m, nil
	}
	if err := patchNodriverConnectionFile(m.venvDir); err != nil {
		m.stage = stageDone
		m.success = false
		m.statusText = "啟動前修補 nodriver connection.py 失敗"
		m.detailText = err.Error()
		return m, nil
	}

	m.statusText = "正在執行 Python bot..."
	m.detailText = fmt.Sprintf("%s\n即將啟動：%s", reason, script)

	cmd := exec.Command(python, "-u", script)
	cmd.Dir = m.rootDir

	return m, tea.ExecProcess(cmd, func(err error) tea.Msg {
		return botProcessDoneMsg{err: err}
	})
}

func (m model) confirmDetail() string {
	return fmt.Sprintf(
		"是否建立專用 Chrome Profile?\n\n專案目錄：%s\n目的地：%s\n\n不會複製你的日常 Chrome 資料。",
		m.projectDir,
		m.profileDir,
	)
}

func (m *model) loadChromeProfiles() error {
	choices, err := loadChromeProfileChoices(m.profileDir)
	if err != nil {
		return err
	}

	m.chromeProfiles = choices
	return nil
}

func (m model) hasChromeProfileOption(basename string) bool {
	for _, profile := range m.chromeProfiles {
		if profile.Basename == basename {
			return true
		}
	}

	return false
}

func (m model) defaultConfig(cfg botConfig) botConfig {
	if cfg.ChromeProfileDir == "" || !m.hasChromeProfileOption(cfg.ChromeProfileDir) {
		if len(m.chromeProfiles) > 0 {
			cfg.ChromeProfileDir = m.chromeProfiles[0].Basename
		}
	}
	if !m.hasLoginProviderOption(cfg.LoginProvider) {
		cfg.LoginProvider = loginProviderOptions[0]
	}
	if !isValidQuantityOption(cfg.Quantity) {
		cfg.Quantity = quantityOptions[0]
	}

	return cfg
}

func (m model) hasLoginProviderOption(provider string) bool {
	for _, option := range loginProviderOptions {
		if option == strings.ToLower(strings.TrimSpace(provider)) {
			return true
		}
	}

	return false
}

func (m model) chromeProfileLabel(basename string) string {
	for _, profile := range m.chromeProfiles {
		if profile.Basename == basename {
			return profile.Label
		}
	}

	if basename == "" {
		return "未設定"
	}

	return basename
}

func (m model) loginProviderLabel(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "google":
		return "Google"
	case "facebook":
		return "Facebook"
	case "":
		return "未設定"
	default:
		return provider
	}
}

func (m model) View() tea.View {
	if m.quitting {
		return tea.NewView(m.quitMsg + "\n")
	}

	// 表單 stage 讓 form 自己渲染,外面不要包其他 UI 避免干擾
	if m.stage == stageConfigForm && m.form != nil {
		return tea.NewView(m.form.View())
	}

	title := ui.TitleStyle.Render("Tixcraft Bot Setup")
	box := ui.BoxStyle.Render(m.renderContent())
	help := ui.HelpStyle.Render(m.helpText())

	return tea.NewView(title + "\n" + box + "\n" + help)
}

func (m model) renderContent() string {
	status := m.statusText
	switch {
	case m.stage == stageDone && m.success:
		status = ui.SuccessStyle.Render("成功") + " " + status
	case m.stage == stageDone && !m.success:
		status = ui.ErrorStyle.Render("失敗") + " " + status
	case m.stage == stageProfileMenu,
		m.stage == stageProfileResetConfirm,
		m.stage == stageConfirm,
		m.stage == stageWarnChromeClose,
		m.stage == stageConfigConfirmOverwrite,
		m.stage == stageConfigReview:
		status = ui.PromptStyle.Render("需要確認") + " " + status
	case m.stage == stageProfileResetting,
		m.stage == stageRunning,
		m.stage == stagePythonCheck,
		m.stage == stagePythonRunning,
		m.stage == stageDepsSyncing,
		m.stage == stageConfigCheck,
		m.stage == stageConfigSaving:
		spinner := spinnerFrames[m.spinnerFrame%len(spinnerFrames)]
		status = ui.PromptStyle.Render(spinner+" 處理中") + " " + status
	}

	detail := m.detailText
	if progress := downloadProgressText(); progress != "" {
		if detail == "" {
			detail = progress
		} else {
			detail = detail + "\n\n" + progress
		}
	}

	if detail == "" {
		return status
	}
	return status + "\n\n" + detail
}

func (m model) helpText() string {
	switch m.stage {
	case stageProfileMenu:
		return "[ c ] 繼續  [ r ] 重置 Profile  [ q ] 離開"
	case stageProfileResetConfirm:
		return "[ y ] 重置並重新初始化  [ n ] 返回  [ q ] 離開"
	case stageConfirm:
		return "[ y ] 建立 Profile  [ n ] 取消  [ q ] 離開"
	case stageWarnChromeClose:
		return "[ y ] 已了解並繼續  [ n ] 返回上一步  [ q ] 離開"
	case stageConfigConfirmOverwrite:
		return "[ y ] 重新填寫  [ n ] 沿用現有  [ q ] 離開"
	case stageConfigReview:
		return "[ y ] 正確,儲存並啟動  [ n ] 重新填寫  [ q ] 離開"
	default:
		return "[ q ] 離開"
	}
}

func main() {
	p := tea.NewProgram(initialModel())

	if _, err := p.Run(); err != nil {
		fmt.Println("程式執行失敗:", err)
		os.Exit(1)
	}
}
