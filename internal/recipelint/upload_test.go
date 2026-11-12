package recipelint

import (
	"archive/zip"
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.yaml.in/yaml/v3"
)

// uploadRecipe is a recipe that sends one day's rows to a platform's API: its
// send step, the secrets that step reads, the ones among them that are sent as
// a header, the status the platform answers when it takes a file, and the
// address the file goes to.
type uploadRecipe struct {
	send        string
	secrets     []string
	credentials []string
	status      string
	address     string
}

var uploadRecipes = map[string]uploadRecipe{
	"github-vantage-upload.yml": {
		send:        "Send it to Vantage",
		secrets:     []string{"VANTAGE_API_TOKEN", "VANTAGE_INTEGRATION_TOKEN"},
		credentials: []string{"VANTAGE_API_TOKEN"},
		status:      "201",
		address:     "https://api.vantage.sh/v2/integrations/$VANTAGE_INTEGRATION_TOKEN/costs.csv",
	},
	"github-datadog-upload.yml": {
		send:        "Send it to Datadog",
		secrets:     []string{"DD_API_KEY", "DD_APP_KEY"},
		credentials: []string{"DD_API_KEY", "DD_APP_KEY"},
		status:      "202",
		address:     "https://api.$DD_SITE/api/v2/cost/custom_costs",
	},
}

// uploadWorkflow is the part of an upload recipe the test reads.
type uploadWorkflow struct {
	On          map[string]yaml.Node `yaml:"on"`
	Permissions map[string]string    `yaml:"permissions"`
	Jobs        map[string]struct {
		TimeoutMinutes int               `yaml:"timeout-minutes"`
		Permissions    map[string]string `yaml:"permissions"`
		Steps          []struct {
			Name string            `yaml:"name"`
			Uses string            `yaml:"uses"`
			With map[string]string `yaml:"with"`
			Env  map[string]string `yaml:"env"`
			Run  string            `yaml:"run"`
		} `yaml:"steps"`
	} `yaml:"jobs"`
}

// TestUploadRecipesLintClean holds each upload recipe to the recipe rules and
// to costroid lint --strict, and to what a job that holds a platform's token
// needs: a schedule and a manual run and no pull request, no permission at the
// top and actions: read alone on its job, a timeout, each secret read into one
// step's environment, and a curl that fails on a refusal, takes the token on
// standard input and checks the status the platform answers.
func TestUploadRecipesLintClean(t *testing.T) {
	for name, want := range uploadRecipes {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(recipesDir, name)
			source := read(t, path)
			if report := LintBytes(path, []byte(source)); len(report.Findings)+len(report.Notes) != 0 {
				t.Fatalf("lint-recipes: %v %v", report.Findings, report.Notes)
			}
			if report := LintCustomer(path, []byte(source)); len(report.Findings) != 0 {
				t.Fatalf("lint --strict: %v", report.Findings)
			}
			var workflow uploadWorkflow
			if err := yaml.Unmarshal([]byte(source), &workflow); err != nil {
				t.Fatal(err)
			}
			events := make([]string, 0, len(workflow.On))
			for event := range workflow.On {
				events = append(events, event)
			}
			slices.Sort(events)
			if !slices.Equal(events, []string{"schedule", "workflow_dispatch"}) {
				t.Errorf("runs on %v, want a schedule and a manual run alone", events)
			}
			if workflow.Permissions == nil || len(workflow.Permissions) != 0 {
				t.Errorf("the workflow's permissions are %v, want {}", workflow.Permissions)
			}
			var read []string
			sends := 0
			for jobName, job := range workflow.Jobs {
				if job.TimeoutMinutes <= 0 {
					t.Errorf("job %s has no timeout", jobName)
				}
				if len(job.Permissions) != 1 || job.Permissions["actions"] != "read" {
					t.Errorf("job %s holds %v, want actions: read alone", jobName, job.Permissions)
				}
				for _, step := range job.Steps {
					for key, value := range step.With {
						if strings.Contains(value, "secrets.") {
							t.Errorf("step %q passes a secret as the input %s", step.Name, key)
						}
					}
					for key, value := range step.Env {
						if strings.Contains(value, "secrets.") {
							if value != "${{ secrets."+key+" }}" {
								t.Errorf("step %q reads %s as %q, want one secret of its own name", step.Name, key, value)
							}
							read = append(read, key)
						}
					}
					if !strings.Contains(step.Run, "curl") {
						continue
					}
					sends++
					if step.Name != want.send {
						t.Errorf("the step %q runs curl, want %q", step.Name, want.send)
					}
					for _, flag := range []string{"--fail-with-body", "--header @-", "--proto '=https'", `[ "$code" != ` + want.status + " ]", `"` + want.address + `"`} {
						if !strings.Contains(step.Run, flag) {
							t.Errorf("step %q's curl lacks %s", step.Name, flag)
						}
					}
				}
			}
			slices.Sort(read)
			if secrets := slices.Sorted(slices.Values(want.secrets)); !slices.Equal(read, secrets) {
				t.Errorf("the steps read the secrets %v, want %v", read, secrets)
			}
			if sends != 1 {
				t.Errorf("%d steps run curl, want one", sends)
			}
		})
	}
}

