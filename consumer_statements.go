package tessaridb

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

// consumerStatements renders the statements a consumer sends (§2) in one place.
// The consumer builds its text through nothing else, so the shared corpus
// (consumer-v1.json) checking this type checks what actually goes out. Names are
// checked here, once, before anything is sent (§3).
type consumerStatements struct {
	// Sent with every statement: a connection that reconnected has forgotten
	// any earlier USE (§5).
	tenancy string
	topic   string
	group   string
}

// isGroupName holds §3's pattern: the group is a quoted literal the grammar
// does not take as a parameter, so it is checked and never escaped.
func isGroupName(name string) bool {
	if len(name) == 0 || len(name) > 128 {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		switch {
		case 'A' <= c && c <= 'Z', 'a' <= c && c <= 'z', '0' <= c && c <= '9':
		case c == '_', c == '.', c == ':', c == '-':
		default:
			return false
		}
	}
	return true
}

func newConsumerStatements(namespace, database, topic, group string) (consumerStatements, error) {
	for _, each := range [][2]string{{"a namespace", namespace}, {"a database", database}, {"a topic", topic}} {
		if err := checkName(each[0], each[1]); err != nil {
			return consumerStatements{}, err
		}
	}
	if !isGroupName(group) {
		return consumerStatements{}, &BuilderError{Reason: NotAName, What: "a group", Name: group}
	}
	return consumerStatements{
		tenancy: "USE NAMESPACE " + namespace + "; USE DATABASE " + database + "; ",
		topic:   topic,
		group:   group,
	}, nil
}

func (s consumerStatements) read(limit int) string {
	return s.tenancy + "READ FROM " + s.topic + " FOR CONSUMER '" + s.group + "' LIMIT " + strconv.Itoa(limit) + ";"
}

func (s consumerStatements) ack(positions []uint64) (string, map[string]Value, error) {
	return s.settle("ACK", positions, "")
}

func (s consumerStatements) nack(delay time.Duration, positions []uint64) (string, map[string]Value, error) {
	// A delay is a duration literal in the grammar, not a parameter, written
	// from a number formatted here and never from a caller's text.
	tail := ""
	if millis := delay.Milliseconds(); millis > 0 {
		tail = " DELAY " + strconv.FormatInt(millis, 10) + "ms"
	}
	return s.settle("NACK", positions, tail)
}

func (s consumerStatements) settle(verb string, positions []uint64, tail string) (string, map[string]Value, error) {
	parameters := make(map[string]Value, len(positions))
	references := make([]string, len(positions))
	for i, position := range positions {
		if position > 1<<63-1 {
			return "", nil, fmt.Errorf("tessaridb: position %d is past what the store counts", position)
		}
		name := "p" + strconv.Itoa(i)
		parameters[name] = Integer{Value: int64(position)}
		references[i] = "$" + name
	}
	script := s.tenancy + verb + " " + s.topic + " FOR CONSUMER '" + s.group + "' AT " +
		strings.Join(references, ", ") + tail + ";"
	return script, parameters, nil
}
