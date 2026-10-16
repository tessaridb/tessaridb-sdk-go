package tessaridb

import (
	"strconv"
	"strings"
	"time"
)

// NotACount: a claim asked for fewer than 1 or more than 500 records.
const NotACount RefusalReason = "not-a-count"

// queueCeiling is the most records one claim may take; the node refuses more.
const queueCeiling = 500

// queueStatements renders the statements a queue worker sends (queue contract
// §2) in one place. The worker builds its text through nothing else, so the
// shared corpus (queue-v1.json) checking this type checks what actually goes
// out. Names are checked here, once, before anything is sent (§3).
type queueStatements struct {
	// Sent with every statement: a connection that reconnected has forgotten
	// any earlier USE (§5).
	tenancy string
	queue   string
	worker  string
}

func newQueueStatements(namespace, database, queue, worker string) (queueStatements, error) {
	for _, each := range [][2]string{{"a namespace", namespace}, {"a database", database}, {"a queue", queue}} {
		if err := checkName(each[0], each[1]); err != nil {
			return queueStatements{}, err
		}
	}
	// The worker is a quoted literal, checked as a group name is and never
	// escaped.
	if !isGroupName(worker) {
		return queueStatements{}, &BuilderError{Reason: NotAName, What: "a worker", Name: worker}
	}
	return queueStatements{
		tenancy: "USE NAMESPACE " + namespace + "; USE DATABASE " + database + "; ",
		queue:   queue,
		worker:  worker,
	}, nil
}

func (s queueStatements) claim(count int, where Filter) (string, map[string]Value, error) {
	if count < 1 || count > queueCeiling {
		return "", nil, &BuilderError{Reason: NotACount, What: "a count", Name: strconv.Itoa(count)}
	}
	b := newBinder()
	narrowed := ""
	if where != nil {
		var out strings.Builder
		if err := where.render(b, &out); err != nil {
			return "", nil, err
		}
		narrowed = " WHERE " + out.String()
	}
	script := s.tenancy + "USE CONSUMER '" + s.worker + "'; CLAIM " + strconv.Itoa(count) +
		" FROM " + s.queue + narrowed + ";"
	return script, b.parameters, nil
}

// done deletes a record a claim took, named by the claim's own spelling (§2).
func (s queueStatements) done(identity string) string {
	return s.tenancy + "DELETE " + s.queue + ":" + identity + ";"
}

func (s queueStatements) release(identity string, after time.Duration) string {
	// A delay is a duration literal written from a number formatted here, never
	// from a caller's text.
	tail := ""
	if millis := after.Milliseconds(); millis > 0 {
		tail = " NOT BEFORE " + strconv.FormatInt(millis, 10) + "ms"
	}
	return s.tenancy + "RELEASE " + s.queue + ":" + identity + " FOR CONSUMER '" + s.worker + "'" + tail + ";"
}
