package claudecode

// The variables Claude Code reads its endpoint and its credential from.
const (
	baseURLVariable   = "ANTHROPIC_BASE_URL"
	authTokenVariable = "ANTHROPIC_AUTH_TOKEN"
	apiKeyVariable    = "ANTHROPIC_API_KEY"
)

// GatewayEnv points Claude Code at the gateway, with the run's key as its
// bearer token. An API key beside it would let a request skip the gateway, so
// it is removed.
func (Agent) GatewayEnv(base, key string) []string {
	return []string{baseURLVariable + "=" + base, authTokenVariable + "=" + key, apiKeyVariable + "="}
}

// GatewayArgs is empty: Claude Code reads the gateway from its environment.
func (Agent) GatewayArgs(string) []string {
	return nil
}
