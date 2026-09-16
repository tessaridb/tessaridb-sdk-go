package tessaridb

// The access paths, in the order their bytes number them. Anything else reads
// as a scan: the field says how the read was served, and a client that refused
// an unknown one would fail on a node that grew a tenth path while the records
// beside it were perfectly readable.
var accessPaths = []string{
	"record", "index", "scan", "ordered", "approximate",
	"graph", "join", "materialised", "span",
}

const (
	outcomeDone    = 0
	outcomeRecords = 1
	outcomeValue   = 2
	outcomeKeys    = 3
	outcomeRemoved = 4
)

// An Outcome is one statement's result. There is one per statement, in order.
type Outcome interface{ isOutcome() }

type Done struct{}

type Records struct {
	Path    string
	Names   map[uint32]string
	Rows    []Row
	Notes   []Note
	Only    bool
	Exact   Exactness
	Suggest Suggestion
}

type ValueOutcome struct {
	Names map[uint32]string
	Value Value
}

type Keys struct{ Keys []string }

type Removed struct{ Count uint64 }

// Unknown is an outcome kind this build has never seen. It is surfaced rather
// than dropped, and reading does not stop at it — the outcome's own length is
// what makes that possible, and it is why the length is there.
//
// It is not "an outcome carrying nothing". It is one this build cannot read,
// which points at upgrading rather than at shrugging.
type Unknown struct {
	Tag   byte
	Bytes []byte
}

func (Done) isOutcome()         {}
func (Records) isOutcome()      {}
func (ValueOutcome) isOutcome() {}
func (Keys) isOutcome()         {}
func (Removed) isOutcome()      {}
func (Unknown) isOutcome()      {}

type Row struct {
	Identity string
	Value    Value
}

// A Note qualifies an answer. Kind is a stable word a client may group on;
// Message is a sentence for a person and must not be branched on. An
// unrecognised kind is carried through rather than dropped — a note this build
// does not know is still the store reporting that the answer is qualified.
type Note struct {
	Kind    string
	Message string
}

// Exactness has THREE states, and the third is the one a client loses.
//
// Absent is not the default: a node that predates the field said nothing, which
// is different from a node that said "exact". A client whose type here is a
// boolean has already lost the distinction, and the value it invents at the
// first default is a promise no node made.
type Exactness struct {
	State  ExactState
	Reason string
}

type ExactState int

const (
	Unstated ExactState = iota
	Exact
	Inexact
)

// Suggestion also has three states, and NotConsulted is not Complete.
//
// Complete is a claim about the collection: a term dictionary was asked and
// found nothing to fix. NotConsulted is the absence of a claim — nothing was
// looked for. Rendering both as "no suggestions" reports a negative the node
// never checked, on every read of an unindexed field.
type Suggestion struct {
	State       SuggestState
	Corrections []Correction
}

type SuggestState int

const (
	NotConsulted SuggestState = iota
	Complete
	Corrections
)

type Correction struct {
	Typed   string
	Instead string
}

func readAnswer(body []byte) ([]Outcome, error) {
	r := &reader{buf: body}
	count, err := r.u32("an outcome count")
	if err != nil {
		return nil, err
	}
	outcomes := make([]Outcome, 0, min(int(count), 1024))
	for i := uint32(0); i < count; i++ {
		length, err := r.u32("an outcome length")
		if err != nil {
			return nil, err
		}
		if length < 1 {
			return nil, protocolf("an outcome carries at least its tag")
		}
		slice, err := r.fixed(int(length), "an outcome body")
		if err != nil {
			return nil, err
		}
		outcome, err := readOutcome(slice)
		if err != nil {
			return nil, err
		}
		outcomes = append(outcomes, outcome)
	}
	return outcomes, nil
}

func readOutcome(slice []byte) (Outcome, error) {
	r := &reader{buf: slice}
	tag, err := r.u8("an outcome tag")
	if err != nil {
		return nil, err
	}
	switch tag {
	case outcomeDone:
		return Done{}, nil
	case outcomeRecords:
		return readRecords(r)
	case outcomeValue:
		names, err := readNames(r)
		if err != nil {
			return nil, err
		}
		// The value is a `bytes` in §3.5's table, which at the frame layer means
		// a u32 length and then the value — NOT a bare value. Reading it raw
		// works out to reading the length's first byte as a type tag, which is
		// `0x00` and therefore an error; the failure is loud, but only for a
		// client that ever asks for a value outcome. A suite that only ever
		// SELECTs never produces one.
		payload, err := r.lenbytes("a value outcome")
		if err != nil {
			return nil, err
		}
		v, err := Decode(payload)
		return ValueOutcome{Names: names, Value: v}, err
	case outcomeKeys:
		count, err := r.u32("a key count")
		if err != nil {
			return nil, err
		}
		keys := make([]string, 0, min(int(count), 1024))
		for i := uint32(0); i < count; i++ {
			key, err := r.text("a key")
			if err != nil {
				return nil, err
			}
			keys = append(keys, key)
		}
		return Keys{Keys: keys}, nil
	case outcomeRemoved:
		n, err := r.u64("a removed count")
		return Removed{Count: n}, err
	default:
		return Unknown{Tag: tag, Bytes: slice[1:]}, nil
	}
}

