package webdav

import (
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"strconv"
	"strings"
)

// davNamespace is the WebDAV XML namespace URI (RFC 4918); the usual prefix is
// "D:" but the parser matches on the namespace URI, not the prefix.
const davNamespace = "DAV:"

// maxResponses bounds response processing even for pathological-but-valid
// bodies; combined with the 1 MiB body cap this keeps parsing bounded.
const maxResponses = 10000

// Sentinel parse/validation errors. They are mapped to stable reason codes in
// webdav.go and never carry body content.
var (
	errMalformedXML         = errors.New("not well-formed XML")
	errWrongRoot            = errors.New("root element is not DAV multistatus")
	errNoResponse           = errors.New("multistatus contains no response")
	errNoHref               = errors.New("no response contains an href")
	errNoSuccessfulPropstat = errors.New("a response has no propstat with a 2xx status")
	errTooManyResponses     = errors.New("too many responses")
)

// validateMultistatus parses a bounded PROPFIND response body with the standard
// streaming XML parser and validates the WebDAV multistatus structure using
// explicit namespace/local-name checks:
//
//   - the root element must be {DAV:}multistatus,
//   - at least one {DAV:}response must exist and at least one response must
//     contain a non-empty {DAV:}href,
//   - every response must contain at least one {DAV:}propstat whose
//     {DAV:}status is a 2xx HTTP status (e.g. "HTTP/1.1 200 OK").
//
// It returns the number of responses on success. The body must already be
// size-capped by the caller; no body content is ever returned or included in
// errors.
func validateMultistatus(body []byte) (int, error) {
	dec := xml.NewDecoder(bytes.NewReader(body))
	dec.Strict = true

	var (
		stack      []xml.Name
		rootSeen   bool
		inResponse bool
		inPropstat bool
		inHref     bool
		inStatus   bool
		curHref    bool
		cur2xx     bool
		responses  int
		withHref   int
		no2xx      bool
		text       strings.Builder
	)

	isDAV := func(n xml.Name, local string) bool {
		return n.Space == davNamespace && n.Local == local
	}

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return 0, errMalformedXML
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if !rootSeen {
				rootSeen = true
				if t.Name.Space != davNamespace || t.Name.Local != "multistatus" {
					return 0, errWrongRoot
				}
			}
			stack = append(stack, t.Name)
			depth := len(stack)
			switch {
			case depth == 2 && isDAV(t.Name, "response"):
				responses++
				if responses > maxResponses {
					return 0, errTooManyResponses
				}
				inResponse = true
				curHref = false
				cur2xx = false
			case inResponse && depth == 3 && isDAV(t.Name, "href"):
				inHref = true
				text.Reset()
			case inResponse && depth == 3 && isDAV(t.Name, "propstat"):
				inPropstat = true
			case inPropstat && depth == 4 && isDAV(t.Name, "status"):
				inStatus = true
				text.Reset()
			}
		case xml.CharData:
			if inHref || inStatus {
				text.Write(t)
			}
		case xml.EndElement:
			depth := len(stack)
			switch {
			case inHref && depth == 3 && isDAV(t.Name, "href"):
				if strings.TrimSpace(text.String()) != "" {
					curHref = true
				}
				inHref = false
			case inStatus && depth == 4 && isDAV(t.Name, "status"):
				if code := parseStatusCode(text.String()); code >= 200 && code <= 299 {
					cur2xx = true
				}
				inStatus = false
			case inPropstat && depth == 3 && isDAV(t.Name, "propstat"):
				inPropstat = false
			case inResponse && depth == 2 && isDAV(t.Name, "response"):
				inResponse = false
				if curHref {
					withHref++
				}
				if !cur2xx {
					no2xx = true
				}
			}
			if len(stack) > 0 {
				stack = stack[:len(stack)-1]
			}
		}
	}

	if !rootSeen {
		return 0, errMalformedXML // empty body
	}
	if responses == 0 {
		return 0, errNoResponse
	}
	if withHref == 0 {
		return 0, errNoHref
	}
	if no2xx {
		return 0, errNoSuccessfulPropstat
	}
	return responses, nil
}

// parseStatusCode extracts the numeric code from a WebDAV status line such as
// "HTTP/1.1 200 OK". It returns -1 for anything unparseable.
func parseStatusCode(s string) int {
	fields := strings.Fields(s)
	if len(fields) < 2 {
		return -1
	}
	code, err := strconv.Atoi(fields[1])
	if err != nil {
		return -1
	}
	return code
}
