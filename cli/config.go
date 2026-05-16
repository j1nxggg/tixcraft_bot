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
	URL              string
	TicketName       string
	Price            string
	Quantity         string
	ShowTime         string
	FallbackPolicy   string
	GrabTime         string
	ChromeProfileDir string
	LoginProvider    string
}

func (c botConfig) toEnvLines() []string {
	return []string{
		"CHROME_PROFILE_DIR=" + c.ChromeProfileDir,
		"LOGIN_PROVIDER=" + c.LoginProvider,
		"TICKET_URL=" + c.URL,
		"TICKET_NAME=" + c.TicketName,
		"TICKET_PRICE=" + c.Price,
		"TICKET_QUANTITY=" + c.Quantity,
		"SHOW_TIME=" + normalizeDateTimeValue(c.ShowTime),
		"FALLBACK_POLICY=" + c.FallbackPolicy,
		"GRAB_TIME=" + normalizeDateTimeValue(c.GrabTime),
	}
}

var envKeyMap = map[string]func(*botConfig, string){
	"CHROME_PROFILE_DIR": func(c *botConfig, v string) { c.ChromeProfileDir = v },
	"LOGIN_PROVIDER":     func(c *botConfig, v string) { c.LoginProvider = strings.ToLower(strings.TrimSpace(v)) },
	"TICKET_URL":         func(c *botConfig, v string) { c.URL = v },
	"TICKET_NAME":        func(c *botConfig, v string) { c.TicketName = v },
	"TICKET_PRICE":       func(c *botConfig, v string) { c.Price = v },
	"TICKET_QUANTITY":    func(c *botConfig, v string) { c.Quantity = v },
	"SHOW_TIME":          func(c *botConfig, v string) { c.ShowTime = normalizeDateTimeValue(v) },
	"FALLBACK_POLICY":    func(c *botConfig, v string) { c.FallbackPolicy = v },
	"GRAB_TIME":          func(c *botConfig, v string) { c.GrabTime = normalizeDateTimeValue(v) },
}

var fallbackOptions = []string{"往下找", "往上找"}
var loginProviderOptions = []string{"google", "facebook"}
var quantityOptions = []string{"1", "2", "3", "4"}

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
//
// 把欄位拆成三個 group (帳號 / 票券 / 時間) 是為了降低每一幀要渲染的欄位數量,
// 單一 group 塞 9 個欄位會讓每次按鍵都要重繪整組,實測明顯卡頓。
func newConfigForm(initial *botConfig, chromeProfiles []chromeProfileChoice) *huh.Form {
	if initial.FallbackPolicy == "" {
		initial.FallbackPolicy = fallbackOptions[0]
	}
	if initial.ChromeProfileDir == "" && len(chromeProfiles) > 0 {
		initial.ChromeProfileDir = chromeProfiles[0].Basename
	}
	if initial.LoginProvider == "" {
		initial.LoginProvider = loginProviderOptions[0]
	}
	if !isValidQuantityOption(initial.Quantity) {
		initial.Quantity = quantityOptions[0]
	}

	profileOptions := make([]huh.Option[string], 0, len(chromeProfiles))
	for _, profile := range chromeProfiles {
		profileOptions = append(profileOptions, huh.NewOption(profile.Label, profile.Basename))
	}

	accountGroup := huh.NewGroup(
		huh.NewSelect[string]().
			Title("Chrome 設定檔").
			Description("使用專案內的獨立 Chrome Profile").
			Options(profileOptions...).
			Value(&initial.ChromeProfileDir),

		huh.NewSelect[string]().
			Title("登入方式").
			Description("進入 Tixcraft 後要點選的社群登入按鈕").
			Options(
				huh.NewOption("Google", "google"),
				huh.NewOption("Facebook", "facebook"),
			).
			Value(&initial.LoginProvider),
	)

	ticketGroup := huh.NewGroup(
		huh.NewInput().
			Title("票券網址").
			Placeholder("https://tixcraft.com/activity/detail/xxx").
			CharLimit(200).
			Value(&initial.URL).
			Validate(validateTicketURL),

		huh.NewInput().
			Title("票名").
			CharLimit(60).
			Value(&initial.TicketName).
			Validate(requireNonEmpty),

		huh.NewInput().
			Title("票價").
			CharLimit(10).
			Value(&initial.Price).
			Validate(validatePositiveInt),

		huh.NewSelect[string]().
			Title("票數").
			Description("拓元單筆最多 4 張").
			Options(huh.NewOptions(quantityOptions...)...).
			Value(&initial.Quantity),
	)

	timingGroup := huh.NewGroup(
		huh.NewInput().
			Title("場次時間").
			Description("格式：YYYY/MM/DD HH:MM").
			CharLimit(20).
			Value(&initial.ShowTime).
			Validate(validateDateTime),

		huh.NewSelect[string]().
			Title("若無票則").
			Options(huh.NewOptions(fallbackOptions...)...).
			Value(&initial.FallbackPolicy),

		huh.NewInput().
			Title("設定搶票時間").
			Description("格式：YYYY/MM/DD HH:MM").
			CharLimit(20).
			Value(&initial.GrabTime).
			Validate(validateDateTime),
	)

	return huh.NewForm(accountGroup, ticketGroup, timingGroup)
}

func isValidQuantityOption(value string) bool {
	value = strings.TrimSpace(value)
	for _, option := range quantityOptions {
		if option == value {
			return true
		}
	}
	return false
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
	s = normalizeDateTimeValue(s)
	if s == "" {
		return errors.New("不可為空")
	}
	if _, err := time.Parse("2006/01/02 15:04", s); err != nil {
		return errors.New("格式需為 YYYY/MM/DD HH:MM")
	}
	return nil
}

func normalizeDateTimeValue(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}

	for _, layout := range []string{"2006/01/02 15:04:05", "2006/01/02 15:04"} {
		if parsed, err := time.Parse(layout, s); err == nil {
			return parsed.Format("2006/01/02 15:04")
		}
	}

	return s
}
