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
	stagePipInstalling
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
	sourceDir         string
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
	pipNoticeShown    bool
}

type pipInstallSlowMsg struct{}

func initialModel() model {
	return model{
		stage:      stageChecking,
		statusText: "正在檢查專案目錄中的 Profile/...",
	}
}

func (m model) Init() tea.Cmd {
	return checkProfileCmd()
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// 表單 stage 時,把所有事件優先交給 form 處理
	if m.stage == stageConfigForm && m.form != nil {
		return m.updateConfigForm(msg)
	}

	switch msg := msg.(type) {
	case profileCheckDoneMsg:
		return m.handleCheckDone(msg)

	case profileCopyDoneMsg:
		return m.handleCopyDone(msg)

	case profileResetDoneMsg:
		return m.handleResetDone(msg)

	case pythonCheckDoneMsg:
		return m.handlePythonCheck(msg)

	case pythonSetupDoneMsg:
		return m.handlePythonSetup(msg)

	case requirementsCheckDoneMsg:
		return m.handleRequirementsCheck(msg)

	case pipInstallDoneMsg:
		return m.handlePipInstall(msg)

	case pipInstallSlowMsg:
		return m.handlePipInstallSlow()

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
	m.sourceDir = msg.sourceDir

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
	m.statusText = "目前目錄內沒有 Chrome 資料"
	m.detailText = m.confirmDetail()
	return m, nil
}

func (m model) handleCopyDone(msg profileCopyDoneMsg) (tea.Model, tea.Cmd) {
	m.projectDir = msg.projectDir
	m.profileDir = msg.profileDir

	if msg.err != nil {
		m.stage = stageDone
		m.success = false
		m.statusText = "Chrome 資料複製失敗"
		m.detailText = msg.err.Error()
		return m, nil
	}

	if err := m.loadChromeProfiles(); err != nil {
		m.stage = stageDone
		m.success = false
		m.statusText = "Chrome 設定檔讀取失敗"
		m.detailText = err.Error()
		return m, nil
	}

	m.stage = stagePythonCheck
	m.statusText = "Chrome 資料複製完成,正在檢查 Python..."
	m.detailText = fmt.Sprintf(
		"Chrome User Data 已複製到：\n%s\n\nProfile 已建立為獨立副本。\n因為 Chrome 127 之後新增的 Cookie 加密機制（App-Bound Encryption），複製過來的登入狀態無法直接繼承，這是預期行為，不是程式錯誤。\n首次啟動時，請在 Chrome 中手動登入目標站點（Tixcraft、Google 等）。登入完成後，session 會保存在這份副本中，之後每次啟動都會沿用。",
		msg.profileDir,
	)
	return m, checkPythonCmd(m.projectDir)
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
		m.stage = stagePipInstalling
		m.statusText = "venv/ 內已有 Python,正在檢查 requirements.txt 套件..."
		m.detailText = fmt.Sprintf("已在以下位置偵測到 python.exe：\n%s", msg.venvDir)
		return m, checkRequirementsCmd(m.rootDir, m.venvDir)
	}

	m.stage = stagePythonRunning
	m.statusText = "正在下載並解壓 Python..."
	m.detailText = fmt.Sprintf("下載中:\n%s\n\n目的地：%s", pythonZipURL, msg.venvDir)
	return m, setupPythonCmd(msg.rootDir, false)
}

func (m model) handlePythonSetup(msg pythonSetupDoneMsg) (tea.Model, tea.Cmd) {
	m.venvDir = msg.venvDir

	if msg.err != nil {
		m.stage = stageDone
		m.success = false
		m.statusText = "Python 安裝失敗"
		m.detailText = msg.err.Error()
		return m, nil
	}

	m.stage = stagePipInstalling
	m.statusText = "正在啟用 pip 並安裝 requirements.txt..."
	m.detailText = fmt.Sprintf("Python 已解壓到：\n%s", msg.venvDir)
	return m, m.startPipInstall(installRequirementsCmd(m.rootDir, m.venvDir))
}

func (m model) handleRequirementsCheck(msg requirementsCheckDoneMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.stage = stageDone
		m.success = false
		m.statusText = "Python 環境檢查失敗"
		m.detailText = msg.err.Error()
		return m, nil
	}

	if len(msg.missingPackages) == 0 {
		m.stage = stageConfigCheck
		m.statusText = "Python 環境正常,正在檢查設定檔..."
		m.detailText = fmt.Sprintf("已確認 requirements.txt 內的套件都已存在：\n%s", msg.reqPath)
		return m, checkConfigCmd(m.rootDir)
	}

	m.stage = stagePipInstalling
	m.statusText = "現有 venv 缺少套件,正在安裝 requirements.txt..."
	m.detailText = fmt.Sprintf(
		"缺少以下套件：\n%s\n\nrequirements：%s",
		strings.Join(msg.missingPackages, "\n"),
		msg.reqPath,
	)
	return m, m.startPipInstall(installRequirementsCmd(m.rootDir, m.venvDir))
}

