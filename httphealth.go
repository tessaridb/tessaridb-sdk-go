package tessaridb

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// /health and /ready.
//
// Three variants with distinct field sets, and not one record with optional
// fields: `leaving` carries no commit position, and a client that modelled it as
// an optional integer would offer a caller a zero that means "we are shutting
// down" rather than "we are at zero".
type Health interface{ isHealth() }

type Healthy struct{ Committed uint64 }

type Unwell struct {
	Committed        uint64
	BackgroundErrors uint64
	Complaint        string
}

type Leaving struct{}

func (Healthy) isHealth() {}
func (Unwell) isHealth()  {}
func (Leaving) isHealth() {}

// Health asks whether the node is well. Ready asks whether it should be sent
// traffic, and the two are NOT synonyms: they answer identically on a well node
// and diverge during a staged shutdown, where /ready reports leaving while
// /health still reports ok. That window is the whole reason both exist — a
// supervisor reads "not ready" as stop sending traffic here and "not healthy" as
// restart this — so neither is implemented in terms of the other.
func (c *HTTPClient) Health() (Health, error) { return c.condition("/health") }

func (c *HTTPClient) Ready() (Health, error) { return c.condition("/ready") }

func (c *HTTPClient) condition(path string) (Health, error) {
	response, err := c.send(http.MethodGet, path, nil, "")
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	// 503 on these two routes is an ANSWER rather than a transport failure: the
	// node has replied to the question it was asked.
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusServiceUnavailable {
		return nil, refusal(response)
	}
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}

	var answer struct {
		Status           string `json:"status"`
		Committed        uint64 `json:"committed"`
		BackgroundErrors uint64 `json:"background_errors"`
		Complaint        string `json:"complaint"`
	}
	if err := json.Unmarshal(body, &answer); err != nil {
		return nil, fmt.Errorf("tessaridb: the health answer is not JSON: %w", err)
	}
	switch answer.Status {
	case "ok":
		return Healthy{Committed: answer.Committed}, nil
	case "unwell":
		return Unwell{
			Committed:        answer.Committed,
			BackgroundErrors: answer.BackgroundErrors,
			Complaint:        answer.Complaint,
		}, nil
	case "leaving":
		return Leaving{}, nil
	}
	// Refused rather than mapped onto the nearest known one: a status this build
	// does not know is a node saying something, and guessing which of the three
	// it resembles inverts an operational decision.
	return nil, fmt.Errorf("tessaridb: %q is not a status this client knows", answer.Status)
}
