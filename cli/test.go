package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
)

func (m model) launchTestBot(reason string) (tea.Model, tea.Cmd) {
	python := filepath.Join(m.venvDir, "python.exe")
	script := filepath.Join(m.rootDir, "test", "main.py")

	if _, err := os.Stat(script); err != nil {
		m.stage = stageDone
		m.success = false
		m.statusText = "測試模式入口不存在"
		m.detailText = err.Error()
		return m, nil
	}

	if err := patchNodriverNetworkFile(m.venvDir); err != nil {
		m.stage = stageDone
		m.success = false
		m.statusText = "啟動測試模式前修補 nodriver network.py 失敗"
		m.detailText = err.Error()
		return m, nil
	}
	if err := patchNodriverConnectionFile(m.venvDir); err != nil {
		m.stage = stageDone
		m.success = false
		m.statusText = "啟動測試模式前修補 nodriver connection.py 失敗"
		m.detailText = err.Error()
		return m, nil
	}

	m.statusText = "正在執行測試模式..."
	m.detailText = fmt.Sprintf("%s\n即將啟動：%s", reason, script)

	cmd := exec.Command(python, "-u", script)
	cmd.Dir = m.rootDir
	cmd.Env = append(
		pythonProcessEnv(),
		"CHROME_PROFILE_DIR="+m.testChromeProfileDir(),
		fmt.Sprintf("TEST_RUN_COUNT=%d", m.testRunCount),
	)
	output := attachProcessOutput(cmd)

	return m, tea.ExecProcess(cmd, func(err error) tea.Msg {
		return botProcessDoneMsg{err: err, output: output.String()}
	})
}

func (m model) testChromeProfileDir() string {
	if len(m.chromeProfiles) > 0 && strings.TrimSpace(m.chromeProfiles[0].Basename) != "" {
		return m.chromeProfiles[0].Basename
	}
	return defaultChromeProfileDir
}
