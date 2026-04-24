package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"sync/atomic"
)

// downloadProgress 保存目前「正在下載中」的單一下載任務狀態,給 UI tick 讀取。
// 同時只會有一個下載在跑 (Python zip / uv zip),不用 map。
var downloadProgress struct {
	Label      atomic.Pointer[string]
	Downloaded atomic.Int64
	Total      atomic.Int64
	Active     atomic.Bool
}

type progressReader struct {
	reader io.Reader
}

func (p *progressReader) Read(b []byte) (int, error) {
	n, err := p.reader.Read(b)
	if n > 0 {
		downloadProgress.Downloaded.Add(int64(n))
	}
	return n, err
}

// downloadFile 下載到 dest,過程寫入 downloadProgress 讓 UI 顯示進度。
// label 是給 UI 顯示用的名稱,例如 "Python 3.14.4 embeddable"。
func downloadFile(url, dest, label string) error {
	resp, err := http.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	labelCopy := label
	downloadProgress.Label.Store(&labelCopy)
	downloadProgress.Downloaded.Store(0)
	downloadProgress.Total.Store(resp.ContentLength) // 伺服器未回傳 Content-Length 時為 -1
	downloadProgress.Active.Store(true)
	defer downloadProgress.Active.Store(false)

	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer out.Close()

	_, err = io.Copy(out, &progressReader{reader: resp.Body})
	return err
}

// downloadProgressText 給 UI 呼叫,回傳「Python 3.14.4 embeddable: 3.2 / 10.1 MB」這樣的字串;
// 若 Content-Length 未知則顯示已下載量
func downloadProgressText() string {
	if !downloadProgress.Active.Load() {
		return ""
	}

	labelPtr := downloadProgress.Label.Load()
	label := ""
	if labelPtr != nil {
		label = *labelPtr
	}

	downloaded := downloadProgress.Downloaded.Load()
	total := downloadProgress.Total.Load()

	if total > 0 {
		return fmt.Sprintf("%s: %.1f / %.1f MB", label, toMB(downloaded), toMB(total))
	}
	return fmt.Sprintf("%s: %.1f MB", label, toMB(downloaded))
}

func toMB(bytes int64) float64 {
	return float64(bytes) / 1024.0 / 1024.0
}
