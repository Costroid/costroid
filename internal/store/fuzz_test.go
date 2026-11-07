package store

import (
	"strings"
	"testing"
)

// FuzzParseTenantID reads a tenant id, which SET LOCAL takes as text because
// it takes no bind parameter. An id read is the canonical lower-case UUID it
// was given, and the statement built from it holds it as one quoted literal.
func FuzzParseTenantID(f *testing.F) {
	f.Add("0b6f3a52-7d3e-4c1a-9a8e-2f4b5c6d7e8f")
	f.Add("0B6F3A52-7D3E-4C1A-9A8E-2F4B5C6D7E8F")
	f.Add("0b6f3a52-7d3e-4c1a-9a8e-2f4b5c6d7e8f'; RESET ROLE; --")
	f.Add("{0b6f3a52-7d3e-4c1a-9a8e-2f4b5c6d7e8f}")
	f.Add("0b6f3a527d3e4c1a9a8e2f4b5c6d7e8f")
	f.Add("0b6f3a52-7d3e-4c1a-9a8e-2f4b5c6d7e8f\n")
	f.Fuzz(func(t *testing.T, text string) {
		tenant, err := ParseTenantID(text)
		if err != nil {
			if tenant.String() != "" {
				t.Fatalf("a refused id kept %q", tenant.String())
			}
			return
		}
		if tenant.String() != text || len(text) != 36 || text != strings.ToLower(text) {
			t.Fatalf("%q read as %q", text, tenant.String())
		}
		for index, r := range text {
			dash := index == 8 || index == 13 || index == 18 || index == 23
			if dash != (r == '-') || (!dash && !strings.ContainsRune("0123456789abcdef", r)) {
				t.Fatalf("%q holds %q at %d", text, r, index)
			}
		}
		statement := setTenant(tenant)
		if statement != "SET LOCAL costroid.tenant_id = '"+text+"'" || strings.Count(statement, "'") != 2 {
			t.Fatalf("the statement reads %q", statement)
		}
	})
}