func (m model) handlePipInstall(msg pipInstallDoneMsg) (tea.Model, tea.Cmd) {
	m.pipNoticeShown = false

	if msg.err != nil {
		m.stage = stageDone
		m.success = false
		m.statusText = "套件安裝失敗"
		m.detailText = msg.err.Error()
		return m, nil
	}

	m.stage = stageConfigCheck
	m.statusText = "套件安裝完成,正在檢查設定檔..."
	return m, checkConfigCmd(m.rootDir)
}

func (m model) handlePipInstallSlow() (tea.Model, tea.Cmd) {
	if m.stage != stagePipInstalling || m.pipNoticeShown {
		return m, nil
	}

	m.pipNoticeShown = true
	if m.detailText == "" {
		m.detailText = "目前腳本沒有當掉，請放心"
	} else {
		m.detailText += "\n\n目前腳本沒有當掉，請放心"
	}
	return m, nil
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
		if !m.hasChromeProfileOption(msg.oldConfig.ChromeProfileDir) || !m.hasLoginProviderOption(msg.oldConfig.LoginProvider) {
			m.stage = stageConfigForm
			m.statusText = "設定檔缺少必要選項"
			m.detailText = "請先選擇要使用的 Chrome 設定檔與登入方式。"
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
			"這會刪除目前專案內的 Chrome 副本，並刪除 %s。\n\n只會刪除 ./Profile 與 metadata，bot/.env 會保留。\n接下來會先關閉所有 Chrome，再重新回到初始化流程。\n\n刪除目標：%s",
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
		m.stage = stageWarnChromeClose
		m.statusText = "開始複製前請先關閉 Chrome"
		m.detailText = fmt.Sprintf(
			"請先手動關閉所有 Chrome 視窗。\n\n若你直接繼續,程式接下來會嘗試強制關閉 Chrome,以避免 Profile 複製失敗或資料不完整。\n\n來源：%s\n目的地：%s",
			m.sourceDir,
			m.profileDir,
		)
		return m, nil
	case "n":
		m.quitting = true
		m.quitMsg = "已取消,不進行 Chrome 資料複製。"
		return m, tea.Quit
	}
	return m, nil
}

func (m model) handleWarnKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "y":
		m.stage = stageRunning
		m.statusText = "正在關閉 Chrome 並複製資料..."
		m.detailText = fmt.Sprintf("來源：%s\n目的地：%s", m.sourceDir, m.profileDir)
		return m, copyProfileCmd(m.projectDir, m.sourceDir)
	case "n":
		m.stage = stageConfirm
		m.statusText = "目前目錄內沒有 Chrome 資料"
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

func (m *model) startPipInstall(cmd tea.Cmd) tea.Cmd {
	m.pipNoticeShown = false
	return tea.Batch(cmd, pipInstallSlowNoticeCmd())
}

func pipInstallSlowNoticeCmd() tea.Cmd {
	return tea.Tick(10*time.Second, func(time.Time) tea.Msg {
		return pipInstallSlowMsg{}
	})
}

func (m model) launchBot(reason string) (tea.Model, tea.Cmd) {
	python := filepath.Join(m.venvDir, "python.exe")
	script := filepath.Join(m.rootDir, botDirName, "main.py")

	if err := patchNodriverNetworkFile(m.venvDir); err != nil {
		m.stage = stageDone
		m.success = false
		m.statusText = "啟動前修補 nodriver 失敗"
		m.detailText = err.Error()
		return m, nil
	}

	m.quitting = true
	m.quitMsg = fmt.Sprintf("%s\n即將啟動：%s\n", reason, script)

	cmd := exec.Command(python, script)
	cmd.Dir = m.rootDir

	return m, tea.ExecProcess(cmd, func(err error) tea.Msg {
		return tea.QuitMsg{}
	})
}

func (m model) confirmDetail() string {
	return fmt.Sprintf(
		"是否同意複製一份到此目錄?\n\n專案目錄：%s\n來源：%s\n目的地：%s",
		m.projectDir,
		m.sourceDir,
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
		m.stage == stagePipInstalling,
		m.stage == stageConfigCheck,
		m.stage == stageConfigSaving:
		status = ui.PromptStyle.Render("處理中") + " " + status
	}

	if m.detailText == "" {
		return status
	}
	return status + "\n\n" + m.detailText
}

func (m model) helpText() string {
	switch m.stage {
	case stageProfileMenu:
		return "[ c ] 繼續  [ r ] 重置 Profile  [ q ] 離開"
	case stageProfileResetConfirm:
		return "[ y ] 重置並重新初始化  [ n ] 返回  [ q ] 離開"
	case stageConfirm:
		return "[ y ] 同意複製  [ n ] 取消  [ q ] 離開"
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