// uploadStep is the script of an upload recipe's step, by its name.
func uploadStep(t *testing.T, recipe, name string) string {
	t.Helper()
	var workflow uploadWorkflow
	if err := yaml.Unmarshal([]byte(read(t, filepath.Join(recipesDir, recipe))), &workflow); err != nil {
		t.Fatal(err)
	}
	for _, step := range workflow.Jobs["upload"].Steps {
		if step.Name == name {
			return step.Run
		}
	}
	t.Fatalf("%s has no step %q", recipe, name)
	return ""
}

// runScript runs a step's script as GitHub's runner runs a bash step.
func (s *shell) runScript(script string, env ...string) (int, string) {
	s.t.Helper()
	command := exec.Command("bash", "--noprofile", "--norc", "-eo", "pipefail", "-c", script)
	command.Dir = s.root
	command.Env = append([]string{"PATH=" + s.bin + ":" + os.Getenv("PATH"), "HOME=" + s.root}, env...)
	var output bytes.Buffer
	command.Stdout, command.Stderr = &output, &output
	err := command.Run()
	if err != nil && command.ProcessState == nil {
		s.t.Fatal(err)
	}
	return command.ProcessState.ExitCode(), output.String()
}

// writeZip writes an artifact's archive that holds the files named.
func writeZip(t *testing.T, path string, files map[string]string) {
	t.Helper()
	var buffer bytes.Buffer
	archive := zip.NewWriter(&buffer)
	for name, body := range files {
		file, err := archive.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := file.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := archive.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buffer.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestUploadRecipesGatherOnlyTheDaysOwnRows runs each recipe's gather step on
// an artifact list, on 2026-11-11 in UTC: a scheduled run reads the day
// before, the day's artifacts from this repository's runs give their rows,
// and a fork's artifact, another day's and one that kept no rows file give
// none. An expired artifact of the day stops the step, as does a day that is
// not over or is more than 6 days ago.
func TestUploadRecipesGatherOnlyTheDaysOwnRows(t *testing.T) {
	const day = "2026-11-10"
	realDate, err := exec.LookPath("date")
	if err != nil {
		t.Fatal(err)
	}
	listing := strings.Join([]string{
		"101\t2026-11-10T00:00:04Z\tfalse\t900\t900",
		"102\t2026-11-10T23:59:58Z\tfalse\t900\t900",
		"103\t2026-11-10T12:00:00Z\tfalse\t900\t777",
		"104\t2026-11-09T23:59:59Z\tfalse\t900\t900",
		"105\t2026-11-11T00:00:00Z\tfalse\t900\t900",
		"106\t2026-11-10T08:00:00Z\tfalse\t900\t900",
	}, "\n") + "\n"
	for recipe := range uploadRecipes {
		t.Run(recipe, func(t *testing.T) {
			s := newShell(t)
			s.stub("date", `if [ "$*" = "-u +%F" ]; then echo 2026-11-11; else exec "`+realDate+`" "$@"; fi`)
			zips := filepath.Join(s.root, "zips")
			if err := os.Mkdir(zips, 0o755); err != nil {
				t.Fatal(err)
			}
			for id, row := range map[string]string{"101": `{"run":101}` + "\n", "102": `{"run":102}` + "\n", "103": `{"fork":103}` + "\n", "104": `{"before":104}` + "\n", "105": `{"after":105}` + "\n"} {
				writeZip(t, filepath.Join(zips, id+".zip"), map[string]string{"costroid-rows.jsonl": row, "costroid-rows.run.json": "{}"})
			}
			writeZip(t, filepath.Join(zips, "106.zip"), map[string]string{"costroid-rows.run.json": `{"rows":0}`})
			if err := os.WriteFile(filepath.Join(s.root, "listing.tsv"), []byte(listing), 0o644); err != nil {
				t.Fatal(err)
			}
			s.stub("gh", `for arg; do case $arg in repos/*) path=$arg ;; esac; done
case $path in
  repos/example/app/actions/artifacts\?name=costroid-rows\&per_page=100) cat "`+s.root+`/listing.tsv" ;;
  repos/example/app/actions/artifacts/*/zip) id=${path%/zip}; id=${id##*/}; echo "$id" >> "`+s.root+`/downloaded"; cat "`+zips+`/$id.zip" ;;
  *) echo "unexpected gh $*" >&2; exit 2 ;;
esac`)
			output := filepath.Join(s.root, "github-output")
			env := []string{"COSTROID_DAY=", "RUNNER_TEMP=" + s.root, "GITHUB_OUTPUT=" + output, "GITHUB_REPOSITORY=example/app", "GH_TOKEN=a-token"}
			script := uploadStep(t, recipe, "Gather the day's rows")
			code, log := s.runScript(script, env...)
			if code != 0 {
				t.Fatalf("exit %d: %s", code, log)
			}
			rows := strings.Split(strings.TrimSpace(read(t, filepath.Join(s.root, "costroid-day", "rows.jsonl"))), "\n")
			slices.Sort(rows)
			if !slices.Equal(rows, []string{`{"run":101}`, `{"run":102}`}) {
				t.Errorf("the day's rows are %v", rows)
			}
			if got := read(t, output); got != "day="+day+"\n" {
				t.Errorf("the step's output is %q", got)
			}
			if got := strings.Fields(read(t, filepath.Join(s.root, "downloaded"))); !slices.Equal(got, []string{"101", "102", "106"}) {
				t.Errorf("downloaded %v, want the day's own artifacts alone", got)
			}
			if !strings.Contains(log, "left out artifact 103, which a fork's pull request kept") || !strings.Contains(log, "2 rows files were kept on "+day) {
				t.Errorf("the log reads %q", log)
			}

			expired := strings.Replace(listing, "102\t2026-11-10T23:59:58Z\tfalse", "102\t2026-11-10T23:59:58Z\ttrue", 1)
			if err := os.WriteFile(filepath.Join(s.root, "listing.tsv"), []byte(expired), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(output); err != nil {
				t.Fatal(err)
			}
			if code, log := s.runScript(script, env...); code == 0 || !strings.Contains(log, "artifact 102 of "+day+" has expired") {
				t.Errorf("an expired artifact: exit %d: %s", code, log)
			}
			if _, err := os.Stat(output); !os.IsNotExist(err) {
				t.Error("the step named a day after an artifact of it had expired")
			}

			for _, bad := range []string{"2026-11-31", "11/10/2026", "2026-11-11", "2026-11-04"} {
				if code, log := s.runScript(script, append(env, "COSTROID_DAY="+bad)...); code == 0 {
					t.Errorf("the day %s passed: %s", bad, log)
				}
			}
			if code, log := s.runScript(script, append(env, "COSTROID_DAY=2026-11-05")...); code != 0 || !strings.Contains(log, "0 rows files were kept on 2026-11-05") {
				t.Errorf("a day 6 days ago: exit %d: %s", code, log)
			}
		})
	}
}

// TestUploadRecipesSendTheTokenOnStandardInput runs each recipe's send step
// against a curl that records what it was handed: each credential reaches curl
// on standard input and never in its arguments, no secret reaches the log, the
// file goes to the platform's address, and an answer other than the status the
// platform gives for a file it took fails the step with the platform's errors.
func TestUploadRecipesSendTheTokenOnStandardInput(t *testing.T) {
	for recipe, want := range uploadRecipes {
		t.Run(recipe, func(t *testing.T) {
			s := newShell(t)
			s.stub("curl", `printf '%s\n' "$@" > "`+s.root+`/argv"
cat > "`+s.root+`/stdin"
while [ $# -gt 0 ]; do
  case $1 in --output) out=$2; shift ;; esac
  shift
done
printf '%s' "$STUB_ANSWER" > "$out"
printf '%s' "$STUB_CODE"
[ "$STUB_CODE" = "$STUB_TAKEN" ] || exit 22`)
			upload := filepath.Join(s.root, "costroid-upload")
			if err := os.Mkdir(upload, 0o755); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(upload, "costroid-example-app-2026-11-10.csv")
			if err := os.WriteFile(file, []byte("header\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			env := []string{"RUNNER_TEMP=" + s.root, "COSTROID_DAY=2026-11-10", "DD_SITE=datadoghq.eu", "STUB_TAKEN=" + want.status, "STUB_ANSWER={}"}
			for _, name := range want.secrets {
				env = append(env, name+"=value-of-"+name)
			}
			script := uploadStep(t, recipe, want.send)
			code, log := s.runScript(script, append(env, "STUB_CODE="+want.status)...)
			if code != 0 {
				t.Fatalf("exit %d: %s", code, log)
			}
			argv, stdin := read(t, filepath.Join(s.root, "argv")), read(t, filepath.Join(s.root, "stdin"))
			for _, name := range want.secrets {
				if strings.Contains(log, "value-of-"+name) {
					t.Errorf("the log holds %s: %s", name, log)
				}
			}
			for _, name := range want.credentials {
				if strings.Contains(argv, "value-of-"+name) || !strings.Contains(stdin, "value-of-"+name) {
					t.Errorf("%s is not on curl's standard input alone:\n%s", name, argv)
				}
			}
			address := strings.NewReplacer("$DD_SITE", "datadoghq.eu", "$VANTAGE_INTEGRATION_TOKEN", "value-of-VANTAGE_INTEGRATION_TOKEN").Replace(want.address)
			if !slices.Contains(strings.Split(argv, "\n"), address) || !strings.Contains(argv, "=@"+file) {
				t.Errorf("curl did not send the file to %s:\n%s", address, argv)
			}

			refusal := `{"errors":["Row 2: BilledCost must be a valid number"]}`
			code, log = s.runScript(script, append(env, "STUB_CODE=422", "STUB_ANSWER="+refusal)...)
			if code == 0 || !strings.Contains(log, "answered 422") || !strings.Contains(log, "Row 2: BilledCost must be a valid number") || strings.Contains(log, "value-of-") {
				t.Errorf("a refusal: exit %d: %s", code, log)
			}
			code, log = s.runScript(script, append(env, "STUB_CODE=200", "STUB_TAKEN=200")...)
			if code == 0 || !strings.Contains(log, "answered 200") {
				t.Errorf("an answer other than %s: exit %d: %s", want.status, code, log)
			}
		})
	}
}
