package main

import (
	"fmt"
	"os"
	"strings"

	"cli/ui"

	tea "charm.land/bubbletea/v2"
)

type appStage int

const (
	stageChecking appStage = iota
	stageConfirm
	stageWarnChromeClose
	stageRunning
	stageDone
)

type model struct {
	stage      appStage
	quitting   bool
	quitMsg    string
	projectDir string
	profileDir string
	sourceDir  string
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
	switch msg := msg.(type) {
	case profileCheckDoneMsg:
		return m.handleCheckDone(msg), nil

	case profileCopyDoneMsg:
		return m.handleCopyDone(msg), nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m model) handleCheckDone(msg profileCheckDoneMsg) model {
	m.projectDir = msg.projectDir
	m.profileDir = msg.profileDir
	m.sourceDir = msg.sourceDir

	if msg.err != nil {
		m.stage = stageDone
		m.success = false
		m.statusText = "Profile 檢查失敗"
		m.detailText = msg.err.Error()
		return m
	}

	if msg.exists {
		m.stage = stageDone
		m.success = true
		m.statusText = "已找到 Profile/"
		m.detailText = fmt.Sprintf("專案目錄內已存在資料夾：\n%s", msg.profileDir)
		return m
	}

	m.stage = stageConfirm
	m.statusText = "目前目錄內沒有 Chrome 資料"
	m.detailText = m.confirmDetail()
	return m
}

func (m model) handleCopyDone(msg profileCopyDoneMsg) model {
	m.projectDir = msg.projectDir
	m.profileDir = msg.profileDir
	m.stage = stageDone

	if msg.err != nil {
		m.success = false
		m.statusText = "Chrome 資料複製失敗"
		m.detailText = msg.err.Error()
		return m
	}

	m.success = true
	m.statusText = "Chrome 資料複製成功"
	m.detailText = fmt.Sprintf("Chrome User Data 已複製到：\n%s", msg.profileDir)
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
	}
	return m, nil
}

func (m model) handleConfirmKey(key string) (tea.Model, tea.Cmd) {
	switch key {
	case "y":
		m.stage = stageWarnChromeClose
		m.statusText = "開始複製前請先關閉 Chrome"
		m.detailText = fmt.Sprintf(
			"請先手動關閉所有 Chrome 視窗。\n\n若你直接繼續，程式接下來會嘗試強制關閉 Chrome，以避免 Profile 複製失敗或資料不完整。\n\n來源：%s\n目的地：%s",
			m.sourceDir,
			m.profileDir,
		)
		return m, nil
	case "n":
		m.quitting = true
		m.quitMsg = "已取消，不進行 Chrome 資料複製。"
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

func (m model) confirmDetail() string {
	return fmt.Sprintf(
		"是否同意複製一份到此目錄？\n\n專案目錄：%s\n來源：%s\n目的地：%s",
		m.projectDir,
		m.sourceDir,
		m.profileDir,
	)
}

func (m model) View() tea.View {
	if m.quitting {
		return tea.NewView(m.quitMsg + "\n")
	}

	title := ui.TitleStyle.Render("Chrome Profile Setup")
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
	case m.stage == stageConfirm || m.stage == stageWarnChromeClose:
		status = ui.PromptStyle.Render("需要確認") + " " + status
	case m.stage == stageRunning:
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
