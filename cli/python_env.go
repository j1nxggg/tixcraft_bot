package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
)

// Python embeddable 啟動後還需要兩類環境準備:
//   1. 打開 site-packages,讓 uv 安裝進去的套件能被 import
//   2. 修補 nodriver 的已知相容性問題,避免啟動前就炸掉
// 這些都屬於 Python 執行環境本身,集中放在同一個檔案。

const (
	nodriverCDPPath        = "Lib\\site-packages\\nodriver\\cdp\\network.py"
	nodriverConnectionPath = "Lib\\site-packages\\nodriver\\core\\connection.py"
)

// nodriverFinallyContinuePattern 找 `finally:` 後面接 `continue` 的 block(允許空白變異)。
var nodriverFinallyContinuePattern = regexp.MustCompile(
	`(?m)^[ \t]+finally:[ \t]*\r?\n[ \t]+continue[ \t]*\r?\n`,
)

// enableSitePackages 把 pythonXXX._pth 裡面 "#import site" 的註解拿掉。
// Python embeddable 預設不載 site-packages,不解開的話 uv / pip 裝進去的套件都 import 不到。
func enableSitePackages(venvDir string) error {
	pthPath, err := findPthFile(venvDir)
	if err != nil {
		return err
	}

	data, err := os.ReadFile(pthPath)
	if err != nil {
		return err
	}

	re := regexp.MustCompile(`(?m)^\s*#\s*import\s+site\s*$`)
	if re.Match(data) {
		newData := re.ReplaceAll(data, []byte("import site"))
		return os.WriteFile(pthPath, newData, 0o644)
	}

	if !regexp.MustCompile(`(?m)^import\s+site\s*$`).Match(data) {
		if len(data) > 0 && data[len(data)-1] != '\n' {
			data = append(data, '\n')
		}
		data = append(data, []byte("import site\n")...)
		return os.WriteFile(pthPath, data, 0o644)
	}

	return nil
}

func findPthFile(venvDir string) (string, error) {
	entries, err := os.ReadDir(venvDir)
	if err != nil {
		return "", err
	}

	re := regexp.MustCompile(`^python\d+\._pth$`)
	for _, e := range entries {
		if !e.IsDir() && re.MatchString(e.Name()) {
			return filepath.Join(venvDir, e.Name()), nil
		}
	}
	return "", fmt.Errorf("在 %s 找不到 python*._pth", venvDir)
}

// patchNodriverNetworkFile 清掉 nodriver/cdp/network.py 裡一個非法字元,
// 不清的話 Windows Python import 會炸。uv 安裝完 nodriver 後要馬上跑一次。
func patchNodriverNetworkFile(venvDir string) error {
	networkPath := filepath.Join(venvDir, nodriverCDPPath)

	data, err := os.ReadFile(networkPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("找不到 %s", networkPath)
		}
		return err
	}

	cleaned := bytes.ReplaceAll(data, []byte{0xB1}, nil)
	cleaned = bytes.ReplaceAll(cleaned, []byte("\uFFFD"), nil)
	if bytes.Equal(cleaned, data) {
		return nil
	}

	return os.WriteFile(networkPath, cleaned, 0o644)
}

// patchNodriverConnectionFile 移除 nodriver/core/connection.py 的 `finally: continue` 區塊,
// 避免 Python 3.14 SyntaxWarning(未來版本可能變 SyntaxError)。
func patchNodriverConnectionFile(venvDir string) error {
	connectionPath := filepath.Join(venvDir, nodriverConnectionPath)

	data, err := os.ReadFile(connectionPath)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("找不到 %s", connectionPath)
		}
		return err
	}

	if !nodriverFinallyContinuePattern.Match(data) {
		return nil
	}

	cleaned := nodriverFinallyContinuePattern.ReplaceAll(data, nil)
	return os.WriteFile(connectionPath, cleaned, 0o644)
}
