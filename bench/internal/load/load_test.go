package load

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
)

func TestParseLock(t *testing.T) {
	files, err := ParseLock(strings.NewReader("# comment\n\na deadbeef http://x/a\nb cafe http://x/b\n"))
	if err != nil || len(files) != 2 || files[1] != (File{"b", "cafe", "http://x/b"}) {
		t.Fatalf("files = %+v, err = %v", files, err)
	}
	for _, bad := range []string{"", "# only a comment\n", "a b\n"} {
		if _, err := ParseLock(strings.NewReader(bad)); err == nil {
			t.Errorf("ParseLock(%q): want error", bad)
		}
	}
}

func TestPagilaLockFile(t *testing.T) {
	f, err := os.Open("../../data/pagila.lock")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	files, err := ParseLock(f)
	if err != nil || len(files) != 2 {
		t.Fatalf("files = %+v, err = %v", files, err)
	}
	for _, x := range files {
		if len(x.SHA256) != 64 || !strings.Contains(x.URL, "/devrimgunduz/pagila/") || strings.Contains(x.URL, "/master/") {
			t.Errorf("lock line %+v is not pinned to a commit", x)
		}
	}
}

func TestFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/missing" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte("select 1;"))
	}))
	t.Cleanup(srv.Close)
	sum := sha256.Sum256([]byte("select 1;"))
	good := hex.EncodeToString(sum[:])

	tests := []struct {
		name     string
		files    []File
		wantErr  bool
		checksum bool
	}{
		{"match", []File{{"a.sql", good, srv.URL + "/a"}}, false, false},
		{"mismatch", []File{{"a.sql", strings.Repeat("0", 64), srv.URL + "/a"}}, true, true},
		{"second file mismatches, nothing returned", []File{{"a.sql", good, srv.URL + "/a"}, {"b.sql", strings.Repeat("1", 64), srv.URL + "/b"}}, true, true},
		{"not found", []File{{"a.sql", good, srv.URL + "/missing"}}, true, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Fetch(context.Background(), srv.Client(), tt.files)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			var ce *ChecksumError
			if errors.As(err, &ce) != tt.checksum {
				t.Fatalf("checksum error = %v, want %v (err %v)", ce, tt.checksum, err)
			}
			if ce != nil && (!strings.Contains(ce.Error(), ce.Expected) || !strings.Contains(ce.Error(), good)) {
				t.Errorf("message %q does not name expected and actual checksums", ce.Error())
			}
			if err != nil && got != nil {
				t.Errorf("returned content despite the error")
			}
		})
	}
}

func TestMeta(t *testing.T) {
	scale, pagila, err := ParseMeta(MetaText(0.1, true))
	if err != nil || scale != 0.1 || !pagila {
		t.Errorf("ParseMeta = %v %v %v", scale, pagila, err)
	}
	if _, _, err := ParseMeta(""); err == nil {
		t.Error("empty comment: want error")
	}
}
