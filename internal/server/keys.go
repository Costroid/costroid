package server

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"regexp"

	"github.com/Costroid/costroid/internal/cli"
	"github.com/Costroid/costroid/internal/store"
)

// KeysUsage is keys' help page. costroid help keys prints it on stdout, and
// keys revoke prints it on stderr when it is given no id.
// keysGroup is what costroid keys prints with no subcommand.
var keysGroup = cli.Group{
	Command: "keys",
	Summary: "keys issues and revokes tenant ingest keys.",
	Subcommands: []cli.Row{
		{"create", "issue a key for a tenant"},
		{"revoke", "stop a key by its id"},
	},
	Example: []string{"costroid keys create --owner github.com:example > key"},
}

var KeysUsage = cli.Page{
	Command: "keys",
	Summary: keysGroup.Summary,
	Usage: []string{
		"costroid keys create --owner HOST:OWNER",
		"costroid keys revoke <id>",
	},
	Examples: []string{
		"costroid keys create --owner github.com:example > key",
		`costroid keys revoke "$KEY_ID"`,
	},
	Flags: []cli.Row{{"--owner HOST:OWNER", "an SCM owner of the tenant, host:owner"}},
	Sections: []cli.Section{
		{Heading: "create and revoke", Body: []cli.Block{cli.Text(`create prints the new key on stdout, once, and its id on stderr. The ledger
keeps the key's SHA-256, which is the id, so a lost key cannot be read back.
--owner names an SCM owner of the tenant the key is for. revoke takes the id
and stops the key at the next request.`)}},
		{Heading: "What a key's rows read", Body: []cli.Block{cli.Text("A key's rows stay unverified and unattributed, and never reach a budget.")}},
	},
	Environment: []cli.Block{cli.Rows{{envMigration, "the owner role's connection"}}},
	Exits: []cli.Row{
		{"0", "the key was created or revoked"},
		cli.UsageExit,
		{"125", `costroid could not reach the database or find the tenant
or the key`},
	},
	Docs: "serve",
}.String()

var keyID = regexp.MustCompile(`^[0-9a-f]{64}$`)

// KeysMain runs costroid keys and returns an exit code.
func KeysMain(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stdout, keysGroup)
		return cli.ExitUsage
	}
	err := keysCommand(ctx, args, stdout, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		return cli.Fail(stderr, err)
	}
	return 0
}

func keysCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	switch args[0] {
	case "create":
		return createKey(ctx, args[1:], stdout, stderr)
	case "revoke":
		return revokeKey(ctx, args[1:], stdout, stderr)
	case "help", "-h", "-help", "--help":
		fmt.Fprint(stdout, KeysUsage)
		return flag.ErrHelp
	}
	return keysGroup.Unknown(args[0])
}

func createKey(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	set := flag.NewFlagSet("keys create", flag.ContinueOnError)
	ownerFlag := set.String("owner", "", "an SCM owner of the tenant, host:owner")
	if err := cli.Parse(set, args, stderr); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, KeysUsage)
		}
		return err
	}
	if err := cli.NoArgument(set); err != nil {
		return err
	}
	owner, err := parseBinding("--owner", *ownerFlag)
	if err != nil {
		return cli.Mistaken("keys", err)
	}
	ownerURL, err := required(envMigration)
	if err != nil {
		return err
	}
	key, err := store.NewKey()
	if err != nil {
		return err
	}
	id := store.KeyDigest(key)
	err = store.AddKey(ctx, ownerURL, owner, id)
	if errors.Is(err, store.ErrNotFound) {
		return fmt.Errorf("no tenant holds %s:%s", owner.Host, owner.Owner)
	}
	if err != nil {
		return err
	}
	fmt.Fprintln(stdout, key)
	cli.Say(stderr, "id %s\nThis is the only time the key is shown.\n", id)
	return nil
}

// revokeKey never repeats its argument, which may be a key pasted by mistake.
func revokeKey(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	set := flag.NewFlagSet("keys revoke", flag.ContinueOnError)
	if err := cli.Parse(set, args, stderr); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, KeysUsage)
		}
		return err
	}
	if set.NArg() != 1 || !keyID.MatchString(set.Arg(0)) {
		return cli.Mistaken("keys", errors.New("keys revoke needs one id: the 64 hexadecimal characters create printed"))
	}
	args = set.Args()
	ownerURL, err := required(envMigration)
	if err != nil {
		return err
	}
	err = store.RevokeKey(ctx, ownerURL, args[0])
	if errors.Is(err, store.ErrNotFound) {
		return errors.New("no key has that id")
	}
	return err
}
