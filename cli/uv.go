package main

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// uv 的 Windows x64 zip,解壓後有 uv.exe + uvx.exe(都在 zip 根目錄或一層子資料夾)。
// 用 latest/download URL,GitHub 會自動 redirect 到最新 release;http.Get 預設 follow。
const (
	requirementsTxt = "requirements.txt"
	uvZipURL        = "https://github.com/astral-sh/uv/releases/latest/download/uv-x86_64-pc-windows-msvc.zip"
	uvZipName       = "uv-x86_64-pc-windows-msvc.zip"
	uvBinaryName    = "uv.exe"
)

type uvSyncDoneMsg struct {
	venvDir string
	reqPath string
	err     error
}

// installUvExe 下載 uv zip 解壓,只挑 uv.exe 搬到 venv 根目錄。
// 其它 binary (uvx.exe 等)目前用不到就不放。
func installUvExe(venvDir string) error {
	if err := os.MkdirAll(venvDir, 0o755); err != nil {
		return fmt.Errorf("建立 venv 目錄失敗: %w", err)
	}

	zipPath := filepath.Join(venvDir, uvZipName)
	if err := downloadFile(uvZipURL, zipPath, "uv"); err != nil {
		return fmt.Errorf("下載 uv 失敗: %w", err)
	}
	defer os.Remove(zipPath)

	if err := extractUvBinary(zipPath, filepath.Join(venvDir, uvBinaryName)); err != nil {
		return fmt.Errorf("解壓 uv.exe 失敗: %w", err)
	}

	return nil
}

func extractUvBinary(zipPath, destPath string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer r.Close()

	for _, f := range r.File {
		// zip 結構可能是 "uv.exe" 或 "uv-x86_64-.../uv.exe",都用 basename 比對
		if !strings.EqualFold(filepath.Base(f.Name), uvBinaryName) || f.FileInfo().IsDir() {
			continue
		}

		rc, err := f.Open()
		if err != nil {
			return err
		}
		defer rc.Close()

		out, err := os.OpenFile(destPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o755)
		if err != nil {
			return err
		}
		defer out.Close()

		if _, err := io.Copy(out, rc); err != nil {
			return err
		}
		return nil
	}

	return fmt.Errorf("zip 內找不到 %s", uvBinaryName)
}

// uvSyncCmd 使用 uv 依 requirements.txt 安裝套件,
// uv 自帶 resolver 與平行下載,且若全部已裝好會在 1~2 秒內結束,
// 不必像 pip 那樣先 dry-run 查缺失。
func uvSyncCmd(rootDir, venvDir string) tea.Cmd {
	return func() tea.Msg {
		reqPath := filepath.Join(rootDir, requirementsTxt)

		if _, err := os.Stat(reqPath); err != nil {
			if os.IsNotExist(err) {
				return uvSyncDoneMsg{
					venvDir: venvDir,
					reqPath: reqPath,
					err:     fmt.Errorf("找不到 %s", reqPath),
				}
			}
			return uvSyncDoneMsg{venvDir: venvDir, reqPath: reqPath, err: err}
		}

		if err := enableSitePackages(venvDir); err != nil {
			return uvSyncDoneMsg{venvDir: venvDir, reqPath: reqPath, err: fmt.Errorf("啟用 site-packages 失敗: %w", err)}
		}

		if err := runUvSync(venvDir, reqPath); err != nil {
			return uvSyncDoneMsg{venvDir: venvDir, reqPath: reqPath, err: err}
		}

		if err := patchNodriverNetworkFile(venvDir); err != nil {
			return uvSyncDoneMsg{venvDir: venvDir, reqPath: reqPath, err: fmt.Errorf("修補 nodriver network.py 失敗: %w", err)}
		}
		if err := patchNodriverConnectionFile(venvDir); err != nil {
			return uvSyncDoneMsg{venvDir: venvDir, reqPath: reqPath, err: fmt.Errorf("修補 nodriver connection.py 失敗: %w", err)}
		}

		return uvSyncDoneMsg{venvDir: venvDir, reqPath: reqPath}
	}
}

func runUvSync(venvDir, reqPath string) error {
	uv := filepath.Join(venvDir, uvBinaryName)
	python := filepath.Join(venvDir, "python.exe")

	cmd := exec.Command(
		uv,
		"pip", "install",
		"--python", python,
		"--no-progress",
		"-r", reqPath,
	)

	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("uv pip install 失敗: %w\n%s", err, buf.String())
	}
	return nil
}
