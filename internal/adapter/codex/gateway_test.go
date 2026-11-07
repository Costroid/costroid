package codex_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Costroid/costroid/internal/adapter/codex"
	"github.com/Costroid/costroid/internal/decimal"
	"github.com/Costroid/costroid/internal/gateway"
	"github.com/Costroid/costroid/internal/run"
)

// runThroughGateway runs argv under a gateway that counts every request it
// is sent, and returns the run's error and that count.
func runThroughGateway(t *testing.T, argv ...string) (int64, error) {
	t.Helper()
	var asked atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		asked.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)
	session := &gateway.Session{Client: gateway.NewClient(server.URL), Key: "sk-run-key-for-tests", Wait: 50 * time.Millisecond, Poll: 10 * time.Millisecond}
	_, err := run.Run(context.Background(), run.Options{Argv: argv, Budget: decimal.MustParse("0.01"), Gateway: session})
	return asked.Load(), err
}

// assertRefused holds a command to a refusal that names flag, made before the
// gateway was asked anything.
func assertRefused(t *testing.T, flag string, argv ...string) {
	t.Helper()
	asked, err := runThroughGateway(t, argv...)
	want := "run: " + flag + " sends codex's requests around the gateway, and a gateway cannot hold a run that routes around it"
	if err == nil || !strings.HasPrefix(err.Error(), want) {
		t.Errorf("%q: err %v, want %q", argv, err, want)
	}
	if asked != 0 {
		t.Errorf("%q: the run asked the gateway %d times before it refused", argv, asked)
	}
}

func TestGatewayRefusesCodexLocalProvider(t *testing.T) {
	for _, argv := range []struct {
		flag string
		argv []string
	}{
		{"--oss", []string{"codex", "exec", "--oss", "work"}},
		{"--oss", []string{"codex", "--oss", "exec", "work"}},
		{"--oss", []string{"codex", "exec", "work", "--oss"}},
		{"--local-provider", []string{"codex", "exec", "--local-provider", "ollama", "work"}},
		{"--local-provider", []string{"codex", "exec", "--local-provider=lmstudio", "work"}},
	} {
		assertRefused(t, argv.flag, argv.argv...)
	}
}

func TestGatewayRefusesACodexProviderOverride(t *testing.T) {
	for _, argv := range []struct {
		flag string
		argv []string
	}{
		{"-c model_provider", []string{"codex", "exec", "-c", `model_provider="elsewhere"`, "work"}},
		{"--config model_provider", []string{"codex", "--config", "model_provider=elsewhere", "exec", "work"}},
		{"--config openai_base_url", []string{"codex", "exec", "--config", `openai_base_url="https://elsewhere.test/v1"`, "work"}},
		{"--config openai_base_url", []string{"codex", "exec", `--config=openai_base_url="https://elsewhere.test/v1"`, "work"}},
		{"-c model_providers.elsewhere.base_url", []string{"codex", "exec", "-c", `model_providers.elsewhere.base_url="https://elsewhere.test"`, "work"}},
		{"-c model_provider", []string{"codex", "exec", `-cmodel_provider="elsewhere"`, "work"}},
		{"-c model_provider", []string{"codex", "exec", "-c", ` model_provider = "elsewhere"`, "work"}},
	} {
		assertRefused(t, argv.flag, argv.argv...)
	}

	for _, argv := range [][]string{
		{"codex", "exec", "-c", `model="gpt-5-nano"`, "-m", "gpt-5-nano", "work"},
		{"codex", "exec", "-c", `model_reasoning_effort="low"`, "work"},
		{"codex", "exec", "--", "--oss", "-c", `model_provider="elsewhere"`},
	} {
		if flag, err := (codex.Agent{}).GatewayBypass(argv); flag != "" || err != nil {
			t.Errorf("%q routes nothing around the gateway, yet reads as %q, %v", argv, flag, err)
		}
	}
	if asked, _ := runThroughGateway(t, "codex", "exec", "-c", `model="gpt-5-nano"`, "work"); asked == 0 {
		t.Error("a command that routes nothing around the gateway never reached it")
	}
}
