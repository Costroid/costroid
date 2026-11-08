package reconcile

import (
	"encoding/json"
	"testing"
)

// actorReport is a usage response grouped by actor, as the Usage and Cost
// Analytics API documents it: a request on a federation token has a null
// api_key_id and names the service account the token acts as.
const actorReport = `{"data":[
{"starting_at":"2026-10-14T00:00:00Z","group":{"api_key_id":null,"actor":{"type":"service_account_actor","service_account_id":"svac_01ExampleCi"}},"usage":{"output_tokens":7}},
{"starting_at":"2026-10-14T00:00:00Z","group":{"api_key_id":null,"actor":{"type":"service_account_actor","service_account_id":"svac_02ExampleOther"}},"usage":{"output_tokens":9}},
{"starting_at":"2026-10-14T00:00:00Z","group":{"api_key_id":null,"actor":{"type":"user_actor","user_id":"user_01Example","deleted":false,"email_address":null,"name":null}},"usage":{"output_tokens":11}},
{"starting_at":"2026-10-14T00:00:00Z","group":{"api_key_id":null,"actor":null},"usage":{"output_tokens":13}}
],"has_more":false,"next_page":null,"data_refreshed_at":"2026-10-15T08:00:00Z"}`

func TestASavedReportKeepsTheServiceAccountsOwnRecords(t *testing.T) {
	cut, err := keepOnly([]byte(actorReport), []string{"svac_01ExampleCi"})
	if err != nil {
		t.Fatal(err)
	}
	var kept struct {
		Data []struct {
			Usage struct {
				OutputTokens int `json:"output_tokens"`
			} `json:"usage"`
		} `json:"data"`
	}
	if err := json.Unmarshal(cut, &kept); err != nil {
		t.Fatal(err)
	}
	if len(kept.Data) != 1 || kept.Data[0].Usage.OutputTokens != 7 {
		t.Errorf("kept %+v, want the one record of svac_01ExampleCi", kept.Data)
	}
}
