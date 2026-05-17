package main

import (
	"io"
	"os"
	"os/exec"
	"sync"
)

const processOutputTailBytes = 32 * 1024

type tailBuffer struct {
	mu   sync.Mutex
	data []byte
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	b.data = append(b.data, p...)
	if len(b.data) > processOutputTailBytes {
		b.data = b.data[len(b.data)-processOutputTailBytes:]
	}

	return len(p), nil
}

func (b *tailBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return string(append([]byte(nil), b.data...))
}

func attachProcessOutput(cmd *exec.Cmd) *tailBuffer {
	output := &tailBuffer{}
	cmd.Stdout = io.MultiWriter(os.Stdout, output)
	cmd.Stderr = io.MultiWriter(os.Stderr, output)
	return output
}

func pythonProcessEnv() []string {
	return append(os.Environ(), "PYTHONUTF8=1", "PYTHONIOENCODING=utf-8")
}