// readRecords reads the fields that have been appended over time, checking for
// the end of the slice after each one.
//
// The tail is how this protocol grows without a version bump, so a client must
// stop where the bytes stop rather than demanding the newest shape. Exactness is
// the one field whose ABSENCE is not its default.
func readRecords(r *reader) (Outcome, error) {
	pathByte, err := r.u8("an access path")
	if err != nil {
		return nil, err
	}
	path := "scan"
	if int(pathByte) < len(accessPaths) {
		path = accessPaths[pathByte]
	}

	names, err := readNames(r)
	if err != nil {
		return nil, err
	}

	count, err := r.u32("a record count")
	if err != nil {
		return nil, err
	}
	rows := make([]Row, 0, min(int(count), 1024))
	for i := uint32(0); i < count; i++ {
		identity, err := r.text("a record identity")
		if err != nil {
			return nil, err
		}
		payload, err := r.lenbytes("a record value")
		if err != nil {
			return nil, err
		}
		value, err := Decode(payload)
		if err != nil {
			return nil, err
		}
		rows = append(rows, Row{Identity: identity, Value: value})
	}

	out := Records{Path: path, Names: names, Rows: rows, Notes: []Note{}}
	if r.remaining() == 0 {
		return out, nil
	}

	noteCount, err := r.u32("a note count")
	if err != nil {
		return nil, err
	}
	for i := uint32(0); i < noteCount; i++ {
		kind, err := r.text("a note kind")
		if err != nil {
			return nil, err
		}
		message, err := r.text("a note message")
		if err != nil {
			return nil, err
		}
		out.Notes = append(out.Notes, Note{Kind: kind, Message: message})
	}
	if r.remaining() == 0 {
		return out, nil
	}

	only, err := r.u8("an only flag")
	if err != nil {
		return nil, err
	}
	out.Only = only != 0
	if r.remaining() == 0 {
		return out, nil
	}

	state, err := r.u8("an exactness")
	if err != nil {
		return nil, err
	}
	reason, err := r.text("an exactness reason")
	if err != nil {
		return nil, err
	}
	if state == 0 {
		out.Exact = Exactness{State: Exact}
	} else {
		out.Exact = Exactness{State: Inexact, Reason: reason}
	}
	if r.remaining() == 0 {
		return out, nil
	}

	suggestion, err := readSuggestion(r)
	out.Suggest = suggestion
	return out, err
}

func readSuggestion(r *reader) (Suggestion, error) {
	state, err := r.u8("a suggestion state")
	if err != nil {
		return Suggestion{}, err
	}
	switch state {
	case 1:
		return Suggestion{State: Complete}, nil
	case 2:
		count, err := r.u32("a suggestion count")
		if err != nil {
			return Suggestion{}, err
		}
		items := make([]Correction, 0, min(int(count), 256))
		for i := uint32(0); i < count; i++ {
			typed, err := r.text("a suggestion's typed term")
			if err != nil {
				return Suggestion{}, err
			}
			instead, err := r.text("a suggestion's replacement")
			if err != nil {
				return Suggestion{}, err
			}
			items = append(items, Correction{Typed: typed, Instead: instead})
		}
		return Suggestion{State: Corrections, Corrections: items}, nil
	default:
		// An unknown state reads as silence rather than as a claim.
		return Suggestion{State: NotConsulted}, nil
	}
}

func readNames(r *reader) (map[uint32]string, error) {
	count, err := r.u32("a name count")
	if err != nil {
		return nil, err
	}
	names := make(map[uint32]string, min(int(count), 1024))
	for i := uint32(0); i < count; i++ {
		id, err := r.u32("a table id")
		if err != nil {
			return nil, err
		}
		name, err := r.text("a table name")
		if err != nil {
			return nil, err
		}
		names[id] = name
	}
	return names, nil
}
