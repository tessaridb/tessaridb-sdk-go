package tessaridb

// RefusalClass is what a refusal says to do next (protocol §3.6, from minor 3).
//
// The message is prose for a person and changes between releases; the class is
// what code branches on. The wire carries it as one byte before the message and
// HTTP as the word in an error body's "code". The empty class means the node
// sent none — it predates protocol 1.3.
type RefusalClass string

const (
	// RefusalInvalid: fix the request; repeating it unchanged cannot succeed.
	RefusalInvalid RefusalClass = "invalid"
	// RefusalUnauthenticated: sign in, or sign in again.
	RefusalUnauthenticated RefusalClass = "unauthenticated"
	// RefusalForbidden: stop; signing in again will not help.
	RefusalForbidden RefusalClass = "forbidden"
	// RefusalThrottled: wait, then repeat.
	RefusalThrottled RefusalClass = "throttled"
	// RefusalElsewhere: send it to the node the message names.
	RefusalElsewhere RefusalClass = "elsewhere"
	// RefusalRetry: run the transaction again from its start.
	RefusalRetry RefusalClass = "retry"
	// RefusalConflict: re-read; the state the request assumed is not the state there is.
	RefusalConflict RefusalClass = "conflict"
	// RefusalUnavailable: try later or another node; the request itself was fine.
	RefusalUnavailable RefusalClass = "unavailable"
	// RefusalInternal: a defect, damaged data, or a format the node cannot read — report it.
	RefusalInternal RefusalClass = "internal"
	// RefusalUnknown: the node could not class it, or named a class this client
	// does not know. Treat it as not retriable.
	RefusalUnknown RefusalClass = "unknown"
)

// refusalClasses is the wire table: the index is the byte.
var refusalClasses = [...]RefusalClass{
	RefusalUnknown, RefusalInvalid, RefusalUnauthenticated, RefusalForbidden, RefusalThrottled,
	RefusalElsewhere, RefusalRetry, RefusalConflict, RefusalUnavailable, RefusalInternal,
}

// refusalClassOfWord reads an HTTP error body's "code"; a word this client does
// not know is RefusalUnknown.
func refusalClassOfWord(word string) RefusalClass {
	for _, class := range refusalClasses {
		if string(class) == word {
			return class
		}
	}
	return RefusalUnknown
}

// readRefusal reads a Refusal body: a first byte of 0–9 is the class, anything
// else is the first byte of a message from a node before protocol 1.3, which
// carries no class at all.
func readRefusal(body []byte) *Refusal {
	if len(body) > 0 && int(body[0]) < len(refusalClasses) {
		return &Refusal{Message: string(body[1:]), Class: refusalClasses[body[0]]}
	}
	return &Refusal{Message: string(body)}
}
