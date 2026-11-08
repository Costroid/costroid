package ghaw

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"regexp"

	"github.com/Costroid/costroid/internal/adapter"
	"github.com/Costroid/costroid/internal/decimal"
)

// runInfoFile is the part of aw_info.json the reader uses: the AI-credit cap
// the workflow was compiled with.
type runInfoFile struct {
	ModelConfig *struct {
		MaxAICredits json.Number `json:"max_ai_credits"`
	} `json:"model_config"`
}

// statedLimit is the AI-credit cap aw_info.json states for the run, nil when
// neither artifact holds an aw_info.json that states one. gh-aw's activation
// job uploads aw_info.json as the info artifact, and its conclusion job copies
// it into the usage artifact only for some workflows, so either may hold it.
// Two caps that differ name neither, and the warning says so.
func statedLimit(usage, info fs.FS) (*decimal.Decimal, string, error) {
	limit, err := readLimit(usage, runInfo)
	if err != nil || info == nil {
		return limit, "", err
	}
	other, err := readLimit(info, InfoArtifact+"/"+runInfo)
	switch {
	case err != nil:
		return nil, "", err
	case limit == nil:
		return other, "", nil
	case other != nil && limit.Cmp(*other) != 0:
		return nil, fmt.Sprintf("the usage artifact's aw_info.json states a cap of %s AI credits and the info artifact's %s, so no cap is named", limit, other), nil
	}
	return limit, "", nil
}

// readLimit is the cap the aw_info.json in dir states, nil when dir holds
// none or it states none. name is the file as an error names it.
func readLimit(dir fs.FS, name string) (*decimal.Decimal, error) {
	data, err := readAll(dir, runInfo)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var info runInfoFile
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&info); err != nil {
		return nil, fmt.Errorf("gh-aw: read %s: %w", name, err)
	}
	if info.ModelConfig == nil || info.ModelConfig.MaxAICredits == "" {
		return nil, nil
	}
	limit, err := decimal.Parse(info.ModelConfig.MaxAICredits.String())
	if err != nil || limit.Sign() <= 0 {
		return nil, fmt.Errorf("gh-aw: %s states max_ai_credits %s, which is not a count of credits above 0", name, info.ModelConfig.MaxAICredits)
	}
	return &limit, nil
}

// capFormat names the cap aw_info.json states, and capPattern reads it back
// from the description of a row whose note carries it. gh-aw's firewall
// checks the cap before it forwards each request, against the credits of the
// responses that came back, so the request that crosses it completes and a
// run can end past it (getAiCreditsBlockState and enforceGuards in
// containers/api-proxy of github/gh-aw-firewall v0.29.0, the firewall gh-aw
// v1.0.1 runs). The note names the cap and never says it held the run.
const capFormat = "a cap of %s AI credits that gh-aw checks, priced by gh-aw, not by Costroid"

var capPattern = regexp.MustCompile(`(?:^|; )none: a cap of ([0-9]{1,26}(?:\.[0-9]{1,32})?) AI credits that gh-aw checks, priced by gh-aw, not by Costroid(?:;|$)`)

// ProxyCap is the AI-credit cap a gh-aw row's description states, as the
// grade's note wrote it, and false when it states none.
func ProxyCap(description string) (string, bool) {
	match := capPattern.FindStringSubmatch(description)
	if match == nil {
		return "", false
	}
	return match[1], true
}

// CapSentence names the cap gh-aw checks. When gh-aw's own credits for the
// run, stated, pass it, the sentence also states them and by how much, in
// credits, with every digit. stated is nil when gh-aw states no credits, and a
// limit or a figure that is not a decimal states nothing past the cap.
func CapSentence(limit string, stated *adapter.Stated) string {
	sentence := fmt.Sprintf(capFormat, limit)
	if stated == nil || stated.Unit != adapter.UnitAICredits || stated.Source != Name {
		return sentence
	}
	capped, err := decimal.Parse(limit)
	if err != nil {
		return sentence
	}
	credits, err := decimal.Parse(stated.Quantity)
	if err != nil || credits.Cmp(capped) <= 0 {
		return sentence
	}
	return sentence + fmt.Sprintf("; gh-aw stated %s AI credits, over the cap by %s AI credits", credits, credits.Sub(capped))
}

// enforcement is the run's grade. Costroid did not start the run and minted no
// cap, so the grade is none whatever cap gh-aw checks. Its note names that cap
// when the artifact states it, and how far gh-aw's own credits passed it.
func enforcement(limit *decimal.Decimal, stated *adapter.Stated) adapter.Enforcement {
	note := noneNote
	if limit != nil {
		note = "none: " + CapSentence(limit.String(), stated)
	}
	return adapter.Enforcement{Grade: adapter.GradeNone, Note: note}
}
