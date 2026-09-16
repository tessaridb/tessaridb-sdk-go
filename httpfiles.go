package tessaridb

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
)

// The object routes.
//
// A bucket is declared with DEFINE BUCKET and is not a table. This client does
// not check that — the catalog is the server's — but the first attempt against a
// name that is a table answers 404, which reads like a routing fault and is not
// one.

// A FileEntry is one element of a bucket listing. Path is always present; Size
// and Updated appear only where the store recorded them, and are nil rather than
// zero because absence here says the store never recorded it rather than that it
// recorded nothing.
type FileEntry struct {
	Path    string  `json:"path"`
	Size    *uint64 `json:"size"`
	Updated *string `json:"updated"`
}

const unreserved = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-._~"

// The server percent-decodes the file path, so a client MUST encode it. Every
// byte outside the unreserved set is escaped and '/' is left as itself, because
// a slash in a file name reaches the server as a slash.
//
// Deliberately not url.PathEscape, which leaves sub-delims like '$', '&' and
// '+' alone. Encoding more than the minimum is safe — the server decodes what
// arrives — and encoding less is not: an unencoded space makes the request LINE
// unparseable rather than merely wrong, and an unencoded '%' asks the server to
// decode an escape the caller never wrote.
func encodeFilePath(path string) string {
	const hexDigits = "0123456789ABCDEF"
	var out strings.Builder
	for i := 0; i < len(path); i++ {
		c := path[i]
		if c == '/' || strings.IndexByte(unreserved, c) >= 0 {
			out.WriteByte(c)
			continue
		}
		out.WriteByte('%')
		out.WriteByte(hexDigits[c>>4])
		out.WriteByte(hexDigits[c&0x0f])
	}
	return out.String()
}

func bucketPath(namespace, database, bucket string) string {
	return "/files/" + namespace + "/" + database + "/" + bucket
}

// Put writes a file. POST is a synonym the server accepts and this client does
// not offer: a second verb for one action widens the surface for nothing.
func (c *HTTPClient) Put(namespace, database, bucket, path string, content []byte) error {
	target := bucketPath(namespace, database, bucket) + "/" + encodeFilePath(path)
	response, err := c.send(http.MethodPut, target, content, "application/octet-stream")
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusCreated {
		return refusal(response)
	}
	defer response.Body.Close()
	_, _ = io.Copy(io.Discard, response.Body)
	return nil
}

// Get reads a file. A 404 is an ANSWER — the file is not there — and it comes
// back as a nil slice with no error. A file that exists and is empty comes back
// as an empty non-nil slice: those are different facts, the server draws the
// line, and a client that reported both as "no bytes" would erase it.
func (c *HTTPClient) Get(namespace, database, bucket, path string) ([]byte, error) {
	target := bucketPath(namespace, database, bucket) + "/" + encodeFilePath(path)
	response, err := c.send(http.MethodGet, target, nil, "")
	if err != nil {
		return nil, err
	}
	if response.StatusCode == http.StatusNotFound {
		response.Body.Close()
		return nil, nil
	}
	if response.StatusCode != http.StatusOK {
		return nil, refusal(response)
	}
	defer response.Body.Close()
	content, err := io.ReadAll(response.Body)
	if content == nil {
		content = []byte{}
	}
	return content, err
}

// List returns a bucket's files. A nil listing means the name is not a bucket —
// either declared as something else or declared nowhere, and the server does not
// separate those by status because listing asks one question and gets one
// answer. An empty non-nil listing means the bucket is there and holds nothing.
//
// HEAD is not offered: the node reads the whole object and discards the body, so
// it costs the server exactly what a GET costs, and presenting it as a cheap
// exists() would be an invitation to call it in a loop.
func (c *HTTPClient) List(namespace, database, bucket string) ([]FileEntry, error) {
	response, err := c.send(http.MethodGet, bucketPath(namespace, database, bucket), nil, "")
	if err != nil {
		return nil, err
	}
	if response.StatusCode == http.StatusNotFound {
		response.Body.Close()
		return nil, nil
	}
	if response.StatusCode != http.StatusOK {
		return nil, refusal(response)
	}
	defer response.Body.Close()
	var answer struct {
		Files []FileEntry `json:"files"`
	}
	if err := json.NewDecoder(response.Body).Decode(&answer); err != nil {
		return nil, err
	}
	if answer.Files == nil {
		answer.Files = []FileEntry{}
	}
	return answer.Files, nil
}

// Remove deletes a file. It answers the same whether or not the file was there,
// so deletion is idempotent — and this client says nothing about which happened,
// because the server reports no difference and claiming to know would be an
// invention.
func (c *HTTPClient) Remove(namespace, database, bucket, path string) error {
	target := bucketPath(namespace, database, bucket) + "/" + encodeFilePath(path)
	response, err := c.send(http.MethodDelete, target, nil, "")
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusNoContent {
		return refusal(response)
	}
	response.Body.Close()
	return nil
}
