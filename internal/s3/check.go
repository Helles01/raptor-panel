package s3

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

// Step is one thing Check tried, and what came of it.
type Step struct {
	Name string
	// Err is nil when the step worked.
	Err error
}

const checkBody = "raptor check: this object is made and removed by `raptor backups check`"

// Check tries every way the panel uses the bucket, with an object of its own that it removes again: an upload is started, a part is sent through a signed address
// (as a node would send it), the parts are put together, the object is read through a signed address (as a browser would), deleted, and looked for. It
// says what worked and where it stopped. It makes real requests, and is for the administrator to run when the settings are new. A step that fails ends the
// check, and what it left in the bucket is thrown away as well as it can be.
func (c *Client) Check(ctx context.Context) []Step {
	var steps []Step
	fail := func(name string, err error) []Step { return append(steps, Step{Name: name, Err: err}) }
	ok := func(name string) { steps = append(steps, Step{Name: name}) }

	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return fail("make a name for the test object", err)
	}
	key := "raptor-check/" + hex.EncodeToString(raw) + ".tar.gz"

	// Whatever happens below, nothing of the check is left in the bucket if it can be helped.
	var uploadID string
	var objectMaybe bool
	cleanup := func() {
		if uploadID != "" {
			_ = c.AbortMultipartUpload(ctx, key, uploadID)
		}
		if objectMaybe {
			_ = c.DeleteObject(ctx, key)
		}
	}

	id, err := c.CreateMultipartUpload(ctx, key)
	if err != nil {
		return fail("start an upload", err)
	}
	uploadID = id
	ok("start an upload")

	partURL, err := c.PresignUploadPart(key, uploadID, 1, 10*time.Minute)
	if err != nil {
		cleanup()
		return fail("sign an address for a part", err)
	}
	etag, err := c.sendPart(ctx, partURL, []byte(checkBody))
	if err != nil {
		cleanup()
		return fail("send a part through a signed address", err)
	}
	ok("send a part through a signed address")

	objectMaybe = true
	if err := c.CompleteMultipartUpload(ctx, key, uploadID, []Part{{Number: 1, ETag: etag}}); err != nil {
		cleanup()
		return fail("put the parts together", err)
	}
	uploadID = ""
	ok("put the parts together")

	getURL, err := c.PresignGet(key, 5*time.Minute)
	if err != nil {
		cleanup()
		return fail("sign an address to read the object", err)
	}
	got, err := c.fetch(ctx, getURL)
	if err != nil {
		cleanup()
		return fail("read the object through a signed address", err)
	}
	if string(got) != checkBody {
		cleanup()
		return fail("read the object through a signed address", errors.New("s3: what was read is not what was sent"))
	}
	ok("read the object through a signed address")

	if err := c.DeleteObject(ctx, key); err != nil {
		cleanup()
		return fail("delete the object", err)
	}
	objectMaybe = false
	ok("delete the object")

	if _, found, err := c.Head(ctx, key); err != nil {
		return fail("see that the object is gone", err)
	} else if found {
		return fail("see that the object is gone", errors.New("s3: the object is still there after it was deleted"))
	}
	ok("see that the object is gone")
	return steps
}

// sendPart sends a body to a signed address of a part and returns the tag the server gave it.
func (c *Client) sendPart(ctx context.Context, address string, body []byte) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, shortWait)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, address, bytes.NewReader(body))
	if err != nil {
		return "", errors.New("s3: the signed address is not usable")
	}
	req.Header.Set("Content-Type", ContentType)
	resp, err := c.http.Do(req)
	if err != nil {
		return "", transportError(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return "", errorOf(reply{status: resp.StatusCode, body: data})
	}
	etag := resp.Header.Get("ETag")
	if etag == "" {
		return "", errors.New("s3: the server gave no entity tag for the part")
	}
	return etag, nil
}

// fetch reads what a signed address gives.
func (c *Client) fetch(ctx context.Context, address string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, shortWait)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, errors.New("s3: the signed address is not usable")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, transportError(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(resp.Body, maxResponse))
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, errorOf(reply{status: resp.StatusCode, body: data})
	}
	return data, nil
}

// transportError is what a request that did not get an answer says, without the address, which carries a signature.
func transportError(err error) error {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	return fmt.Errorf("s3: %w", err)
}
