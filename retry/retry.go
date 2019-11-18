// Package retry implements a retrying transport based on a combination of strategies.
package retry

import (
	"io"
	"io/ioutil"
	"net/http"
	"time"
)

var (
	now = time.Now

	// Limit the size to consume when draining the body
	// to maintain http connections.
	bodyReadLimit = int64(4096)
)

// Attempt counts the round trips issued, starting from 1.  Response is valid
// only when Err is nil.
type Attempt struct {
	Start time.Time
	Count uint
	Err   error
	*http.Request
	*http.Response
}

// Delayer sleeps or selects any amount of time for each attempt.
type Delayer func(Attempt)

// Decision signals the intent of a Retryer
type Decision int

const (
	Ignore Decision = iota
	Retry
	Abort
)

// Retryer chooses whether or not to retry this request.  The error is only
// valid when the Retyer returns Abort.
type Retryer func(Attempt) (Decision, error)

// Allows to use loggers.
type Logger interface {
	Printf(string, ...interface{})
}

type Transport struct {
	// Delay is called for attempts that are retried.  If nil, no delay will be used.
	Delay Delayer

	// Retry is called for every attempt
	Retry Retryer

	// Next is called for every attempt
	Next http.RoundTripper

	// Customer logger instance.
	Logger Logger
}

// RoundTrip delegates a RoundTrip, then determines via Retry whether to retry
// and Delay for the wait time between attempts.
func (t Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	var (
		retryer = t.Retry
		start   = now()
	)
	if retryer == nil {
		retryer = DefaultRetryer
	}

	for count := uint(1); ; count++ {
		if count > 1 {
			t.logf("[DEBUG] retrying %s %v, attempt: %d", req.Method, req.URL, count)
		}

		// Perform request
		resp, err := t.Next.RoundTrip(req)

		if err != nil {
			t.logf("[INFO] %s %v, request error: %s", req.Method, req.URL, err)
		}

		// Collect result of attempt
		attempt := Attempt{
			Start:    start,
			Count:    count,
			Err:      err,
			Request:  req,
			Response: resp,
		}

		// Evaluate attempt
		retry, retryErr := retryer(attempt)

		if retryErr != nil {
			t.logf("[INFO] %s %v, retryer error: %s", req.Method, req.URL, retryErr)
		}

		// Returns either the valid response or an error coming from the underlying Transport
		if retry == Ignore {
			return resp, err
		}

		// Return the error explaining why we aborted and nil as response
		if retry == Abort {
			t.logf("[ERROR] aborting request %s %v, error: %s", req.Method, req.URL, retryErr)

			return resp, retryErr
		}

		// Drain and close the response body to let the Transport reuse the connection
		// when we wont use it anymore (Retry).
		if resp != nil {
			_, err := io.Copy(ioutil.Discard, io.LimitReader(resp.Body, bodyReadLimit))
			if err != nil {
				t.logf("[ERROR] error reading response body: %s", req.Method, req.URL, retryErr)
			}

			resp.Body.Close()
		}

		// ... Retries (stay the loop)

		// Delay next attempt
		if t.Delay != nil {
			t.logf("[DEBUG] delaying before retry %s %v", req.Method, req.URL)

			t.Delay(attempt)
		}
	}
	panic("unreachable")
}

func (t Transport) logf(format string, v ...interface{}) {
	if t.Logger != nil {
		t.Logger.Printf(format, v...)
	}
}
