package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"localization-kit/src/internal/agent"
	"localization-kit/src/internal/store"
)

func TestWorkspaceBinaryAcrossProcesses(t *testing.T) {
	binary := os.Getenv("LOCALIZATION_KIT_TEST_BINARY")
	if binary == "" {
		t.Skip("LOCALIZATION_KIT_TEST_BINARY is not configured")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"model":"test","choices":[{"message":{"content":"你好"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "source"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "source", "input.jsonl"), []byte("{\"id\":\"1\",\"src\":\"こんにちは\"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	modelConfig := fmt.Sprintf("[models.test]\nurl=%q\nmodel='test'\nprofile='hy-mt2'\n", server.URL)
	otherDir := t.TempDir()
	capture := func(args ...string) []byte {
		t.Helper()
		cmd := exec.Command(binary, args...)
		cmd.Dir = otherDir
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		stdout, err := cmd.Output()
		if err != nil {
			t.Fatalf("%v: %v stderr=%s", args, err, stderr.String())
		}
		if !bytes.Contains(stderr.Bytes(), []byte("INFO")) {
			t.Fatalf("%v did not write log to stderr", args)
		}
		return stdout
	}
	execute := func(args ...string) []byte {
		t.Helper()
		stdout := capture(args...)
		if !json.Valid(bytes.TrimSpace(stdout)) {
			t.Fatalf("%v stdout is not JSON: %s", args, stdout)
		}
		return stdout
	}
	execute("init", root, "--adapter", "jsonl")
	configureWorkspaceModel(t, root, modelConfig)
	execute("terms", "init", "--project-config", filepath.Join(root, "config.toml"))
	execute("characters", "init", "--project-config", filepath.Join(root, "config.toml"))
	execute("translate", root, "--model", "test")
	out := filepath.Join(t.TempDir(), "translation.jsonl")
	execute("export", root, "--stage", "translate", "--out", out)
	data, err := os.ReadFile(out)
	if err != nil || !bytes.Contains(data, []byte("你好")) {
		t.Fatalf("export=%s err=%v", data, err)
	}
	nextArgs := []string{"next", root, "--stage", "polish", "--worker", "editor", "--model", "editor"}
	snapshot := filepath.Join(root, "state", "translate", "translation.json")
	if err := os.Remove(snapshot); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(snapshot, 0700); err != nil {
		t.Fatal(err)
	}
	var task agent.Task
	if err := json.Unmarshal(execute(nextArgs...), &task); err != nil || task.Status != "ready" || task.TaskID == "" || len(task.Lines) != 1 {
		t.Fatalf("next=%+v err=%v", task, err)
	}
	if err := os.Remove(snapshot); err != nil {
		t.Fatal(err)
	}
	state, err := store.OpenExisting(filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	unlocked := make(chan error, 1)
	time.AfterFunc(300*time.Millisecond, func() { unlocked <- state.Close() })
	output := capture(append(nextArgs, "--format", "text")...)
	if err := <-unlocked; err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(output, []byte("任务 "+task.TaskID)) ||
		!bytes.Contains(output, []byte("1  こんにちは -> 你好")) ||
		bytes.Contains(output, []byte("· ")) {
		t.Fatalf("unexpected next text: %s", output)
	}
	changes := filepath.Join(t.TempDir(), "changes.json")
	if err := os.WriteFile(changes, []byte(`[{"id":"1","dst":"您好","reason":"语气"}]`), 0600); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		execute("submit", root, "--worker", "editor", "--task", task.TaskID, "--changes", changes)
	}
	if done := capture(append(nextArgs, "--format", "text")...); string(done) != "状态 done\n" {
		t.Fatalf("unexpected completed status: %s", done)
	}
	logs, err := filepath.Glob(filepath.Join(root, "*.log"))
	if err != nil || len(logs) != 1 || filepath.Base(logs[0]) != "localization-kit.log" {
		t.Fatalf("logs=%v err=%v", logs, err)
	}
	logData, err := os.ReadFile(logs[0])
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(logData, []byte("任务已领取，关闭作业时出错")) || !bytes.Contains(logData, []byte(task.TaskID)) {
		t.Fatalf("missing claim recovery diagnostic: %s", logData)
	}
	for _, command := range []string{"init", "translate", "export"} {
		if !bytes.Contains(logData, []byte("完成 "+command)) {
			t.Fatalf("missing %s in workspace log: %s", command, logData)
		}
	}
}

func TestWorkLogsIdentifyCommandAndFailure(t *testing.T) {
	binary := os.Getenv("LOCALIZATION_KIT_TEST_BINARY")
	if binary == "" {
		t.Skip("LOCALIZATION_KIT_TEST_BINARY is not configured")
	}
	root := t.TempDir()
	cmd := exec.Command(binary, "characters", "init", "--project-config", "config.toml")
	cmd.Dir = root
	output, err := cmd.CombinedOutput()
	if err == nil || !bytes.Contains(output, []byte(root)) {
		t.Fatalf("invalid directory: %s %v", output, err)
	}
	if _, err := os.Stat(filepath.Join(root, "localization-kit.log")); !os.IsNotExist(err) {
		t.Fatalf("unresolved project wrote a log: %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "source"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "source", "input.jsonl"), []byte("{\"id\":\"1\",\"src\":\"こんにちは\"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"init", ".", "--adapter", "jsonl"}, {"characters", "init", "--project-config", "config.toml"}} {
		cmd := exec.Command(binary, args...)
		cmd.Dir = root
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v: %s %v", args, output, err)
		}
	}
	cmd = exec.Command(binary, "characters", "init", "--project-config", "config.toml")
	cmd.Dir = root
	output, err = cmd.CombinedOutput()
	if err == nil {
		t.Fatal("duplicate output did not fail")
	}
	data, err := os.ReadFile(filepath.Join(root, "localization-kit.log"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"characters init", "角色卡模板已创建", "ERROR", "characters.json"} {
		if !bytes.Contains(data, []byte(want)) {
			t.Fatalf("missing %q in log: %s", want, data)
		}
	}
	if !bytes.Contains(output, []byte("ERROR")) {
		t.Fatalf("missing stderr error: %s", output)
	}
}

func TestStatusDuringTranslation(t *testing.T) {
	binary := os.Getenv("LOCALIZATION_KIT_TEST_BINARY")
	if binary == "" {
		t.Skip("LOCALIZATION_KIT_TEST_BINARY is not configured")
	}
	requests := make(chan int, 2)
	gates := []chan struct{}{make(chan struct{}), make(chan struct{})}
	var mu sync.Mutex
	count := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		i := count
		count++
		mu.Unlock()
		if i >= len(gates) {
			http.Error(w, "unexpected request", 500)
			return
		}
		requests <- i
		<-gates[i]
		_, _ = w.Write([]byte(`{"model":"test","choices":[{"message":{"content":"你好"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	var release sync.Once
	closeGates := func() {
		release.Do(func() {
			for _, gate := range gates {
				select {
				case <-gate:
				default:
					close(gate)
				}
			}
		})
	}
	defer closeGates()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "source"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "source", "input.jsonl"), []byte("{\"id\":\"1\",\"src\":\"こんにちは\"}\n{\"id\":\"2\",\"src\":\"ありがとう\"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	modelConfig := fmt.Sprintf("[models.test]\nurl=%q\nmodel='test'\nprofile='hy-mt2'\n", server.URL)
	run := func(args ...string) agent.Status {
		t.Helper()
		cmd := exec.Command(binary, args...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			t.Fatalf("%v: %v: %s", args, err, stderr.String())
		}
		var status agent.Status
		if args[0] == "status" {
			if err := json.Unmarshal(out, &status); err != nil {
				t.Fatal(err)
			}
		}
		return status
	}
	run("init", root, "--adapter", "jsonl")
	configureWorkspaceModel(t, root, modelConfig)
	if initial := run("status", root); initial.Translate["pending"] != 2 {
		t.Fatalf("initial=%+v", initial)
	}
	if _, err := os.Stat(filepath.Join(root, "state")); !os.IsNotExist(err) {
		t.Fatalf("status created state: %v", err)
	}
	cmd := exec.Command(binary, "translate", root, "--model", "test")
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		closeGates()
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	waitRequest := func(want int) {
		t.Helper()
		select {
		case got := <-requests:
			if got != want {
				t.Fatalf("request=%d want=%d", got, want)
			}
		case <-time.After(20 * time.Second):
			t.Fatal("model request timeout")
		}
	}
	waitRequest(0)
	first := run("status", root, "--details")
	if first.Translate["pending"] != 2 || first.SnapshotAt == nil {
		t.Fatalf("during first request: %+v", first)
	}
	close(gates[0])
	waitRequest(1)
	second := run("status", root, "--id", "1")
	if second.Translate["done"] != 1 || second.Translate["pending"] != 1 || len(second.Lines) != 1 || second.Lines[0].Target == nil {
		data, _ := os.ReadFile(filepath.Join(root, "state", "results.jsonl"))
		t.Fatalf("during second request: %+v history=%s", second, data)
	}
	if _, err := os.Stat(filepath.Join(root, "state", "translate", "translation.json")); err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{"polish", "proofread"} {
		if _, err := os.Stat(filepath.Join(root, "state", stage)); !os.IsNotExist(err) {
			t.Fatalf("unstarted %s created: %v", stage, err)
		}
	}
	closeGates()
	if err := cmd.Wait(); err != nil {
		t.Fatal(err)
	}
}
