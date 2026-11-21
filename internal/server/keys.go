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

// KeysUsage is keys' help page. costroid help keys prints it on stdout, and a
// command line keys cannot read prints it on stderr.
var KeysUsage = cli.Page{
	Command: "keys",
	Summary: "keys issues and revokes tenant ingest keys.",
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
		{"125", `costroid could not read the command line, reach the
database or find the tenant or the key`},
	},
	Docs: "serve",
}.String()

var keyID = regexp.MustCompile(`^[0-9a-f]{64}$`)

// KeysMain runs costroid keys and returns an exit code.
func KeysMain(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	err := keysCommand(ctx, args, stdout, stderr)
	if errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if err != nil {
		fmt.Fprintf(stderr, "costroid: %v\n", err)
		return exitFailure
	}
	return 0
}

func keysCommand(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	if len(args) == 0 {
		fmt.Fprint(stderr, KeysUsage)
		return errors.New("keys needs create or revoke")
	}
	switch args[0] {
	case "create":
		return createKey(ctx, args[1:], stdout, stderr)
	case "revoke":
		return revokeKey(ctx, args[1:], stderr)
	case "help", "-h", "-help", "--help":
		fmt.Fprint(stdout, KeysUsage)
		return flag.ErrHelp
	}
	return fmt.Errorf("unknown keys command %q", args[0])
}

func createKey(ctx context.Context, args []string, stdout, stderr io.Writer) error {
	set := flag.NewFlagSet("keys create", flag.ContinueOnError)
	ownerFlag := set.String("owner", "", "an SCM owner of the tenant, host:owner")
	if err := cli.Parse(set, args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			fmt.Fprint(stdout, KeysUsage)
		}
		return err
	}
	owner, err := parseBinding("--owner", *ownerFlag)
	if err != nil {
		return err
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
	fmt.Fprintf(stderr, "id %s\nThis is the only time the key is shown.\n", id)
	return nil
}

// revokeKey never repeats its argument, which may be a key pasted by mistake.
func revokeKey(ctx context.Context, args []string, stderr io.Writer) error {
	if len(args) != 1 || !keyID.MatchString(args[0]) {
		fmt.Fprint(stderr, KeysUsage)
		return errors.New("keys revoke needs one id: the 64 hexadecimal characters create printed")
	}
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
