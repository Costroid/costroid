package adapter

// Gatewayed is an agent that can send every request through a gateway on a
// key the run holds. A run refuses a gateway key for an agent that is not.
type Gatewayed interface {
	// GatewayEnv is the environment, as NAME=value entries, that points the
	// agent at the gateway's base URL with the key as its credential. An entry
	// with no value names a credential the agent must not hold beside the
	// key, and the run removes it.
	GatewayEnv(base, key string) []string
	// GatewayArgs is what the agent's command line needs to reach the gateway
	// at base. The run places it after every option the command already
	// holds, so it overrides any value the command or a configuration file
	// sets.
	GatewayArgs(base string) []string
}

// GatewayBilled is a session that reads its cost basis from the credential
// its agent starts with. A run through a gateway starts the agent on the
// run's key, which the gateway bills per token upstream, and says so.
type GatewayBilled interface {
	BilledThroughGateway()
}
