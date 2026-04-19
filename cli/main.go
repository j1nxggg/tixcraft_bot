package main

import (
	"fmt"
	"os"
	"strings"

	"cli/ui"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
)

type appStage int

const (
	stageChecking appStage = iota
	stageConfirm
	stageWarnChromeClose
	stageRunning
	stagePythonCheck
	stagePythonConfirmOverwrite
	stagePythonRunning
	stagePipInstalling
	stageConfigCheck
	stageConfigConfirmOverwrite
	stageConfigForm
	stageConfigSaving
	stageDone
)

type model struct {
	stage      appStage
	quitting   bool
	quitMsg    string
	projectDir string
	profileDir string
	sourceDir  string
	rootDir    string
	venvDir    string
	envPath    string
	oldConfig  botConfig
	cfg        *botConfig
	form       *huh.Form
	statusText string
	detailText string
	success    bool
}

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

	case pythonCheckDoneMsg:
		return m.handlePythonCheck(msg)

	case pythonSetupDoneMsg:
		return m.handlePythonSetup(msg)

	case pipInstallDoneMsg:
		return m.handlePipInstall(msg)

	case configCheckDoneMsg:
		return m.handleConfigCheck(msg)

	case configSaveDoneMsg:
		return m.handleConfigSave(msg), nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

// 轉發事件給 huh.Form,form 結束後觸發存檔
func (m model) updateConfigForm(msg tea.Msg) (tea.Model, tea.Cmd) {
	// 讓使用者在表單裡仍可以 Ctrl+C 離開整個程式
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
		m.stage = stageConfigSaving
		m.statusText = "正在寫入 .env..."
		m.detailText = m.envPath
		return m, saveConfigCmd(m.envPath, *m.cfg)
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
		m.stage = stagePythonCheck
		m.statusText = "Profile 已就緒,正在檢查 Python..."
		m.detailText = fmt.Sprintf("專案目錄內已存在資料夾：\n%s", msg.profileDir)
		return m, checkPythonCmd(m.projectDir)
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

	m.stage = stagePythonCheck
	m.statusText = "Chrome 資料複製完成,正在檢查 Python..."
	m.detailText = fmt.Sprintf("Chrome User Data 已複製到：\n%s", msg.profileDir)
	return m, checkPythonCmd(m.projectDir)
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
		m.stage = stagePythonConfirmOverwrite
		m.statusText = "venv/ 內已有 Python"
		m.detailText = fmt.Sprintf("已在以下位置偵測到 python.exe：\n%s\n\n是否要覆蓋重新下載?", msg.venvDir)
		return m, nil
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
	return m, installRequirementsCmd(m.rootDir, m.venvDir)
}

func (m model) handlePipInstall(msg pipInstallDoneMsg) (tea.Model, tea.Cmd) {
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
		m.stage = stageConfigConfirmOverwrite
		m.statusText = "偵測到既有設定檔"
		m.detailText = fmt.Sprintf(
			"路徑：%s\n\n既有設定：\n  票券網址：%s\n  票名：%s\n  票價：%s\n  票數：%s\n  場次時間：%s\n  若無票則：%s\n  設定搶票時間：%s\n\n是否要重新填寫? (y 會預設帶入舊值)",
			msg.envPath,
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

	return m.enterConfigForm(botConfig{})
}

// 進入表單 stage:把 cfg 初始化、建 form、觸發 form.Init()
func (m model) enterConfigForm(initial botConfig) (tea.Model, tea.Cmd) {
	cfg := initial
	m.cfg = &cfg
	m.form = newConfigForm(m.cfg)
	m.stage = stageConfigForm

	cmd := m.form.Init()
	return m, cmd
}

func (m model) handleConfigSave(msg configSaveDoneMsg) model {
	m.stage = stageDone

	if msg.err != nil {
		m.success = false
		m.statusText = "設定檔寫入失敗"
		m.detailText = msg.err.Error()
		return m
	}

	saved := msg.cfg
	m.cfg = &saved
	m.success = true
	m.statusText = "所有環境設定完成"
	m.detailText = fmt.Sprintf(
		"Python：%s\n設定檔：%s\n\n搶票目標：%s\n票名：%s × %s\n場次：%s\n開搶：%s\n無票處理：%s",
		m.venvDir,
		msg.envPath,
		saved.URL,
		saved.TicketName,
		saved.Quantity,
		saved.ShowTime,
		saved.GrabTime,
		saved.FallbackPolicy,
	)
	return m
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
	case stageConfirm:
		return m.handleConfirmKey(key)
	case stageWarnChromeClose:
		return m.handleWarnKey(key)
	case stagePythonConfirmOverwrite:
		return m.handlePythonOverwriteKey(key)
	case stageConfigConfirmOverwrite:
		return m.handleConfigOverwriteKey(key)
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

func (m model) handlePythonOverwriteKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "y":
		m.stage = stagePythonRunning
		m.statusText = "正在下載並解壓 Python..."
		m.detailText = fmt.Sprintf("下載中:\n%s\n\n目的地：%s", pythonZipURL, m.venvDir)
		return m, setupPythonCmd(m.rootDir, true)
	case "n":
		m.stage = stagePipInstalling
		m.statusText = "沿用現有 Python,開始安裝套件..."
		return m, installRequirementsCmd(m.rootDir, m.venvDir)
	}
	return m, nil
}

func (m model) handleConfigOverwriteKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "y":
		return m.enterConfigForm(m.oldConfig)
	case "n":
		m.stage = stageDone
		m.success = true
		m.statusText = "沿用既有設定檔"
		m.detailText = fmt.Sprintf("設定檔：%s", m.envPath)
		return m, nil
	}
	return m, nil
}

func (m model) confirmDetail() string {
	return fmt.Sprintf(
		"是否同意複製一份到此目錄?\n\n專案目錄：%s\n來源：%s\n目的地：%s",
		m.projectDir,
		m.sourceDir,
		m.profileDir,
	)
}

func (m model) View() tea.View {
	if m.quitting {
		return tea.NewView(m.quitMsg + "\n")
	}

	// 表單 stage 讓 form 自己渲染
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
	case m.stage == stageConfirm,
		m.stage == stageWarnChromeClose,
		m.stage == stagePythonConfirmOverwrite,
		m.stage == stageConfigConfirmOverwrite:
		status = ui.PromptStyle.Render("需要確認") + " " + status
	case m.stage == stageRunning,
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
	case stageConfirm:
		return "[ y ] 同意複製  [ n ] 取消  [ q ] 離開"
	case stageWarnChromeClose:
		return "[ y ] 已了解並繼續  [ n ] 返回上一步  [ q ] 離開"
	case stagePythonConfirmOverwrite:
		return "[ y ] 覆蓋重下  [ n ] 沿用現有  [ q ] 離開"
	case stageConfigConfirmOverwrite:
		return "[ y ] 重新填寫  [ n ] 沿用現有  [ q ] 離開"
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
