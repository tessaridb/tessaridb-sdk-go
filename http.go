package tessaridb

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
)

// The HTTP surface (§5).
//
// This is a different client from Conn because it is a different surface, not an
// alternative to it. Statements and change subscriptions go over the wire, which
// carries the store's full model of seventeen value types; objects, files,
// backup and the operational routes come here, because nothing else serves them.
//
// A caller never picks a transport per call. Routing statements through /script
// would work, would reach every route, and would silently narrow every result —
// JSON carries six types against the store's seventeen — with nothing at the
// call site showing what was lost.

// An HTTPError is a refusal, carrying the node's own sentence. That sentence is
// meant for a person: it is not a stable identifier and MUST NOT be branched on.
// Branch on Status, which §5.2 enumerates.
type HTTPError struct {
	Status  int
	Message string
	// Location is set only on 307, which is not a refusal at all — it is a read
	// this node could not answer within the staleness bound it was given, where
	// a peer can. The address is here rather than in the sentence, because a
	// redirect whose target a client must parse out of prose is not a redirect.
	Location string
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("tessaridb: %d %s", e.Status, e.Message)
}

// HTTPClient talks to one node. It is safe for concurrent use.
type HTTPClient struct {
	address     string
	credentials *Credentials
	client      *http.Client

	mu    sync.Mutex
	token string
}

func NewHTTPClient(address string, credentials *Credentials) *HTTPClient {
	return &HTTPClient{
		address:     address,
		credentials: credentials,
		client:      &http.Client{},
	}
}

// OpenSession spends the password once and holds the token.
//
// A password is expensive to verify on purpose — Argon2id at the OWASP floor —
// and HTTP has no connection to hang a session on, so that cost is paid on EVERY
// request carrying Basic. A client that presents a password everywhere is
// correct, passes every test, and is slower than this protocol intends by more
// than an order of magnitude.
//
// It reports whether a token is now held. An open store has no session to open
// and answers 401 saying so, which is not a failure to retry: a token cut from
// the ABSENCE of a credential would still work after the first DEFINE USER
// closed the store, which is precisely what must not happen.
func (c *HTTPClient) OpenSession() (bool, error) {
	if c.credentials == nil {
		return false, nil
	}
	request, err := http.NewRequest(http.MethodPost, c.url("/session"), nil)
	if err != nil {
		return false, err
	}
	request.SetBasicAuth(c.credentials.User, c.credentials.Password)

	response, err := c.client.Do(request)
	if err != nil {
		return false, err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusUnauthorized {
		// Either the store is open, or the credential is wrong. Both leave this
		// client with no token, and the difference shows on the next request.
		_, _ = io.Copy(io.Discard, response.Body)
		return false, nil
	}
	if response.StatusCode != http.StatusOK {
		// 503 is the token ceiling and IS retriable, unlike the 401 above. A
		// client that treated every failure to open a session as fatal would
		// give up on a node that is merely busy.
		return false, refusal(response)
	}

	var answer struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(response.Body).Decode(&answer); err != nil {
		return false, fmt.Errorf("tessaridb: the session answer is not JSON: %w", err)
	}
	c.mu.Lock()
	c.token = answer.Token
	c.mu.Unlock()
	return true, nil
}

// CloseSession gives the token back. It answers the same whether or not the node
// was holding it — whether a token it never issued exists is not something the
// presenter is entitled to learn.
func (c *HTTPClient) CloseSession() error {
	c.mu.Lock()
	held := c.token
	c.token = ""
	c.mu.Unlock()
	if held == "" {
		return nil
	}
	request, err := http.NewRequest(http.MethodDelete, c.url("/session"), nil)
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+held)
	response, err := c.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	return nil
}

func (c *HTTPClient) url(path string) string {
	return "http://" + c.address + path
}

// send carries the credential, and retries ONCE when a token stops working.
//
// A token ends four ways — it expires, it was given back, the user record
// changed, or the node restarted — and a client cannot tell them apart because
// all four answer 401. The correct behaviour for all four is the same: discard
// it, sign in again, retry once.
func (c *HTTPClient) send(method, path string, body []byte, contentType string) (*http.Response, error) {
	response, err := c.attempt(method, path, body, contentType)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusUnauthorized {
		return response, nil
	}

	c.mu.Lock()
	held := c.token
	c.mu.Unlock()
	if held == "" {
		return response, nil
	}
	response.Body.Close()

	c.mu.Lock()
	if c.token == held {
		c.token = ""
	}
	c.mu.Unlock()
	opened, err := c.OpenSession()
	if err != nil || !opened {
		return c.attempt(method, path, body, contentType)
	}
	return c.attempt(method, path, body, contentType)
}

func (c *HTTPClient) attempt(method, path string, body []byte, contentType string) (*http.Response, error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	request, err := http.NewRequest(method, c.url(path), reader)
	if err != nil {
		return nil, err
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}

	c.mu.Lock()
	held := c.token
	c.mu.Unlock()
	switch {
	case held != "":
		request.Header.Set("Authorization", "Bearer "+held)
	case c.credentials != nil:
		request.SetBasicAuth(c.credentials.User, c.credentials.Password)
	}
	return c.client.Do(request)
}

// refusal reads the node's sentence. Escaping is real here — these strings carry
// arbitrary caller input through JSON escaping — so it is parsed rather than
// scanned for the text between quotation marks.
func refusal(response *http.Response) error {
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	var answer struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(body, &answer)
	if answer.Error == "" {
		answer.Error = http.StatusText(response.StatusCode)
	}
	return &HTTPError{
		Status:   response.StatusCode,
		Message:  answer.Error,
		Location: response.Header.Get("Location"),
	}
}

// Script sends a script and returns one raw outcome per statement, in order.
//
// It takes NO parameters, deliberately. A parameter on this route is a JSON
// string carrying TessariQL SOURCE rather than a value — {"x":"3"} is the number
// 3 and {"x":"hello"} is a 400 — so passing a caller's string through would be a
// type-confusion hazard that no test written against it would show. A statement
// with a value in it goes over the wire, where a parameter is an encoded value
// and none of this arises.
//
// A failed multi-statement script reports no partial results, and a client MUST
// NOT retry one automatically: the statements before the failure have taken
// effect and are durable, so re-running turns one create into two. A caller that
// needs all-or-nothing wraps the statements in BEGIN … COMMIT.
func (c *HTTPClient) Script(script string) ([]any, error) {
	response, err := c.send(http.MethodPost, "/script", []byte(script), "text/plain")
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		return nil, refusal(response)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		return nil, err
	}
	return ScriptResults(body)
}

// Backup returns the whole log in one response — there is no resumption and no
// range support in this version, so a caller's memory ceiling for this route is
// the size of the log.
func (c *HTTPClient) Backup(from *uint64) ([]byte, error) {
	path := "/backup"
	if from != nil {
		path = fmt.Sprintf("/backup?from=%d", *from)
	}
	response, err := c.send(http.MethodGet, path, nil, "")
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		return nil, refusal(response)
	}
	defer response.Body.Close()
	return io.ReadAll(response.Body)
}
