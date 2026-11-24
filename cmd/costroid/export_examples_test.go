package main

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
)

// example is one of the contract's examples, held to the SHA-256 of the file
// the contract published.
func example(t *testing.T, name string) []byte {
	t.Helper()
	held, found := contractExamples[name]
	if !found {
		t.Fatalf("no example %s is held", name)
	}
	if sum := sha256.Sum256([]byte(held.body)); hex.EncodeToString(sum[:]) != held.sha256 {
		t.Fatalf("the example %s is not the published file", name)
	}
	return []byte(held.body)
}

// The examples of integration/v0.7 of Costroid/costroid-schemas that the
// senders' tests send and read, each as the published file holds it, byte for
// byte, under its path in the version's examples directory. Each is held to
// the SHA-256 of the published file by TestTheSendersHoldTheContractsOwnExamples.
var contractExamples = map[string]struct {
	sha256, body string
}{
	"Identity.json": {
		sha256: "4271ec30826ce53d81093823ecc03fea99bcde9c0ddb338f6d8b8803432d2e33",
		body: `{
  "contract": { "version": "0.7", "tag_set": "tags/v0.5", "focus_row": "focus-row/v0.6" },
  "tenant": { "id": "org_01JEXAMPLE0001", "name": "Example Inc", "region": "eu" },
  "account": { "id": "acct_01JEXAMPLE0001", "role": "admin" },
  "token": {"id":"tok_01JEXAMPLE0001","scopes":["identity:read","rows:write","merges:write","credentials:write","reconciliations:write","quotas:read"],"created_at":"2026-10-01T08:00:00Z","expires_at":"2027-01-01T08:00:00Z","last_used_at":"2026-10-14T11:20:03Z"},
  "installations": [
    { "id": "github:58120394", "tenant_id": "org_01JEXAMPLE0001", "owner_id": "7204411", "status": "active", "proof_kind": "app_attestation", "bound_at": "2026-10-01T08:02:11Z", "proven_at": "2026-10-01T08:02:00Z", "ended_at": null }
  ]
}
`,
	},
	"CredentialAttestationBody.json": {
		sha256: "4df88748eb27c192b53231b11a151ae53b0212258368d6dc86c7e790ffdd23d9",
		body: `{
  "run_id": "16873409921",
  "run_attempt": "1",
  "head_sha": "4b1e0c9d27f35a86e01c7d4f98b2a6e3c5d70f12"
}
`,
	},
	"CredentialSpendBody.json": {
		sha256: "7ef0f70fb09b26ab6465788bd5f973138001759ad1573b1cf2640d2d81454d9f",
		body: `{
  "run_id": "16873409921",
  "run_attempt": "1",
  "currency": "USD",
  "spend": "0.412"
}
`,
	},
	"CredentialAttestation.json": {
		sha256: "47406b1a488bfef4ad89ac684f9ba9d47fadc7dc7f20ed9f8a091c11d63dd009",
		body: `{
  "key_id": "f87edbb88470bc20b5b701ece0500b4bd75f061b5547255cb6518fd2eaa4e778",
  "repo": "github.com/example/widgets",
  "run_id": "16873409921",
  "run_attempt": "1",
  "head_sha": "4b1e0c9d27f35a86e01c7d4f98b2a6e3c5d70f12",
  "attested_by": {
    "run_id": "16873410388",
    "run_attempt": "1",
    "ref": "refs/heads/main"
  }
}
`,
	},
	"CredentialSpend.json": {
		sha256: "d8607c7d9a044dd6a8244619d4e6c330a9d549ef3fee84e0ab5e008596805d68",
		body: `{
  "key_id": "f87edbb88470bc20b5b701ece0500b4bd75f061b5547255cb6518fd2eaa4e778",
  "repo": "github.com/example/widgets",
  "run_id": "16873409921",
  "run_attempt": "1",
  "currency": "USD",
  "spend": "0.412",
  "attested_by": {
    "run_id": "16873452107",
    "run_attempt": "1",
    "ref": "refs/heads/main"
  }
}
`,
	},
	"CredentialRunTokenClaims.json": {
		sha256: "4bcc8384ba0f659f53381ad9982ec27ae0c3fe8af244628413c39d81b32336e3",
		body: `{
  "iss": "https://token.actions.githubusercontent.com",
  "aud": "https://app.costroid.com/tenants/org_01JEXAMPLE0001/credentials/f87edbb88470bc20b5b701ece0500b4bd75f061b5547255cb6518fd2eaa4e778/16873409921/1",
  "iat": 1791970020,
  "exp": 1791970320,
  "jti": "example-61c0d8a2",
  "sub": "repo:example/widgets:ref:refs/heads/main",
  "repository": "example/widgets",
  "repository_id": "903114",
  "repository_owner": "example",
  "repository_owner_id": "7204411",
  "run_id": "16873410388",
  "run_attempt": "1",
  "sha": "2d7c41e90b8fa365c1e04d9b7a26f3e58c0d1b94",
  "ref": "refs/heads/main",
  "ref_type": "branch",
  "ref_protected": "true",
  "event_name": "workflow_run",
  "workflow_ref": "example/widgets/.github/workflows/costroid-gateway.yml@refs/heads/main"
}
`,
	},
	"CredentialRunTokenClaims.spend.json": {
		sha256: "c59a46e41d682c98202b8e6384715f64e4f888aa2fc82f6c78d3d95fc9624f47",
		body: `{
  "iss": "https://token.actions.githubusercontent.com",
  "aud": "https://app.costroid.com/tenants/org_01JEXAMPLE0001/credentials/f87edbb88470bc20b5b701ece0500b4bd75f061b5547255cb6518fd2eaa4e778/16873409921/1/spend",
  "iat": 1791971220,
  "exp": 1791971520,
  "jti": "example-9e27b4f5",
  "sub": "repo:example/widgets:ref:refs/heads/main",
  "repository": "example/widgets",
  "repository_id": "903114",
  "repository_owner": "example",
  "repository_owner_id": "7204411",
  "run_id": "16873452107",
  "run_attempt": "1",
  "sha": "2d7c41e90b8fa365c1e04d9b7a26f3e58c0d1b94",
  "ref": "refs/heads/main",
  "ref_type": "branch",
  "ref_protected": "true",
  "event_name": "workflow_run",
  "workflow_ref": "example/widgets/.github/workflows/costroid-gateway.yml@refs/heads/main"
}
`,
	},
	"KeyId.json": {
		sha256: "cc7dc7b39f18c0dbd35b63648c031b27d82333b2c6373018602d9ea1a02f3c80",
		body: `"f87edbb88470bc20b5b701ece0500b4bd75f061b5547255cb6518fd2eaa4e778"
`,
	},
	"Error.not-mounted.json": {
		sha256: "70c2c56d1ce894c5020ab8a6bea7b081e00fb437fc39453ce478c431820514ae",
		body: `{
  "error": {
    "code": "NOT_FOUND",
    "message": "No such route."
  }
}
`,
	},
	"CredentialRefusal.AUTH_TOKEN_SCOPE.json": {
		sha256: "02324efa00e9d4990966dc5e337d979e40082e4c45d36b6e135e3a72aace1b32",
		body: `{
  "error": {
    "code": "AUTH_TOKEN_SCOPE",
    "message": "The token lacks the credentials:write scope."
  }
}
`,
	},
	"CredentialRefusal.CREDENTIAL_ATTESTED_ELSEWHERE.json": {
		sha256: "8555aa912f022f9f68637440aea9111205d3070422eaed42bb63ba8f2a75f247",
		body: `{
  "error": {
    "code": "CREDENTIAL_ATTESTED_ELSEWHERE",
    "message": "The key is attested for run 16873409921, attempt 1."
  }
}
`,
	},
	"CredentialRefusal.CREDENTIAL_ATTESTING_RUN_USED.json": {
		sha256: "c068a6f4b5bbcf97e1d581fc85f274d123475bbd8e834bdfd3713b837dd16972",
		body: `{
  "error": {
    "code": "CREDENTIAL_ATTESTING_RUN_USED",
    "message": "Run 16873410388, attempt 1, has attested a key already."
  }
}
`,
	},
	"CredentialRefusal.CREDENTIAL_NOT_ATTESTED.json": {
		sha256: "3451e163bf5e3c9e297649daa82371afc1107bf761eb4f1966cf4d16b531c614",
		body: `{
  "error": {
    "code": "CREDENTIAL_NOT_ATTESTED",
    "message": "The key holds no attestation for this run and attempt, so no figure is taken."
  }
}
`,
	},
	"CredentialRefusal.CREDENTIAL_RUN_UNPROTECTED.json": {
		sha256: "49ce0315666d753c2e57b84f725664e766076d34478594a65a3ead0a9ef9b969",
		body: `{
  "error": {
    "code": "CREDENTIAL_RUN_UNPROTECTED",
    "message": "The run token's event_name is pull_request. A key is attested by a workflow_run run of a protected branch."
  }
}
`,
	},
	"CredentialRefusal.CREDENTIAL_SPEND_HELD.json": {
		sha256: "4a638ab5fea3c5bf2f18649ee99a9bafcf49e192e4e76d4cb6bbd8edd1298891",
		body: `{
  "error": {
    "code": "CREDENTIAL_SPEND_HELD",
    "message": "The key holds a spend already."
  }
}
`,
	},
	"CredentialRefusal.INSTALLATION_NOT_BOUND.json": {
		sha256: "6b02642ddfb1bf9ff094494b662eabd7cee1827b37001c1d9684427c65a0ff07",
		body: `{
  "error": {
    "code": "INSTALLATION_NOT_BOUND",
    "message": "This tenant holds no active binding of github-owner:7204411."
  }
}
`,
	},
	"CredentialRefusal.INSTALLATION_PROOF_KEYS_UNAVAILABLE.json": {
		sha256: "738f6d8ff1e3a71cc3ed53090e9b5d6146b9cb80fd1b655f1d0b344b6aadf4ff",
		body: `{
  "error": {
    "code": "INSTALLATION_PROOF_KEYS_UNAVAILABLE",
    "message": "The issuer's key set cannot be read. Retry inside the proof's window."
  }
}
`,
	},
	"CredentialRefusal.RUN_TOKEN_INVALID.json": {
		sha256: "37d31cd5d2beabaf64f0a084e291cdd0691101487f3fe8c8716c4f978abd4d8b",
		body: `{
  "error": {
    "code": "RUN_TOKEN_INVALID",
    "message": "The run token is not valid for this attestation's audience."
  }
}
`,
	},
	"CredentialRefusal.RUN_TOKEN_INVALID.spend.json": {
		sha256: "e5c7939189b44d2dd0f0004b360e05325d2dddb94577879852d15f58a94d3238",
		body: `{
  "error": {
    "code": "RUN_TOKEN_INVALID",
    "message": "The run token is not valid for this spend's audience."
  }
}
`,
	},
	"CredentialRefusal.RUN_TOKEN_OWNER_MISMATCH.json": {
		sha256: "2bc3493e137f98a275041d258878a06a9c7af21ee4a8b8cfbab7ac297062e03a",
		body: `{
  "error": {
    "code": "RUN_TOKEN_OWNER_MISMATCH",
    "message": "The run token names another owner than github-owner:7204411."
  }
}
`,
	},
	"CredentialRefusal.TOOL_INPUT_INVALID.json": {
		sha256: "b8903e7272396095f5c60f8d131ea47552fb593f991db00278bae60f935a8570",
		body: `{
  "error": {
    "code": "TOOL_INPUT_INVALID",
    "message": "The key id in the path is malformed, a header is missing or malformed, or the body is not an attestation of this version."
  }
}
`,
	},
	"CredentialRefusal.TOOL_INPUT_INVALID.spend.json": {
		sha256: "e3a794552218b9b1eee262e29b6acf74c2d1be102fb18d4df81758ac8d04532b",
		body: `{
  "error": {
    "code": "TOOL_INPUT_INVALID",
    "message": "The key id in the path is malformed, a header is missing or malformed, or the body is not a spend of this version."
  }
}
`,
	},
	"IngestCase.a-stated-pull-request-under-pull-request-target.json": {
		sha256: "40497bce4c76cb5358fbe48de0eab06160c02526d95e029df6f89461234f8fec",
		body: `{
  "run": {
    "iss": "https://token.actions.githubusercontent.com",
    "aud": "https://app.costroid.com/tenants/org_01JEXAMPLE0001/rows",
    "iat": 1791970800,
    "exp": 1791971100,
    "jti": "example-1e5b9c30",
    "sub": "repo:example/widgets:pull_request",
    "repository": "example/widgets",
    "repository_id": "903114",
    "repository_owner": "example",
    "repository_owner_id": "7204411",
    "run_id": "16873421764",
    "run_attempt": "1",
    "sha": "2d7c41e90b8fa365c1e04d9b7a26f3e58c0d1b94",
    "ref": "refs/heads/main",
    "event_name": "pull_request_target"
  },
  "rows": [
    {
      "costroid/repo": "github.com/example/widgets",
      "costroid/pr": "1423",
      "costroid/commit_sha": "4b1e0c9d27f35a86e01c7d4f98b2a6e3c5d70f12",
      "costroid/workflow_run_id": "16873421764",
      "costroid/agent": "example-agent",
      "costroid/model": "example-large-1",
      "costroid/cost_source": "agent_stream",
      "costroid/cost_basis": "measured",
      "costroid/attribution_grade": "exact",
      "costroid/enforcement_grade": "gateway-precheck",
      "costroid/run_identity": "token_verified"
    }
  ],
  "refusal": null,
  "effect": "ingested"
}
`,
	},
	"IngestCase.rows-of-the-tokens-pull-request.json": {
		sha256: "980447bc50cda7ee9a69b4a3e6eead7206b6eba8cebd69f6d82175286c711c46",
		body: `{
  "run": {
    "iss": "https://token.actions.githubusercontent.com",
    "aud": "https://app.costroid.com/tenants/org_01JEXAMPLE0001/rows",
    "iat": 1791970800,
    "exp": 1791971100,
    "jti": "example-7c41d2e9",
    "sub": "repo:example/widgets:pull_request",
    "repository": "example/widgets",
    "repository_id": "903114",
    "repository_owner": "example",
    "repository_owner_id": "7204411",
    "run_id": "16873409921",
    "run_attempt": "1",
    "sha": "5b0e2f7c1d9a84e36f2c0b1a7d4e9f3c8a6b2d10",
    "ref": "refs/pull/1423/merge",
    "event_name": "pull_request"
  },
  "rows": [
    {
      "costroid/repo": "github.com/example/widgets",
      "costroid/pr": "1423",
      "costroid/commit_sha": "4b1e0c9d27f35a86e01c7d4f98b2a6e3c5d70f12",
      "costroid/workflow_run_id": "16873409921",
      "costroid/agent": "example-agent",
      "costroid/model": "example-large-1",
      "costroid/cost_source": "agent_stream",
      "costroid/cost_basis": "measured",
      "costroid/attribution_grade": "exact",
      "costroid/enforcement_grade": "gateway-precheck",
      "costroid/run_identity": "token_verified"
    },
    {
      "costroid/repo": "github.com/example/widgets",
      "costroid/pr": "1423",
      "costroid/commit_sha": "4b1e0c9d27f35a86e01c7d4f98b2a6e3c5d70f12",
      "costroid/workflow_run_id": "16873409921",
      "costroid/agent": "example-agent",
      "costroid/model": "example-large-1",
      "costroid/cost_source": "agent_stream",
      "costroid/cost_basis": "measured",
      "costroid/attribution_grade": "exact",
      "costroid/enforcement_grade": "gateway-precheck",
      "costroid/run_identity": "token_verified"
    }
  ],
  "refusal": null,
  "effect": "ingested"
}
`,
	},
	"rejected/CredentialAttestationBody.TOOL_INPUT_INVALID.pull-request-key.json": {
		sha256: "f2c035ba71acb60dcf87d690b8bbd45abb133cf30b2ddfca3d1cefc9d100a419",
		body: `{
  "run_id": "16873409921",
  "run_attempt": "1",
  "head_sha": "4b1e0c9d27f35a86e01c7d4f98b2a6e3c5d70f12",
  "pull_request": 1423
}
`,
	},
	"rejected/CredentialAttestationBody.TOOL_INPUT_INVALID.run-attempt-as-a-number.json": {
		sha256: "51781a23765d8a098223a2a399f934a47a0453c17e0e85b686d51d63da318d74",
		body: `{
  "run_id": "16873409921",
  "run_attempt": 1,
  "head_sha": "4b1e0c9d27f35a86e01c7d4f98b2a6e3c5d70f12"
}
`,
	},
	"rejected/CredentialAttestationBody.TOOL_INPUT_INVALID.short-head.json": {
		sha256: "829d6df009279c4ab15563232a55e8329cc10d94de7d46cd70f89eb569b0eeb4",
		body: `{
  "run_id": "16873409921",
  "run_attempt": "1",
  "head_sha": "4b1e0c9d27f3"
}
`,
	},
	"rejected/CredentialSpendBody.TOOL_INPUT_INVALID.another-currency.json": {
		sha256: "0c6bd960dc6857c4afe8c281e8b81771c9f3ed06cfa310c422702fd882059701",
		body: `{
  "run_id": "16873409921",
  "run_attempt": "1",
  "currency": "EUR",
  "spend": "0.412"
}
`,
	},
	"rejected/CredentialSpendBody.TOOL_INPUT_INVALID.exponent.json": {
		sha256: "e1eb14207893e5510b89e74d0e47a874bd2a1bae12d48972a44ef2453803e10a",
		body: `{
  "run_id": "16873409921",
  "run_attempt": "1",
  "currency": "USD",
  "spend": "4.12e-1"
}
`,
	},
	"rejected/CredentialSpendBody.TOOL_INPUT_INVALID.leading-zero.json": {
		sha256: "f3998e6cfc5c58ca264e8ba457b261714842f8cc8753c919ca86dd1aafb872d8",
		body: `{
  "run_id": "16873409921",
  "run_attempt": "1",
  "currency": "USD",
  "spend": "00.412"
}
`,
	},
	"rejected/CredentialSpendBody.TOOL_INPUT_INVALID.negative.json": {
		sha256: "26bfe21508732acdaabb3dcbfdb4ae349c5d7185b940529419f2e118e9d6488e",
		body: `{
  "run_id": "16873409921",
  "run_attempt": "1",
  "currency": "USD",
  "spend": "-0.412"
}
`,
	},
	"rejected/CredentialSpendBody.TOOL_INPUT_INVALID.spend-as-a-number.json": {
		sha256: "0fb809a38ffd78a04847860ad0d4c3004cf937096c1b374df9029c21f38e3373",
		body: `{
  "run_id": "16873409921",
  "run_attempt": "1",
  "currency": "USD",
  "spend": 0.412
}
`,
	},
	"rejected/CredentialSpendBody.TOOL_INPUT_INVALID.thirteen-places.json": {
		sha256: "1eec6b7c43a3c2e6d9ec19b1dad73dd7eb37db3fcaf41fa24b8a41e85bf24da9",
		body: `{
  "run_id": "16873409921",
  "run_attempt": "1",
  "currency": "USD",
  "spend": "0.4120000000001"
}
`,
	},
	"rejected/KeyId.TOOL_INPUT_INVALID.session-id.json": {
		sha256: "baa3a841986889ed533bb9306b7f8f2c0fb7bef0e0f5b9541be9ab72021a3a09",
		body: `"4f1c9a70-2b1e-4c55-9a1d-7be0a3c1d2ef"
`,
	},
	"rejected/KeyId.TOOL_INPUT_INVALID.upper-case.json": {
		sha256: "939d811139a91ef296d248fcb1942a6e1262f3ebaa3580e5d68cc4b68ae611a5",
		body: `"F87EDBB88470BC20B5B701ECE0500B4BD75F061B5547255CB6518FD2EAA4E778"
`,
	},
	"rejected/CredentialRunTokenClaims.RUN_TOKEN_INVALID.audience-by-prefix.json": {
		sha256: "5b8e6a5746c8e16fc9f61096c156fcd0f47cb8fbe674017d88e3ecd6e4278379",
		body: `{
  "iss": "https://token.actions.githubusercontent.com",
  "aud": "https://app.costroid.com/tenants/org_01JEXAMPLE0001/credentials/f87edbb88470bc20b5b701ece0500b4bd75f061b5547255cb6518fd2eaa4e778/16873409921/1/extra",
  "iat": 1791970020,
  "exp": 1791970320,
  "jti": "example-61c0d8a2",
  "sub": "repo:example/widgets:ref:refs/heads/main",
  "repository": "example/widgets",
  "repository_id": "903114",
  "repository_owner": "example",
  "repository_owner_id": "7204411",
  "run_id": "16873410388",
  "run_attempt": "1",
  "sha": "2d7c41e90b8fa365c1e04d9b7a26f3e58c0d1b94",
  "ref": "refs/heads/main",
  "ref_type": "branch",
  "ref_protected": "true",
  "event_name": "workflow_run",
  "workflow_ref": "example/widgets/.github/workflows/costroid-gateway.yml@refs/heads/main"
}
`,
	},
	"rejected/CredentialRunTokenClaims.RUN_TOKEN_INVALID.rows-audience.json": {
		sha256: "ba4415900cfd26a07cd098e8951a27d9e370aeed4d96c635714c2d3d9012e188",
		body: `{
  "iss": "https://token.actions.githubusercontent.com",
  "aud": "https://app.costroid.com/tenants/org_01JEXAMPLE0001/rows",
  "iat": 1791970020,
  "exp": 1791970320,
  "jti": "example-61c0d8a2",
  "sub": "repo:example/widgets:ref:refs/heads/main",
  "repository": "example/widgets",
  "repository_id": "903114",
  "repository_owner": "example",
  "repository_owner_id": "7204411",
  "run_id": "16873410388",
  "run_attempt": "1",
  "sha": "2d7c41e90b8fa365c1e04d9b7a26f3e58c0d1b94",
  "ref": "refs/heads/main",
  "ref_type": "branch",
  "ref_protected": "true",
  "event_name": "workflow_run",
  "workflow_ref": "example/widgets/.github/workflows/costroid-gateway.yml@refs/heads/main"
}
`,
	},
	"rejected/CredentialCase.RUN_TOKEN_INVALID.attestation-audience-on-the-spend-path.json": {
		sha256: "d644f66ee5b118db6f71791299c17de2c43b6aaa1672520ebe7acad648398508",
		body: `{
  "tenant_id": "org_01JEXAMPLE0001",
  "at": "2026-10-14T09:27:40Z",
  "scopes": [
    "credentials:write"
  ],
  "installation": {
    "id": "github-owner:7204411",
    "owner_id": "7204411",
    "bound": true
  },
  "keys_available": true,
  "signature_verifies": true,
  "write": "spend",
  "key_id": "f87edbb88470bc20b5b701ece0500b4bd75f061b5547255cb6518fd2eaa4e778",
  "body": {
    "run_id": "16873409921",
    "run_attempt": "1",
    "currency": "USD",
    "spend": "9.5"
  },
  "claims": {
    "iss": "https://token.actions.githubusercontent.com",
    "aud": "https://app.costroid.com/tenants/org_01JEXAMPLE0001/credentials/f87edbb88470bc20b5b701ece0500b4bd75f061b5547255cb6518fd2eaa4e778/16873409921/1",
    "iat": 1791970020,
    "exp": 1791970320,
    "jti": "example-61c0d8a2",
    "sub": "repo:example/widgets:ref:refs/heads/main",
    "repository": "example/widgets",
    "repository_id": "903114",
    "repository_owner": "example",
    "repository_owner_id": "7204411",
    "run_id": "16873410388",
    "run_attempt": "1",
    "sha": "2d7c41e90b8fa365c1e04d9b7a26f3e58c0d1b94",
    "ref": "refs/heads/main",
    "ref_type": "branch",
    "ref_protected": "true",
    "event_name": "workflow_run",
    "workflow_ref": "example/widgets/.github/workflows/costroid-gateway.yml@refs/heads/main"
  },
  "held": {
    "attestations": [
      {
        "key_id": "f87edbb88470bc20b5b701ece0500b4bd75f061b5547255cb6518fd2eaa4e778",
        "repo": "github.com/example/widgets",
        "run_id": "16873409921",
        "run_attempt": "1",
        "head_sha": "4b1e0c9d27f35a86e01c7d4f98b2a6e3c5d70f12",
        "attested_by": {
          "run_id": "16873410388",
          "run_attempt": "1",
          "ref": "refs/heads/main"
        }
      }
    ],
    "spends": []
  },
  "refusal": "RUN_TOKEN_INVALID",
  "effect": null
}
`,
	},
	"rejected/CredentialCase.RUN_TOKEN_INVALID.spend-audience-on-the-attestation-path.json": {
		sha256: "14b1da46233f276dc80fbbd82cb7c3bce6356f21734f605b034dfb4ff850e00f",
		body: `{
  "tenant_id": "org_01JEXAMPLE0001",
  "at": "2026-10-14T09:47:15Z",
  "scopes": [
    "credentials:write"
  ],
  "installation": {
    "id": "github-owner:7204411",
    "owner_id": "7204411",
    "bound": true
  },
  "keys_available": true,
  "signature_verifies": true,
  "write": "attestation",
  "key_id": "f87edbb88470bc20b5b701ece0500b4bd75f061b5547255cb6518fd2eaa4e778",
  "body": {
    "run_id": "16873409921",
    "run_attempt": "1",
    "head_sha": "4b1e0c9d27f35a86e01c7d4f98b2a6e3c5d70f12"
  },
  "claims": {
    "iss": "https://token.actions.githubusercontent.com",
    "aud": "https://app.costroid.com/tenants/org_01JEXAMPLE0001/credentials/f87edbb88470bc20b5b701ece0500b4bd75f061b5547255cb6518fd2eaa4e778/16873409921/1/spend",
    "iat": 1791971220,
    "exp": 1791971520,
    "jti": "example-9e27b4f5",
    "sub": "repo:example/widgets:ref:refs/heads/main",
    "repository": "example/widgets",
    "repository_id": "903114",
    "repository_owner": "example",
    "repository_owner_id": "7204411",
    "run_id": "16873452107",
    "run_attempt": "1",
    "sha": "2d7c41e90b8fa365c1e04d9b7a26f3e58c0d1b94",
    "ref": "refs/heads/main",
    "ref_type": "branch",
    "ref_protected": "true",
    "event_name": "workflow_run",
    "workflow_ref": "example/widgets/.github/workflows/costroid-gateway.yml@refs/heads/main"
  },
  "held": {
    "attestations": [],
    "spends": []
  },
  "refusal": "RUN_TOKEN_INVALID",
  "effect": null
}
`,
	},
	"rejected/RestatementCase.EXPORT_BATCH_RUN_BOUND.a-later-attempt.json": {
		sha256: "91dada657e7aa68eeeed23f59214d6b77bf0017f97c3c5ab1b1cec603ef418d7",
		body: `{
  "batch_id": "903114-16873409921-1",
  "held": {
    "token_verified": true,
    "run": {
      "repository": "example/widgets",
      "run_id": "16873409921",
      "run_attempt": "1"
    }
  },
  "run": {
    "repository": "example/widgets",
    "run_id": "16873409921",
    "run_attempt": "2"
  },
  "refusal": "EXPORT_BATCH_RUN_BOUND",
  "effect": null
}
`,
	},
	"rejected/IngestCase.ROW_IDENTITY_MISMATCH.another-pull-request.json": {
		sha256: "0e5a43ca52db177bce205d34baa2088e8fee311ce340af982b2707456cc0e047",
		body: `{
  "run": {
    "iss": "https://token.actions.githubusercontent.com",
    "aud": "https://app.costroid.com/tenants/org_01JEXAMPLE0001/rows",
    "iat": 1791970800,
    "exp": 1791971100,
    "jti": "example-7c41d2e9",
    "sub": "repo:example/widgets:pull_request",
    "repository": "example/widgets",
    "repository_id": "903114",
    "repository_owner": "example",
    "repository_owner_id": "7204411",
    "run_id": "16873409921",
    "run_attempt": "1",
    "sha": "5b0e2f7c1d9a84e36f2c0b1a7d4e9f3c8a6b2d10",
    "ref": "refs/pull/1423/merge",
    "event_name": "pull_request"
  },
  "rows": [
    {
      "costroid/repo": "github.com/example/widgets",
      "costroid/pr": "1423",
      "costroid/commit_sha": "4b1e0c9d27f35a86e01c7d4f98b2a6e3c5d70f12",
      "costroid/workflow_run_id": "16873409921",
      "costroid/agent": "example-agent",
      "costroid/model": "example-large-1",
      "costroid/cost_source": "agent_stream",
      "costroid/cost_basis": "measured",
      "costroid/attribution_grade": "exact",
      "costroid/enforcement_grade": "gateway-precheck",
      "costroid/run_identity": "token_verified"
    },
    {
      "costroid/repo": "github.com/example/widgets",
      "costroid/pr": "1424",
      "costroid/commit_sha": "4b1e0c9d27f35a86e01c7d4f98b2a6e3c5d70f12",
      "costroid/workflow_run_id": "16873409921",
      "costroid/agent": "example-agent",
      "costroid/model": "example-large-1",
      "costroid/cost_source": "agent_stream",
      "costroid/cost_basis": "measured",
      "costroid/attribution_grade": "exact",
      "costroid/enforcement_grade": "gateway-precheck",
      "costroid/run_identity": "token_verified"
    },
    {
      "costroid/repo": "github.com/example/widgets",
      "costroid/pr": "1424",
      "costroid/commit_sha": "4b1e0c9d27f35a86e01c7d4f98b2a6e3c5d70f12",
      "costroid/workflow_run_id": "16873409921",
      "costroid/agent": "example-agent",
      "costroid/model": "example-large-1",
      "costroid/cost_source": "agent_stream",
      "costroid/cost_basis": "measured",
      "costroid/attribution_grade": "exact",
      "costroid/enforcement_grade": "gateway-precheck",
      "costroid/run_identity": "token_verified"
    }
  ],
  "refusal": "ROW_IDENTITY_MISMATCH",
  "effect": null
}
`,
	},
	"rejected/FocusRow.ROW_ORIGIN_REFUSED.developer-machine.json": {
		sha256: "fc8ede3924dd861e7db56f9074f475d2c879135e6ce6f98d68e53551fb99e794",
		body: `{
  "BilledCost": "0",
  "BillingAccountId": "org-example-0001",
  "BillingAccountName": "Example Inc",
  "BillingCurrency": "USD",
  "BillingPeriodEnd": "2026-12-01T00:00:00Z",
  "BillingPeriodStart": "2026-11-01T00:00:00Z",
  "ChargeCategory": "Usage",
  "ChargeClass": null,
  "ChargeDescription": "example-large-1 cache_read tokens; the requests of one session on one model, summed, counted on the author's machine",
  "ChargeFrequency": "Usage-Based",
  "ChargePeriodEnd": "2026-11-21T00:00:00Z",
  "ChargePeriodStart": "2026-11-18T00:00:00Z",
  "ConsumedQuantity": "1520400",
  "ConsumedUnit": "Tokens",
  "ContractedCost": "0.501732",
  "ContractedUnitPrice": "0.33",
  "EffectiveCost": "0",
  "HostProviderName": "Example AI",
  "InvoiceIssuerName": "Example AI",
  "ListCost": "0.501732",
  "ListUnitPrice": "0.33",
  "PricingCategory": "Standard",
  "PricingQuantity": "1.5204",
  "PricingUnit": "1000000 Tokens",
  "RegionId": "us",
  "RegionName": "us",
  "ServiceCategory": "AI and Machine Learning",
  "ServiceName": "Example AI API",
  "ServiceProviderName": "Example AI",
  "ServiceSubcategory": "Generative AI",
  "SkuId": "example-ai/example-large-1/cache_read",
  "SkuMeter": "cache_read",
  "SkuPriceId": "example-ai/example-large-1/cache_read/standard",
  "SubAccountId": null,
  "SubAccountName": null,
  "Tags": {
    "costroid/repo": "github.com/example/widgets",
    "costroid/pr": "1423",
    "costroid/commit_sha": "9f2c6e1ab47d03f58c1e7726d0b4a93e5f618c2d",
    "costroid/session_id": "4f1c9a70-2b1e-4c55-9a1d-7be0a3c1d2ef",
    "costroid/agent": "example-agent",
    "costroid/agent_version": "2.1.4",
    "costroid/model": "example-large-1",
    "costroid/cost_source": "agent_session_file",
    "costroid/cost_basis": "list_equivalent",
    "costroid/attribution_grade": "unattributed",
    "costroid/enforcement_grade": "none",
    "costroid/run_identity": "unverified",
    "costroid/origin": "developer_machine"
  },
  "x_CostroidCatalogVersion": "0.1.0",
  "x_CostroidOvershootBound": null,
  "x_CostroidRowId": "4bfb2a24b7595fad435b6ef5753e309de3010f84474f725f1ba8c5bc74ab7d7f",
  "x_CostroidServiceTier": "standard"
}
`,
	},
}
