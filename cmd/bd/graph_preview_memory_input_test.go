package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

type failingMemoryBodyReader struct{}

func (failingMemoryBodyReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestGraphPreviewMemoryBodyInput(t *testing.T) {
	body := "---\r\nmetadata: untouched\r\n---\n 雪 e\u0301 \t"
	path := filepath.Join(t.TempDir(), "body.md")
	if err := os.WriteFile(path, []byte(body), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name        string
		flags, args []string
		input, want string
		bad         bool
	}{
		{"positional", nil, []string{body}, "", body, false},
		{"file", []string{"--body-file", path}, nil, "", body, false},
		{"stdin", []string{"--stdin"}, nil, body, body, false},
		{"empty-stdin", []string{"--stdin"}, nil, "", "", false},
		{"empty-positional", nil, []string{""}, "", "", false},
		{"missing-file", []string{"--body-file", path + ".missing"}, nil, "", "", true},
		{"empty-path", []string{"--body-file="}, nil, "", "", true},
		{"false-stdin", []string{"--stdin=false"}, nil, "", "", true},
		{"missing", nil, nil, "", "", true},
		{"two-args", nil, []string{"a", "b"}, "", "", true},
		{"arg-and-file", []string{"--body-file", path}, []string{"body"}, "", "", true},
		{"file-and-stdin", []string{"--body-file", path, "--stdin"}, nil, "", "", true},
		{"arg-and-stdin", []string{"--stdin"}, []string{""}, "", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cmd := &cobra.Command{}
			cmd.Flags().String("body-file", "", "")
			cmd.Flags().Bool("stdin", false, "")
			if err := cmd.ParseFlags(tc.flags); err != nil {
				t.Fatal(err)
			}
			cmd.SetIn(strings.NewReader(tc.input))
			got, err := graphPreviewRememberBody(cmd, tc.args)
			if (err != nil) != tc.bad || got != tc.want {
				t.Fatalf("got %q, %v", got, err)
			}
		})
	}
}

func TestGraphPreviewMemoryBodyBoundedUTF8(t *testing.T) {
	for _, tc := range []struct {
		name   string
		reader io.Reader
		code   int
	}{
		{"boundary", strings.NewReader(strings.Repeat("x", graphPreviewMemoryBodyLimit)), 0},
		{"over", strings.NewReader(strings.Repeat("x", graphPreviewMemoryBodyLimit+1)), 5},
		{"invalid-utf8", strings.NewReader("\xff"), 2},
		{"read-failure", failingMemoryBodyReader{}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := graphPreviewReadMemoryBody(tc.reader)
			if tc.code == 0 {
				if err != nil || len(got) != graphPreviewMemoryBodyLimit {
					t.Fatal("boundary refused", err)
				}
				return
			}
			var failure *exitError
			if got != "" || !errors.As(err, &failure) || failure.Code != tc.code {
				t.Fatalf("partial body or wrong error: %q %v", got, err)
			}
		})
	}
}
