package cli

import (
	"errors"
	"io/fs"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

// Plain is err's text with what the system said put in words. Go names the
// call and its error, as in "open rows.jsonl: no such file or directory" or
// `Get "https://host/path": dial tcp 127.0.0.1:443: connect: connection
// refused`, and a reader named the file or the host, never the call.
func Plain(err error) string {
	text := err.Error()
	if file := new(fs.PathError); errors.As(err, &file) {
		text = strings.Replace(text, file.Error(), fileSentence(file), 1)
	}
	if call := new(url.Error); errors.As(err, &call) {
		text = strings.Replace(text, call.Error(), callSentence(call), 1)
	}
	if program := new(exec.Error); errors.As(err, &program) && errors.Is(program.Err, exec.ErrNotFound) {
		text = strings.Replace(text, program.Error(), program.Name+" is not on PATH", 1)
	}
	return text
}

// File is err said by the file it ends on, when it ends on one: "read a
// workflow: stat x: no such file or directory" reads "x does not exist". It
// is for a file the user named, where what each caller put before the file
// names a step of the program and not the thing that is wrong.
func File(err error) error {
	if file := new(fs.PathError); errors.As(err, &file) && strings.HasSuffix(err.Error(), file.Error()) {
		return file
	}
	return err
}

// unnamed is an error without a package's name in its text.
type unnamed struct {
	error
	text string
}

func (u unnamed) Error() string { return u.text }

func (u unnamed) Unwrap() error { return u.error }

// Without is err without the name of the package that made it, where that
// name opens its text or one of the parts a colon divides it into. A package
// opens its errors with its own name, as in "policy: rows line 1", and the
// name is the program's, not a thing the reader named.
func Without(name string, err error) error {
	text := err.Error()
	if rest, opens := strings.CutPrefix(text, name+": "); opens {
		return unnamed{error: err, text: rest}
	}
	if before, after, found := strings.Cut(text, ": "+name+": "); found {
		return unnamed{error: err, text: before + ": " + after}
	}
	return err
}

func fileSentence(file *fs.PathError) string {
	switch {
	case errors.Is(file.Err, fs.ErrNotExist):
		// A file that was to be written is missing for its directory's sake.
		if _, err := os.Stat(filepath.Dir(file.Path)); errors.Is(err, fs.ErrNotExist) {
			return file.Path + " is in a directory that does not exist"
		}
		return file.Path + " does not exist"
	case errors.Is(file.Err, fs.ErrExist):
		return file.Path + " exists"
	case errors.Is(file.Err, syscall.EISDIR):
		return file.Path + " is a directory"
	}
	return file.Path + ": " + file.Err.Error()
}

// callSentence names the host a request went to and why it got no answer.
func callSentence(call *url.Error) string {
	host := call.URL
	if address, err := url.Parse(call.URL); err == nil && address.Host != "" {
		host = address.Host
	}
	if call.Timeout() {
		return host + " did not answer in time"
	}
	if lookup := new(net.DNSError); errors.As(call.Err, &lookup) && lookup.IsNotFound {
		return host + " did not answer: no such host"
	}
	cause := call.Err
	for inner := errors.Unwrap(cause); inner != nil; inner = errors.Unwrap(cause) {
		cause = inner
	}
	return host + " did not answer: " + cause.Error()
}
