package tessaridb

// The statements a vault handle sends (vault contract §3), rendered in one place.
//
// Every id, value, recipient name and key is bound. Only checked names are
// written into the text: the vault and an actor bare, a field QUOTED — a vault is
// exactly where somebody declares a field with a word the language reserves
// (`password`), and a checked name holds no quote, so quoting it cannot change
// how the node reads it.

import (
	"sort"
	"strconv"
	"strings"
)

// vaultMostIDs is the most ids one listing may ask for (§3.1).
const vaultMostIDs = 10_000

// A VaultArgumentError is an argument the vault contract refuses before sending
// anything (§6): "bad-limit" for a listing outside 1-10000, "no-fields" for an
// empty write. A name that is not one is a *BuilderError.
type VaultArgumentError struct{ Reason string }

func (e *VaultArgumentError) Error() string { return "tessaridb: not a vault call: " + e.Reason }

type vaultStatements struct {
	// Sent with every statement: a connection that reconnected has forgotten
	// any earlier USE.
	tenancy string
	vault   string
}

func vaultTenancy(namespace, database string) (string, error) {
	if err := checkName("a namespace", namespace); err != nil {
		return "", err
	}
	if err := checkName("a database", database); err != nil {
		return "", err
	}
	return "USE NAMESPACE " + namespace + "; USE DATABASE " + database + "; ", nil
}

func newVaultStatements(namespace, database, vault string) (vaultStatements, error) {
	tenancy, err := vaultTenancy(namespace, database)
	if err != nil {
		return vaultStatements{}, err
	}
	if err := checkName("a vault", vault); err != nil {
		return vaultStatements{}, err
	}
	return vaultStatements{tenancy: tenancy, vault: vault}, nil
}

// vaultAuditStatement is INFO FOR AUDIT [BY actor] (§3.5); an empty actor is the
// whole trail.
func vaultAuditStatement(namespace, database, actor string) (string, map[string]Value, error) {
	tenancy, err := vaultTenancy(namespace, database)
	if err != nil {
		return "", nil, err
	}
	if actor == "" {
		return tenancy + "INFO FOR AUDIT;", map[string]Value{}, nil
	}
	if err := checkName("an actor", actor); err != nil {
		return "", nil, err
	}
	return tenancy + "INFO FOR AUDIT BY " + actor + ";", map[string]Value{}, nil
}

// sortedFields checks each field name and puts them in ascending byte order.
func sortedFields(names []string) ([]string, error) {
	out := make([]string, 0, len(names))
	for _, name := range names {
		if err := checkName("a field", name); err != nil {
			return nil, err
		}
		out = append(out, name)
	}
	sort.Strings(out) // Go strings compare byte by byte.
	return out, nil
}

func (s vaultStatements) list(after Value, limit *int) (string, map[string]Value, error) {
	clauses := ""
	parameters := map[string]Value{}
	if after != nil {
		clauses += " AFTER " + s.vault + ":$after"
		parameters["after"] = after
	}
	if limit != nil {
		if *limit < 1 || *limit > vaultMostIDs {
			return "", nil, &VaultArgumentError{Reason: "bad-limit"}
		}
		clauses += " LIMIT " + strconv.Itoa(*limit)
	}
	return s.tenancy + "INFO FOR VAULT " + s.vault + " RECORDS" + clauses + ";", parameters, nil
}

func (s vaultStatements) reveal(id Value, fields []string) (string, map[string]Value, error) {
	names, err := sortedFields(fields)
	if err != nil {
		return "", nil, err
	}
	which := "*"
	if len(names) > 0 {
		which = "'" + strings.Join(names, "', '") + "'"
	}
	return s.tenancy + "REVEAL " + which + " FROM " + s.vault + ":$id;", map[string]Value{"id": id}, nil
}

func (s vaultStatements) write(id Value, fields map[string]Value) (string, map[string]Value, error) {
	if len(fields) == 0 {
		return "", nil, &VaultArgumentError{Reason: "no-fields"}
	}
	given := make([]string, 0, len(fields))
	for name := range fields {
		given = append(given, name)
	}
	names, err := sortedFields(given)
	if err != nil {
		return "", nil, err
	}
	parameters := map[string]Value{"id": id}
	pairs := make([]string, 0, len(names))
	for index, name := range names {
		bound := "f" + strconv.Itoa(index)
		pairs = append(pairs, "'"+name+"': $"+bound)
		parameters[bound] = fields[name]
	}
	return s.tenancy + "UPSERT " + s.vault + ":$id MERGE { " + strings.Join(pairs, ", ") + " };", parameters, nil
}

func (s vaultStatements) recipients(id Value) (string, map[string]Value) {
	return s.tenancy + "INFO FOR RECIPIENTS OF " + s.vault + ":$id;", map[string]Value{"id": id}
}

func (s vaultStatements) addRecipient(id Value, name string, key []byte) (string, map[string]Value) {
	return s.tenancy + "ADD RECIPIENT $name TO " + s.vault + ":$id KEY $key;",
		map[string]Value{"id": id, "name": Text{name}, "key": Bytes{key}}
}

func (s vaultStatements) removeRecipient(id Value, name string) (string, map[string]Value) {
	return s.tenancy + "REMOVE RECIPIENT $name FROM " + s.vault + ":$id;",
		map[string]Value{"id": id, "name": Text{name}}
}
