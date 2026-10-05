package tessaridb

// The vault frame's body (protocol §3.14) and the status every act answers with.
// The passphrase is a field of the frame and never statement text, and no
// exported type here holds one.

import (
	"errors"
	"fmt"
	"time"
)

// SealState is whether the node can open secrets right now.
type SealState string

const (
	// Uninitialised: the store has no passphrase yet; the first unseal sets it.
	Uninitialised SealState = "uninitialised"
	// Sealed: no key is held; nothing can be revealed.
	Sealed SealState = "sealed"
	// Unsealed: a key is held until VaultStatus.SealsAt.
	Unsealed SealState = "unsealed"
)

// Custody is what opens a vault. The empty value is the store's own status.
type Custody string

const (
	// CustodyOwn: the vault's own passphrase; the store's opens nothing in it.
	CustodyOwn Custody = "own"
	// CustodyStore: the store's passphrase; the state beside it is the store's.
	CustodyStore Custody = "store"
)

// VaultStatus is the node's answer to every vault act.
type VaultStatus struct {
	State SealState
	// SealsAt is when the key held now stops opening anything; nil unless unsealed.
	SealsAt *time.Time
	// UnsealFor is how long an unseal lasts on this node.
	UnsealFor time.Duration
	// Initialised is whether this unseal set the store's first passphrase.
	Initialised bool
	// Custody is what opens the vault an act named; empty for the store's own.
	Custody Custody
}

// ErrNodeTooOld is returned, wrapped with both numbers, when a call needs a
// minor the node's greeting did not announce. Nothing was sent.
var ErrNodeTooOld = errors.New("tessaridb: this node is too old for this call")

type vaultPlace struct{ namespace, database, vault string }

type vaultAct struct {
	kind                      string
	passphrase, current, next string
}

var vaultActs = map[string]byte{"status": 1, "unseal": 2, "seal": 3, "change": 4}

// vaultFrameBody is credentials, the target (the store, or one vault), then the
// act and its fields.
func vaultFrameBody(credentials *Credentials, place *vaultPlace, act vaultAct) []byte {
	w := &writer{}
	if credentials != nil {
		w.u8(1)
		w.text(credentials.User)
		w.text(credentials.Password)
	} else {
		w.u8(0)
	}
	if place != nil {
		w.u8(1)
		w.text(place.namespace)
		w.text(place.database)
		w.text(place.vault)
	} else {
		w.u8(0)
	}
	w.u8(vaultActs[act.kind])
	switch act.kind {
	case "unseal":
		w.text(act.passphrase)
	case "change":
		w.text(act.current)
		w.text(act.next)
	}
	return w.buf
}

// readVaultStatus reads the status object; anything outside its closed sets is
// refused rather than guessed at.
func readVaultStatus(value Value) (VaultStatus, error) {
	object, ok := value.(Object)
	if !ok {
		return VaultStatus{}, protocolf("a vault status is an object, not %T", value)
	}
	state, ok := object.Fields["state"].(Text)
	period, periodOK := object.Fields["unseal_for"].(Duration)
	if !ok || !periodOK {
		return VaultStatus{}, protocolf("a vault status carries a state and a period")
	}
	status := VaultStatus{
		State:     SealState(state.Value),
		UnsealFor: time.Duration(period.Seconds)*time.Second + time.Duration(period.Nanos),
	}
	switch status.State {
	case Uninitialised, Sealed, Unsealed:
	default:
		return VaultStatus{}, protocolf("a vault state outside its set: %q", state.Value)
	}
	switch at := object.Fields["seals_at"].(type) {
	case Datetime:
		when := time.Unix(at.Seconds, int64(at.Nanos)).UTC()
		status.SealsAt = &when
	case nil, None:
	default:
		return VaultStatus{}, protocolf("seals_at is a datetime, not %T", at)
	}
	if initialised, ok := object.Fields["initialised"].(Bool); ok {
		status.Initialised = initialised.Value
	}
	if held, present := object.Fields["custody"]; present {
		custody, ok := held.(Text)
		if !ok || (Custody(custody.Value) != CustodyOwn && Custody(custody.Value) != CustodyStore) {
			return VaultStatus{}, protocolf("custody is own or store, not %v", held)
		}
		status.Custody = Custody(custody.Value)
	}
	return status, nil
}

// vaultFrame sends one Vault frame and reads the status it answers with.
// Nothing is sent to a node below vaultMinor.
func (c *Conn) vaultFrame(place *vaultPlace, act vaultAct) (VaultStatus, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.peerMinor < vaultMinor {
		return VaultStatus{}, fmt.Errorf("%w: it speaks minor %d and the vault frame needs %d",
			ErrNodeTooOld, c.peerMinor, vaultMinor)
	}
	if c.subscribed {
		return VaultStatus{}, errors.New("tessaridb: this connection is subscribed and no longer answers statements")
	}
	// The credential rides in the frame as it does in a Request, and is spent
	// here as it is there.
	var credentials *Credentials
	if c.credentials != nil && !c.spent {
		credentials = c.credentials
		c.spent = true
	}
	if err := writeFrame(c.conn, frameVault, vaultFrameBody(credentials, place, act)); err != nil {
		return VaultStatus{}, err
	}
	kind, answer, err := readFrame(c.r, statementFrames)
	if err != nil {
		return VaultStatus{}, err
	}
	switch kind {
	case frameRefusal:
		return VaultStatus{}, readRefusal(answer)
	case frameAnswer:
		outcomes, err := readAnswer(answer)
		if err != nil {
			return VaultStatus{}, err
		}
		if len(outcomes) == 1 {
			if value, ok := outcomes[0].(ValueOutcome); ok {
				return readVaultStatus(value.Value)
			}
		}
	}
	return VaultStatus{}, protocolf("a vault frame is answered with one value")
}
