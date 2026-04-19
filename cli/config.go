package main

import (
	"bufio"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/huh/v2"
)

const (
	envFileName = ".env"
	botDirName  = "bot"
)

type botConfig struct {
	URL            string
	TicketName     string
	Price          string
	Quantity       string
	ShowTime       string
	FallbackPolicy string
	GrabTime       string
}

func (c botConfig) toEnvLines() []string {
	return []string{
		"TICKET_URL=" + c.URL,
		"TICKET_NAME=" + c.TicketName,
		"TICKET_PRICE=" + c.Price,
		"TICKET_QUANTITY=" + c.Quantity,
		"SHOW_TIME=" + c.ShowTime,
		"FALLBACK_POLICY=" + c.FallbackPolicy,
		"GRAB_TIME=" + c.GrabTime,
	}
}

var envKeyMap = map[string]func(*botConfig, string){
	"TICKET_URL":      func(c *botConfig, v string) { c.URL = v },
	"TICKET_NAME":     func(c *botConfig, v string) { c.TicketName = v },
	"TICKET_PRICE":    func(c *botConfig, v string) { c.Price = v },
	"TICKET_QUANTITY": func(c *botConfig, v string) { c.Quantity = v },
	"SHOW_TIME":       func(c *botConfig, v string) { c.ShowTime = v },
	"FALLBACK_POLICY": func(c *botConfig, v string) { c.FallbackPolicy = v },
	"GRAB_TIME":       func(c *botConfig, v string) { c.GrabTime = v },
}

var fallbackOptions = []string{"往下找", "往上找", "重新整理", "放棄"}

type configCheckDoneMsg struct {
	rootDir   string
	envPath   string
	exists    bool
	oldConfig botConfig
	err       error
}

type configSaveDoneMsg struct {
	envPath string
	cfg     botConfig
	err     error
}

func checkConfigCmd(rootDir string) tea.Cmd {
	return func() tea.Msg {
		envPath := filepath.Join(rootDir, botDirName, envFileName)

		_, err := os.Stat(envPath)
		if os.IsNotExist(err) {
			return configCheckDoneMsg{rootDir: rootDir, envPath: envPath, exists: false}
		}
		if err != nil {
			return configCheckDoneMsg{rootDir: rootDir, envPath: envPath, err: err}
		}

		cfg, err := loadEnvFile(envPath)
		if err != nil {
			return configCheckDoneMsg{rootDir: rootDir, envPath: envPath, err: err}
		}
		return configCheckDoneMsg{rootDir: rootDir, envPath: envPath, exists: true, oldConfig: cfg}
	}
}

func loadEnvFile(path string) (botConfig, error) {
	var cfg botConfig

	f, err := os.Open(path)
	if err != nil {
		return cfg, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		idx := strings.Index(line, "=")
		if idx < 0 {
			continue
		}

		key := strings.TrimSpace(line[:idx])
		value := strings.TrimSpace(line[idx+1:])
		value = strings.Trim(value, `"'`)

		if setter, ok := envKeyMap[key]; ok {
			setter(&cfg, value)
		}
	}
	return cfg, scanner.Err()
}

func saveEnvFile(path string, cfg botConfig) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	content := strings.Join(cfg.toEnvLines(), "\n") + "\n"
	return os.WriteFile(path, []byte(content), 0o644)
}

func saveConfigCmd(envPath string, cfg botConfig) tea.Cmd {
	return func() tea.Msg {
		err := saveEnvFile(envPath, cfg)
		return configSaveDoneMsg{envPath: envPath, cfg: cfg, err: err}
	}
}

// 建立一個新的 huh Form,預設值帶入 initial
func newConfigForm(initial *botConfig) *huh.Form {
	if initial.FallbackPolicy == "" {
		initial.FallbackPolicy = fallbackOptions[0]
	}

	return huh.NewForm(
		huh.NewGroup(
			huh.NewInput().
				Title("票券網址").
				Placeholder("https://tixcraft.com/activity/detail/xxx").
				Value(&initial.URL).
				Validate(validateTicketURL),

			huh.NewInput().
				Title("票名").
				Value(&initial.TicketName).
				Validate(requireNonEmpty),

			huh.NewInput().
				Title("票價").
				Value(&initial.Price).
				Validate(validatePositiveInt),

			huh.NewInput().
				Title("票數").
				Value(&initial.Quantity).
				Validate(validatePositiveInt),

			huh.NewInput().
				Title("場次時間").
				Description("格式：YYYY/MM/DD HH:MM:SS").
				Value(&initial.ShowTime).
				Validate(validateDateTime),

			huh.NewSelect[string]().
				Title("若無票則").
				Options(huh.NewOptions(fallbackOptions...)...).
				Value(&initial.FallbackPolicy),

			huh.NewInput().
				Title("設定搶票時間").
				Description("格式：YYYY/MM/DD HH:MM:SS").
				Value(&initial.GrabTime).
				Validate(validateDateTime),
		),
	)
}

// ------ 驗證函式 ------

func requireNonEmpty(s string) error {
	if strings.TrimSpace(s) == "" {
		return errors.New("不可為空")
	}
	return nil
}

func validatePositiveInt(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return errors.New("不可為空")
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return errors.New("必須是整數")
	}
	if n <= 0 {
		return errors.New("必須大於 0")
	}
	return nil
}

var ticketURLPattern = regexp.MustCompile(`^https://tixcraft\.com/activity/detail/\S+$`)

func validateTicketURL(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return errors.New("不可為空")
	}
	if _, err := url.Parse(s); err != nil {
		return errors.New("不是合法的網址")
	}
	if !ticketURLPattern.MatchString(s) {
		return errors.New("格式需為 https://tixcraft.com/activity/detail/xxx")
	}
	return nil
}

func validateDateTime(s string) error {
	s = strings.TrimSpace(s)
	if s == "" {
		return errors.New("不可為空")
	}
	if _, err := time.Parse("2006/01/02 15:04:05", s); err != nil {
		return errors.New("格式需為 YYYY/MM/DD HH:MM:SS")
	}
	return nil
}
