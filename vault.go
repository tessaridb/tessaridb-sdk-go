package tessaridb

// A vault — vault contract 1.0 (spec/vault-v1.md in the protocol repository).
//
// Two halves. The store's acts — VaultStatus, Unseal, Seal, ChangePassphrase on
// a *Conn — go in a frame of their own (protocol §3.14), so a passphrase is a
// field and never a statement: statement text is what a console keeps and a
// client logs on failure. A *Vault then lists, reveals, writes and shares the
// records of one vault with statements whose every id and value is bound, and
// acts on that vault alone when it carries its own passphrase.
//
// What a vault promises, said as narrowly as it is true: the stored bytes,
// backups and replicas are ciphertext; a running node that is unsealed can
// decrypt, because it must to answer a reveal. An unseal lasts the node's period
// and then closes by itself. A refusal after a run of wrong passphrases means
// WAIT, and is not retried here. A passphrase given to these calls is sent and
// dropped, and is in no error this package returns.

import "fmt"

// VaultStatus answers whether the node can open secrets with the store's key.
func (c *Conn) VaultStatus() (VaultStatus, error) {
	return c.vaultFrame(nil, vaultAct{kind: "status"})
}

// Unseal presents the store's passphrase. The first one ever presented becomes
// the passphrase, and the answer says so with Initialised.
func (c *Conn) Unseal(passphrase string) (VaultStatus, error) {
	return c.vaultFrame(nil, vaultAct{kind: "unseal", passphrase: passphrase})
}

// Seal drops the store's key: nothing in its custody opens until the next unseal.
func (c *Conn) Seal() (VaultStatus, error) {
	return c.vaultFrame(nil, vaultAct{kind: "seal"})
}

// ChangePassphrase wraps the store's key under a new passphrase. No secret is
// re-encrypted, and a backup taken before still opens with the old one.
func (c *Conn) ChangePassphrase(current, next string) (VaultStatus, error) {
	return c.vaultFrame(nil, vaultAct{kind: "change", current: current, next: next})
}

// VaultAudit is the store's trail of vault reads, or one user's when by is not
// empty. Answered only to a caller who administers the whole store.
func (c *Conn) VaultAudit(namespace, database, by string) ([]Value, error) {
	script, parameters, err := vaultAuditStatement(namespace, database, by)
	if err != nil {
		return nil, err
	}
	report, err := c.vaultValue(script, parameters)
	if err != nil {
		return nil, err
	}
	object, _ := report.(Object)
	entries, ok := object.Fields["audit"].(Array)
	if !ok {
		return nil, protocolf("an audit answer holds an array")
	}
	return entries.Items, nil
}

func (c *Conn) vaultValue(script string, parameters map[string]Value) (Value, error) {
	reply, err := c.Execute(script, parameters)
	if err != nil {
		return nil, err
	}
	if answered, ok := last(reply).(ValueOutcome); ok {
		return answered.Value, nil
	}
	return nil, fmt.Errorf("tessaridb: a vault statement answered %T", last(reply))
}

// A Page is one page of a vault's record ids in key order; Next is nil on the
// last page.
type Page struct {
	IDs  []Value
	Next Value
}

// A Vault is one vault in a namespace and database, over a connection the
// caller holds. Every call sends its own USE, so a connection that reconnected
// underneath cannot read another database.
type Vault struct {
	conn       *Conn
	statements vaultStatements
	place      vaultPlace
}

// NewVault checks the three names — refused with a *BuilderError before
// anything is sent — and returns a handle on that vault.
func NewVault(conn *Conn, namespace, database, vault string) (*Vault, error) {
	statements, err := newVaultStatements(namespace, database, vault)
	if err != nil {
		return nil, err
	}
	return &Vault{conn: conn, statements: statements, place: vaultPlace{namespace, database, vault}}, nil
}

