package command

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestCLIStartCheckpointResume(t *testing.T) {
	repository := commandTestRepository(t)
	store := t.TempDir()

	var out, diagnostics bytes.Buffer
	code := Run([]string{"--cwd", repository, "--store", store, "--client", "com.openai.codex", "--json", "start", "--title", "CLI flow"}, bytes.NewReader(nil), &out, &diagnostics, "dev")
	if code != 0 {
		t.Fatalf("start exit = %d, stderr = %s", code, diagnostics.String())
	}
	assertCommand(t, out.Bytes(), "start")

	inputPath := filepath.Join(t.TempDir(), "checkpoint.json")
	input := []byte(`{"goal":"CLI flow","summary":"Core is small.","decisions":["Keep the input minimal"],"next_actions":["Test resume"]}`)
	if err := os.WriteFile(inputPath, input, 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	diagnostics.Reset()
	code = Run([]string{"--cwd", repository, "--store", store, "--client", "com.anthropic.claude-code", "--json", "checkpoint", "--input", inputPath}, bytes.NewReader(nil), &out, &diagnostics, "dev")
	if code != 0 {
		t.Fatalf("checkpoint exit = %d, stderr = %s", code, diagnostics.String())
	}
	assertCommand(t, out.Bytes(), "checkpoint")

	out.Reset()
	diagnostics.Reset()
	code = Run([]string{"--cwd", repository, "--store", store, "--json", "resume"}, bytes.NewReader(nil), &out, &diagnostics, "dev")
	if code != 0 {
		t.Fatalf("resume exit = %d, stderr = %s", code, diagnostics.String())
	}
	assertCommand(t, out.Bytes(), "resume")
}

func TestCLIRejectsRemovedCheckpointField(t *testing.T) {
	repository := commandTestRepository(t)
	store := t.TempDir()
	var out, diagnostics bytes.Buffer
	if code := Run([]string{"--cwd", repository, "--store", store, "start", "--title", "Strict input"}, bytes.NewReader(nil), &out, &diagnostics, "dev"); code != 0 {
		t.Fatalf("start exit = %d, stderr = %s", code, diagnostics.String())
	}
	out.Reset()
	diagnostics.Reset()
	code := Run([]string{"--cwd", repository, "--store", store, "checkpoint", "--input", "-"}, bytes.NewBufferString(`{"goal":"x","summary":"y","tests":[]}`), &out, &diagnostics, "dev")
	if code != 1 {
		t.Fatalf("checkpoint exit = %d, want 1", code)
	}
}

func assertCommand(t *testing.T, output []byte, want string) {
	t.Helper()
	var response struct {
		OutputVersion int    `json:"output_version"`
		Command       string `json:"command"`
	}
	if err := json.Unmarshal(output, &response); err != nil {
		t.Fatalf("decode output: %v\n%s", err, output)
	}
	if response.OutputVersion != 1 || response.Command != want {
		t.Fatalf("response = %#v", response)
	}
}

func commandTestRepository(t *testing.T) string {
	t.Helper()
	repository := t.TempDir()
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "ctx@example.com"}, {"config", "user.name", "ctx test"}} {
		command := exec.Command("git", append([]string{"-C", repository}, args...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
	return repository
}
