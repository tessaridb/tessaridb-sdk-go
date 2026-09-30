package tessaridb

// The whole vault contract against a node with an empty store (vault contract
// §7), with every error scanned for the passphrase.

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

const (
	vaultPassphrase = "an operator passphrase 4b71"
	vaultNext       = "the next passphrase 9c02"
	vaultTeam       = "the team passphrase 5d13"
	vaultPlanted    = "correct-horse-battery-staple-9f2b"
)

func refusedQuietly(t *testing.T, err error, secrets ...string) {
	t.Helper()
	var refusal *Refusal
	if !errors.As(err, &refusal) {
		t.Fatalf("not a refusal: %v", err)
	}
	for _, secret := range secrets {
		if strings.Contains(fmt.Sprintf("%v %#v", err, err), secret) {
			t.Fatalf("the error quotes a passphrase: %v", err)
		}
	}
}

func TestTheWholeVaultContractRunsAgainstANode(t *testing.T) {
	conn := node(t)
	first := must(conn.Unseal(vaultPassphrase))
	if !first.Initialised {
		t.Skip("the node's store already has a passphrase; run against an empty one")
	}
	if first.State != Unsealed || first.SealsAt == nil {
		t.Fatalf("%+v", first)
	}
	must(conn.Execute("DEFINE NAMESPACE app; USE NAMESPACE app; DEFINE DATABASE main; USE DATABASE main; "+
		"DEFINE VAULT team; DEFINE FIELD 'password' ON team TYPE string SECRET; "+
		"DEFINE FIELD login ON team TYPE string;", nil))
	vault := must(NewVault(conn, "app", "main", "team"))
	for _, write := range []struct {
		id     string
		fields map[string]Value
	}{
		{"github", map[string]Value{"password": Text{vaultPlanted}, "login": Text{"boog"}}},
		{"gitlab", map[string]Value{"password": Text{"second"}}},
		{"github", map[string]Value{"password": Text{vaultPlanted}}},
	} {
		if err := vault.Write(Text{write.id}, write.fields); err != nil {
			t.Fatal(err)
		}
	}
	page := must(vault.List(nil, 1))
	if !reflect.DeepEqual(page.IDs, []Value{Text{"github"}}) {
		t.Fatalf("%+v", page)
	}
	page = must(vault.List(page.Next, 1))
	if !reflect.DeepEqual(page.IDs, []Value{Text{"gitlab"}}) {
		t.Fatalf("%+v", page)
	}
	if last := must(vault.List(page.Next, 1)); len(last.IDs) != 0 || last.Next != nil {
		t.Fatalf("%+v", last)
	}
	revealed := must(vault.Reveal(Text{"github"}, "password"))
	if !reflect.DeepEqual(revealed, map[string]Value{"password": Text{vaultPlanted}}) {
		t.Fatalf("%v", revealed)
	}
	if every := must(vault.Reveal(Text{"github"})); !reflect.DeepEqual(every, revealed) {
		t.Fatalf("%v", every)
	}
	if err := vault.AddRecipient(Text{"github"}, "bob", []byte{1, 2, 3}); err != nil {
		t.Fatal(err)
	}
	if held := must(vault.Recipients(Text{"github"})); !bytes.Equal(held["bob"], []byte{1, 2, 3}) || len(held) != 1 {
		t.Fatalf("%v", held)
	}
	if err := vault.RemoveRecipient(Text{"github"}, "bob"); err != nil {
		t.Fatal(err)
	}
	refusedQuietly(t, vault.RemoveRecipient(Text{"github"}, "bob"))
	trail := must(conn.VaultAudit("app", "main", ""))
	if len(trail) < 2 || strings.Contains(fmt.Sprint(trail), vaultPlanted) {
		t.Fatalf("%v", trail)
	}

	_, err := conn.ChangePassphrase("not it", vaultNext)
	refusedQuietly(t, err, "not it", vaultNext)
	must(conn.ChangePassphrase(vaultPassphrase, vaultNext))
	if sealed := must(conn.Seal()); sealed.State != Sealed {
		t.Fatalf("%+v", sealed)
	}
	_, err = conn.Unseal(vaultPassphrase)
	refusedQuietly(t, err, vaultPassphrase)
	must(conn.Unseal(vaultNext))
	if again := must(vault.Reveal(Text{"github"}, "password")); !reflect.DeepEqual(again, revealed) {
		t.Fatalf("%v", again)
	}

	// A vault with its own passphrase: the store's opens nothing in it.
	must(conn.Execute("USE NAMESPACE app; USE DATABASE main; DEFINE VAULT own PASSPHRASE '"+vaultTeam+"'; "+
		"DEFINE FIELD token ON own TYPE string SECRET;", nil))
	own := must(NewVault(conn, "app", "main", "own"))
	if status := must(own.Status()); status.Custody != CustodyOwn || status.State != Unsealed {
		t.Fatalf("%+v", status)
	}
	if err := own.Write(Text{"github"}, map[string]Value{"token": Text{vaultPlanted}}); err != nil {
		t.Fatal(err)
	}
	if sealed := must(own.Seal()); sealed.State != Sealed {
		t.Fatalf("%+v", sealed)
	}
	_, err = own.Unseal(vaultNext)
	refusedQuietly(t, err, vaultNext)
	if opened := must(own.Unseal(vaultTeam)); opened.State != Unsealed {
		t.Fatalf("%+v", opened)
	}
	must(own.ChangePassphrase(vaultTeam, "the next team one"))
	if token := must(own.Reveal(Text{"github"}, "token")); !reflect.DeepEqual(token, map[string]Value{"token": Text{vaultPlanted}}) {
		t.Fatalf("%v", token)
	}
	if shared := must(vault.Status()); shared.Custody != CustodyStore {
		t.Fatalf("%+v", shared)
	}
	_, err = vault.Unseal(vaultNext)
	refusedQuietly(t, err, vaultNext)
}
