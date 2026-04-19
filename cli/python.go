package main

import (
	"archive/zip"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	tea "charm.land/bubbletea/v2"
)

const (
	pythonZipURL  = "https://github.com/j1nxggg/tixcraft_bot/releases/download/Win32-Python-embeddable-3.14.4/python-3.14.4-embed-amd64.zip"
	pythonZipName = "python-3.14.4-embed-amd64.zip"
	venvDirName   = "venv"
)

type pythonCheckDoneMsg struct {
	rootDir string
	venvDir string
	exists  bool
	err     error
}

type pythonSetupDoneMsg struct {
	venvDir string
	err     error
}

type pythonProgressMsg struct {
	stage string
}

func checkPythonCmd(projectDir string) tea.Cmd {
	return func() tea.Msg {
		rootDir := projectDir
		venvDir := filepath.Join(rootDir, venvDirName)

		pythonExe := filepath.Join(venvDir, "python.exe")
		_, err := os.Stat(pythonExe)

		if err == nil {
			return pythonCheckDoneMsg{
				rootDir: rootDir,
				venvDir: venvDir,
				exists:  true,
			}
		}

		if os.IsNotExist(err) {
			return pythonCheckDoneMsg{
				rootDir: rootDir,
				venvDir: venvDir,
				exists:  false,
			}
		}

		return pythonCheckDoneMsg{
			rootDir: rootDir,
			venvDir: venvDir,
			err:     err,
		}
	}
}

func setupPythonCmd(rootDir string, overwrite bool) tea.Cmd {
	return func() tea.Msg {
		venvDir := filepath.Join(rootDir, venvDirName)

		if overwrite {
			if err := os.RemoveAll(venvDir); err != nil {
				return pythonSetupDoneMsg{venvDir: venvDir, err: fmt.Errorf("清除舊 venv 失敗: %w", err)}
			}
		}

		if err := os.MkdirAll(venvDir, 0o755); err != nil {
			return pythonSetupDoneMsg{venvDir: venvDir, err: fmt.Errorf("建立 venv 目錄失敗: %w", err)}
		}

		zipPath := filepath.Join(venvDir, pythonZipName)
		if err := downloadFile(pythonZipURL, zipPath); err != nil {
			return pythonSetupDoneMsg{venvDir: venvDir, err: fmt.Errorf("下載失敗: %w", err)}
		}

		if err := unzipTo(zipPath, venvDir); err != nil {
			return pythonSetupDoneMsg{venvDir: venvDir, err: fmt.Errorf("解壓縮失敗: %w", err)}
		}

		if err := os.Remove(zipPath); err != nil {
			return pythonSetupDoneMsg{venvDir: venvDir, err: fmt.Errorf("刪除壓縮檔失敗: %w", err)}
		}

		return pythonSetupDoneMsg{venvDir: venvDir}
	}
}

func downloadFile(url, dest string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, resp.Body)
	return err
}

func unzipTo(zipPath, destDir string) error {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return err
	}
	defer r.Close()

	for _, f := range r.File {
		if err := extractZipEntry(f, destDir); err != nil {
			return err
		}
	}
	return nil
}

func extractZipEntry(f *zip.File, destDir string) error {
	target := filepath.Join(destDir, f.Name)

	cleanDest := filepath.Clean(destDir) + string(os.PathSeparator)
	if !filepath.HasPrefix(filepath.Clean(target), cleanDest) && filepath.Clean(target) != filepath.Clean(destDir) {
		return fmt.Errorf("非法壓縮路徑: %s", f.Name)
	}

	if f.FileInfo().IsDir() {
		return os.MkdirAll(target, f.Mode())
	}

	if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
		return err
	}

	rc, err := f.Open()
	if err != nil {
		return err
	}
	defer rc.Close()

	out, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, f.Mode())
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, rc)
	return err
}
