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
// the artifact holds no aw_info.json or it states no cap.
func statedLimit(dir fs.FS) (*decimal.Decimal, error) {
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
		return nil, fmt.Errorf("gh-aw: read %s: %w", runInfo, err)
	}
	if info.ModelConfig == nil || info.ModelConfig.MaxAICredits == "" {
		return nil, nil
	}
	limit, err := decimal.Parse(info.ModelConfig.MaxAICredits.String())
	if err != nil || limit.Sign() <= 0 {
		return nil, fmt.Errorf("gh-aw: %s states max_ai_credits %s, which is not a count of credits above 0", runInfo, info.ModelConfig.MaxAICredits)
	}
	return &limit, nil
}

// proxyNoteFormat is the grade's note when the artifact states the cap, and
// proxyNotePattern reads the cap back from the description of a row that
// carries it.
const proxyNoteFormat = "none: gh-aw's proxy held this run to %s AI credits, priced by gh-aw"

var proxyNotePattern = regexp.MustCompile(`(?:^|; )none: gh-aw's proxy held this run to ([0-9]{1,26}(?:\.[0-9]{1,32})?) AI credits, priced by gh-aw(?:;|$)`)

// ProxyCap is the AI-credit cap a gh-aw row's description states, as the
// grade's note wrote it, and false when it states none.
func ProxyCap(description string) (string, bool) {
	match := proxyNotePattern.FindStringSubmatch(description)
	if match == nil {
		return "", false
	}
	return match[1], true
}

// enforcement is the run's grade. Costroid did not start the run and minted no
// cap, so the grade is none whatever cap gh-aw's proxy held. Its note names
// that cap when the artifact states it.
func enforcement(limit *decimal.Decimal) adapter.Enforcement {
	note := noneNote
	if limit != nil {
		note = fmt.Sprintf(proxyNoteFormat, limit)
	}
	return adapter.Enforcement{Grade: adapter.GradeNone, Note: note}
}
