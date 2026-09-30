package tessaridb

// The vault corpus (vault-v1.json) byte for byte, and the status a node answers
// with read from closed sets.

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"
)

type vaultCorpus struct {
	Frames []struct {
		Name    string          `json:"name"`
		Build   json.RawMessage `json:"build"`
		BodyHex string          `json:"body_hex"`
	} `json:"frames"`
	Statements []struct {
		Name       string                     `json:"name"`
		Build      json.RawMessage            `json:"build"`
		Script     string                     `json:"script"`
		Parameters map[string]json.RawMessage `json:"parameters"`
		Refused    *struct {
			Reason string `json:"reason"`
			What   string `json:"what"`
		} `json:"refused"`
	} `json:"statements"`
}

func readVaultCorpus(t *testing.T) vaultCorpus {
	t.Helper()
	raw := readCorpus(t, "vault-v1.json")
	whole, err := json.Marshal(raw)
	if err != nil {
		t.Fatal(err)
	}
	var corpus vaultCorpus
	if err := json.Unmarshal(whole, &corpus); err != nil {
		t.Fatal(err)
	}
	return corpus
}

func TestEveryVaultFrameIsTheCorpusBytes(t *testing.T) {
	corpus := readVaultCorpus(t)
	if len(corpus.Frames) != 11 {
		t.Fatalf("the corpus holds %d frames, not 11", len(corpus.Frames))
	}
	for _, c := range corpus.Frames {
		var build struct {
			Act         string
			Passphrase  string
			Current     string
			New         string
			Credentials *struct{ Name, Password string }
			Vault       *struct{ Namespace, Database, Vault string }
		}
		if err := json.Unmarshal(c.Build, &build); err != nil {
			t.Fatal(err)
		}
		var credentials *Credentials
		if build.Credentials != nil {
			credentials = &Credentials{User: build.Credentials.Name, Password: build.Credentials.Password}
		}
		var place *vaultPlace
		if build.Vault != nil {
			place = &vaultPlace{build.Vault.Namespace, build.Vault.Database, build.Vault.Vault}
		}
		act := vaultAct{kind: build.Act, passphrase: build.Passphrase, current: build.Current, next: build.New}
		if got := hex.EncodeToString(vaultFrameBody(credentials, place, act)); got != c.BodyHex {
			t.Errorf("%s: %s, corpus %s", c.Name, got, c.BodyHex)
		}
	}
}

func renderVault(t *testing.T, build json.RawMessage) (string, map[string]Value, error) {
	tag, body := one(t, build)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatal(err)
	}
	namespace, database := str(t, fields["namespace"]), str(t, fields["database"])
	if tag == "audit" {
		actor := ""
		if raw, ok := fields["actor"]; ok {
			actor = str(t, raw)
		}
		return vaultAuditStatement(namespace, database, actor)
	}
	s, err := newVaultStatements(namespace, database, str(t, fields["vault"]))
	if err != nil {
		return "", nil, err
	}
	if tag == "list" {
		var after Value
		if raw, ok := fields["after"]; ok {
			after = valueOf(t, raw)
		}
		var limit *int
		if raw, ok := fields["limit"]; ok {
			n := int(num(t, raw))
			limit = &n
		}
		return s.list(after, limit)
	}
	id := valueOf(t, fields["id"])
	switch tag {
	case "reveal":
		var names []string
		if raw, ok := fields["fields"]; ok {
			if err := json.Unmarshal(raw, &names); err != nil {
				t.Fatal(err)
			}
		}
		return s.reveal(id, names)
	case "write":
		var given map[string]json.RawMessage
		if err := json.Unmarshal(fields["fields"], &given); err != nil {
			t.Fatal(err)
		}
		values := make(map[string]Value, len(given))
		for name, raw := range given {
			values[name] = valueOf(t, raw)
		}
		return s.write(id, values)
	case "recipients":
		script, parameters := s.recipients(id)
		return script, parameters, nil
	case "add_recipient":
		key, err := hex.DecodeString(str(t, fields["key"]))
		if err != nil {
			t.Fatal(err)
		}
		script, parameters := s.addRecipient(id, str(t, fields["name"]), key)
		return script, parameters, nil
	case "remove_recipient":
		script, parameters := s.removeRecipient(id, str(t, fields["name"]))
		return script, parameters, nil
	}
	t.Fatalf("a statement the corpus does not define: %s", tag)
	return "", nil, nil
}

func TestEveryVaultStatementRendersOrIsRefusedAsTheCorpusSays(t *testing.T) {
	corpus := readVaultCorpus(t)
	if len(corpus.Statements) != 19 {
		t.Fatalf("the corpus holds %d statements, not 19", len(corpus.Statements))
	}
	for _, c := range corpus.Statements {
		script, parameters, err := renderVault(t, c.Build)
		if c.Refused != nil {
			var builder *BuilderError
			var argument *VaultArgumentError
			switch {
			case errors.As(err, &builder):
				if string(builder.Reason) != c.Refused.Reason || builder.What != c.Refused.What {
					t.Errorf("%s: refused as %s/%s", c.Name, builder.Reason, builder.What)
				}
			case errors.As(err, &argument):
				if argument.Reason != c.Refused.Reason {
					t.Errorf("%s: refused as %s", c.Name, argument.Reason)
				}
			default:
				t.Errorf("%s: not refused as the corpus says: %v", c.Name, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", c.Name, err)
			continue
		}
		if script != c.Script {
			t.Errorf("%s:\n%s\ncorpus:\n%s", c.Name, script, c.Script)
		}
		expected := make(map[string]Value, len(c.Parameters))
		for name, raw := range c.Parameters {
			expected[name] = valueOf(t, raw)
		}
		if !reflect.DeepEqual(parameters, expected) {
			t.Errorf("%s: parameters %v, corpus %v", c.Name, parameters, expected)
		}
	}
}

func statusObject(state string, more map[string]Value) Value {
	fields := map[string]Value{"state": Text{state}, "unseal_for": Duration{Seconds: 600}}
	for name, value := range more {
		fields[name] = value
	}
	return Object{Fields: fields}
}

func TestAVaultStatusIsReadFromClosedSets(t *testing.T) {
	cases := []struct {
		name  string
		value Value
		want  *VaultStatus
	}{
		{"sealed", statusObject("sealed", nil), &VaultStatus{State: Sealed, UnsealFor: 10 * time.Minute}},
		{"own", statusObject("unsealed", map[string]Value{
			"seals_at": Datetime{Seconds: 1_790_000_000}, "custody": Text{"own"}, "initialised": Bool{true},
		}), &VaultStatus{State: Unsealed, UnsealFor: 10 * time.Minute, Initialised: true, Custody: CustodyOwn}},
		{"an unknown state", statusObject("ajar", nil), nil},
		{"an unknown custody", statusObject("sealed", map[string]Value{"custody": Text{"shared"}}), nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := readVaultStatus(c.value)
			if c.want == nil {
				if err == nil {
					t.Fatalf("read %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			sealsAt := got.SealsAt
			got.SealsAt = nil
			if !reflect.DeepEqual(got, *c.want) {
				t.Fatalf("%+v, want %+v", got, *c.want)
			}
			if c.name == "own" && (sealsAt == nil || sealsAt.Unix() != 1_790_000_000) {
				t.Fatalf("seals_at %v", sealsAt)
			}
		})
	}
}