// Status is this vault's seal status; Custody says what opens it, and for
// CustodyStore the state is the store's.
func (v *Vault) Status() (VaultStatus, error) {
	return v.conn.vaultFrame(&v.place, vaultAct{kind: "status"})
}

// Unseal opens this vault with its own passphrase for the node's period. A vault
// in the store's custody is refused rather than unsealed through the store,
// which would open every other vault the store holds.
func (v *Vault) Unseal(passphrase string) (VaultStatus, error) {
	return v.conn.vaultFrame(&v.place, vaultAct{kind: "unseal", passphrase: passphrase})
}

// Seal closes this vault; the store and every other vault stay as they were.
func (v *Vault) Seal() (VaultStatus, error) {
	return v.conn.vaultFrame(&v.place, vaultAct{kind: "seal"})
}

// ChangePassphrase wraps this vault's key under a new passphrase.
func (v *Vault) ChangePassphrase(current, next string) (VaultStatus, error) {
	return v.conn.vaultFrame(&v.place, vaultAct{kind: "change", current: current, next: next})
}

// List answers one page of ids after `after` (the previous page's Next, or nil).
// A limit of 0 asks for the node's own page of 1000; otherwise 1 to 10000.
func (v *Vault) List(after Value, limit int) (Page, error) {
	var bound *int
	if limit != 0 {
		bound = &limit
	}
	script, parameters, err := v.statements.list(after, bound)
	if err != nil {
		return Page{}, err
	}
	report, err := v.conn.vaultValue(script, parameters)
	if err != nil {
		return Page{}, err
	}
	object, _ := report.(Object)
	ids, ok := object.Fields["records"].(Array)
	if !ok {
		return Page{}, protocolf("a listing holds an array of ids")
	}
	page := Page{IDs: ids.Items}
	if next, present := object.Fields["next"]; present {
		if _, none := next.(None); !none {
			page.Next = next
		}
	}
	return page, nil
}

// Reveal answers the named secret fields of one record, or every secret field
// when none are named. The node records the read before it answers.
func (v *Vault) Reveal(id Value, fields ...string) (map[string]Value, error) {
	script, parameters, err := v.statements.reveal(id, fields)
	if err != nil {
		return nil, err
	}
	revealed, err := v.conn.vaultValue(script, parameters)
	if err != nil {
		return nil, err
	}
	object, ok := revealed.(Object)
	if !ok {
		return nil, protocolf("a reveal answers an object")
	}
	return object.Fields, nil
}

// Write sets these fields, creating the record when absent and keeping every
// other field and every recipient.
func (v *Vault) Write(id Value, fields map[string]Value) error {
	script, parameters, err := v.statements.write(id, fields)
	if err != nil {
		return err
	}
	_, err = v.conn.Execute(script, parameters)
	return err
}

// Recipients answers who may one day open this record: name → key material.
func (v *Vault) Recipients(id Value) (map[string][]byte, error) {
	report, err := v.conn.vaultValue(v.statements.recipients(id))
	if err != nil {
		return nil, err
	}
	object, _ := report.(Object)
	held, ok := object.Fields["recipients"].(Object)
	if !ok {
		return nil, protocolf("recipients are names to bytes")
	}
	out := make(map[string][]byte, len(held.Fields))
	for name, key := range held.Fields {
		material, ok := key.(Bytes)
		if !ok {
			return nil, protocolf("recipients are names to bytes")
		}
		out[name] = material.Value
	}
	return out, nil
}

// AddRecipient adds one; a name already present is refused, never replaced.
func (v *Vault) AddRecipient(id Value, name string, key []byte) error {
	_, err := v.conn.Execute(v.statements.addRecipient(id, name, key))
	return err
}

// RemoveRecipient removes one; a name that is not there is refused, never ok.
func (v *Vault) RemoveRecipient(id Value, name string) error {
	_, err := v.conn.Execute(v.statements.removeRecipient(id, name))
	return err
}
