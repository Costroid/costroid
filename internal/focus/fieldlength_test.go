// SPDX-License-Identifier: Apache-2.0
// Copyright 2026 The Costroid Authors

package focus

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

// TestFieldLengthBoundRejectsOverBoundValue proves the persistence-boundary size
// guard (COSTROID-FieldLength-001): a single field value one byte over the bound
// is the only violation, and a value exactly at the bound passes (the check is
// strictly greater-than).
func TestFieldLengthBoundRejectsOverBoundValue(t *testing.T) {
	raw := validRaw()
	raw["ChargeDescription"] = strings.Repeat("a", MaxFreeTextBytes+1)
	got := Validate(raw, DefaultRules())
	if len(got) != 1 {
		t.Fatalf("over-bound value violations = %v, want exactly one", got)
	}
	if got[0].RuleID != "COSTROID-FieldLength-001" {
		t.Errorf("RuleID = %q, want COSTROID-FieldLength-001", got[0].RuleID)
	}

	raw["ChargeDescription"] = strings.Repeat("a", MaxFreeTextBytes)
	if atBound := Validate(raw, DefaultRules()); len(atBound) != 0 {
		t.Fatalf("value exactly at the bound produced violations: %v", atBound)
	}
}

// TestFieldLengthBoundFiresOnRequiredColumn proves coverage is not limited to a
// hand-picked few columns: an over-bound value in a required, connector-controlled
// column (ServiceName) is rejected. The value is non-empty, so the not-null rule
// does not fire; the field-length rule is the sole violation.
func TestFieldLengthBoundFiresOnRequiredColumn(t *testing.T) {
	raw := validRaw()
	raw["ServiceName"] = strings.Repeat("s", MaxFreeTextBytes+1)
	got := Validate(raw, DefaultRules())
	if len(got) != 1 || got[0].RuleID != "COSTROID-FieldLength-001" {
		t.Fatalf("over-bound ServiceName violations = %v, want one COSTROID-FieldLength-001", got)
	}
}

// TestFieldLengthBoundLegitLargeResourceIDPasses proves a worst-case-but-legit
// value passes: a ~2 KB AWS ARN clears the 8 KiB bound with wide margin. The
// length is an absolute literal (not MaxFreeTextBytes-relative) so lowering the
// bound reddens this test, proving the guard is actually applied.
func TestFieldLengthBoundLegitLargeResourceIDPasses(t *testing.T) {
	raw := validRaw()
	raw["ResourceId"] = "arn:aws:ec2:us-east-1:123456789012:instance/" + strings.Repeat("x", 2000)
	if got := Validate(raw, DefaultRules()); len(got) != 0 {
		t.Fatalf("legit ~2 KB ResourceId produced violations: %v", got)
	}
}

// TestFieldLengthBoundTagValueOverBoundRejected proves a single over-bound Tags
// string value is caught. The cell still parses as KeyValueFormat, so CAU-Tags is
// silent and the field-length rule is the sole violation.
func TestFieldLengthBoundTagValueOverBoundRejected(t *testing.T) {
	raw := validRaw()
	cell, err := json.Marshal(map[string]string{"user:note": strings.Repeat("p", MaxFreeTextBytes+1)})
	if err != nil {
		t.Fatalf("marshaling test Tags: %v", err)
	}
	raw["Tags"] = string(cell)
	got := Validate(raw, DefaultRules())
	if len(got) != 1 || got[0].RuleID != "COSTROID-FieldLength-001" {
		t.Fatalf("over-bound Tags value violations = %v, want one COSTROID-FieldLength-001", got)
	}
}

// TestFieldLengthBoundManySmallTagsPass proves the per-value (not per-cell) design:
// a heavily-tagged resource whose serialized Tags cell is well over the bound, but
// whose every key and every value is short, passes. Bounding the whole cell would
// wrongly reject this legitimate record.
func TestFieldLengthBoundManySmallTagsPass(t *testing.T) {
	raw := validRaw()
	m := make(map[string]string, 50)
	for i := 0; i < 50; i++ {
		m[fmt.Sprintf("user:tag-%02d", i)] = strings.Repeat("v", 180)
	}
	cell, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshaling test Tags: %v", err)
	}
	if len(cell) <= MaxFreeTextBytes {
		t.Fatalf("test setup: serialized Tags cell is %d bytes, want > %d to prove per-value bounding", len(cell), MaxFreeTextBytes)
	}
	raw["Tags"] = string(cell)
	if got := Validate(raw, DefaultRules()); len(got) != 0 {
		t.Fatalf("large but per-value-legit Tags cell (%d bytes) produced violations: %v", len(cell), got)
	}
}

// TestFieldLengthBoundNormalRecordPasses is the baseline: an ordinary conformant
// record produces zero violations, so the new rule adds no false positives.
func TestFieldLengthBoundNormalRecordPasses(t *testing.T) {
	if got := Validate(validRaw(), DefaultRules()); len(got) != 0 {
		t.Fatalf("normal record produced violations: %v", got)
	}
}
