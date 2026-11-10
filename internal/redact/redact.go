// Package redact removes credentials from configured URLs and from error texts
// that quote them, before they reach a log, a response or an agent card.
package redact

import (
	"net/url"
	"strings"
)

// InvalidURL stands in for a URL that cannot be parsed.
const InvalidURL = "<invalid url>"

// URL keeps the scheme, host, port and path of raw and drops user information,
// query and fragment, which may carry credentials. It is used for every
// configured URL that goes into an agent card, a log or an error.
func URL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return InvalidURL
	}
	return (&url.URL{Scheme: u.Scheme, Host: u.Host, Path: u.Path, RawPath: u.RawPath}).String()
}

// Error returns the text of err with raw, and the user information, query and
// fragment of raw, removed as URL does. net/http reports a request URL with its
// password masked but its user name and query intact, so those forms are
// removed as well.
func Error(err error, raw string) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	if raw == "" {
		return text
	}
	text = strings.ReplaceAll(text, raw, URL(raw))
	u, perr := url.Parse(raw)
	if perr != nil {
		return text
	}
	if u.User != nil {
		text = strings.ReplaceAll(text, u.User.String()+"@", "")
		if name := u.User.Username(); name != "" {
			text = strings.ReplaceAll(text, name+":***@", "")
		}
	}
	if u.RawQuery != "" {
		text = strings.ReplaceAll(text, "?"+u.RawQuery, "")
	}
	if u.Fragment != "" {
		text = strings.ReplaceAll(text, "#"+u.EscapedFragment(), "")
	}
	return text
}
