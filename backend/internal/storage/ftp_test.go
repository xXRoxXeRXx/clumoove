package storage

import (
	"context"
	"errors"
	"net/textproto"
	"testing"

	"github.com/jlaffaye/ftp"
)

func TestParseFTPURL(t *testing.T) {
	tests := []struct {
		name     string
		url      string
		wantHost string
		wantPort string
		wantMode ftpTLSMode
		wantErr  bool
	}{
		{name: "explicit defaults", url: "ftp://files.example.test?tls=explicit", wantHost: "files.example.test", wantPort: "21", wantMode: ftpExplicitTLS},
		{name: "explicit custom port", url: "ftp://files.example.test:2121?tls=explicit", wantHost: "files.example.test", wantPort: "2121", wantMode: ftpExplicitTLS},
		{name: "implicit defaults", url: "ftps://files.example.test", wantHost: "files.example.test", wantPort: "990", wantMode: ftpImplicitTLS},
		{name: "implicit custom port", url: "ftps://files.example.test:2990", wantHost: "files.example.test", wantPort: "2990", wantMode: ftpImplicitTLS},
		{name: "plaintext rejected", url: "ftp://files.example.test", wantErr: true},
		{name: "wrong TLS value", url: "ftp://files.example.test?tls=implicit", wantErr: true},
		{name: "unexpected parameter", url: "ftp://files.example.test?tls=explicit&mode=passive", wantErr: true},
		{name: "duplicate TLS parameter", url: "ftp://files.example.test?tls=explicit&tls=explicit", wantErr: true},
		{name: "userinfo rejected", url: "ftps://user:password@files.example.test", wantErr: true},
		{name: "path rejected", url: "ftps://files.example.test/root", wantErr: true},
		{name: "fragment rejected", url: "ftps://files.example.test#fragment", wantErr: true},
		{name: "wrong scheme", url: "https://files.example.test", wantErr: true},
		{name: "bad port", url: "ftps://files.example.test:70000", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			host, port, mode, err := parseFTPURL(tt.url)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatalf("parseFTPURL() error = %v", err)
			}
			if host != tt.wantHost || port != tt.wantPort || mode != tt.wantMode {
				t.Fatalf("parseFTPURL() = (%q, %q, %v), want (%q, %q, %v)", host, port, mode, tt.wantHost, tt.wantPort, tt.wantMode)
			}
		})
	}
}

func TestFTPPath(t *testing.T) {
	tests := []struct {
		input, want string
		wantErr     bool
	}{
		{input: "", want: "/"},
		{input: "/", want: "/"},
		{input: "folder/file.txt", want: "/folder/file.txt"},
		{input: "/folder//file.txt", want: "/folder/file.txt"},
		{input: "../secret", wantErr: true},
		{input: "/folder/../../secret", wantErr: true},
	}
	for _, tt := range tests {
		got, err := ftpPath(tt.input)
		if tt.wantErr {
			if !errors.Is(err, ErrPathEscapesRoot) {
				t.Fatalf("ftpPath(%q) error = %v, want ErrPathEscapesRoot", tt.input, err)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Fatalf("ftpPath(%q) = (%q, %v), want (%q, nil)", tt.input, got, err, tt.want)
		}
	}
}

func TestFTPAuthenticationError(t *testing.T) {
	if !isFTPAuthError(&textproto.Error{Code: 530, Msg: "login incorrect"}) {
		t.Fatal("530 must be classified as authentication failure")
	}
	if isFTPAuthError(&textproto.Error{Code: 550, Msg: "missing"}) {
		t.Fatal("550 must not be classified as authentication failure")
	}
}

func TestFTPProviderRefreshesDialContextOnSessionReuse(t *testing.T) {
	p := &FTPProvider{
		client:      &ftp.ServerConn{},
		dialContext: context.Background(),
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := p.ensureConnected(ctx); err != nil {
		t.Fatalf("ensureConnected() error = %v", err)
	}
	if p.dialContext != ctx {
		t.Fatal("ensureConnected() did not refresh the passive data dial context")
	}
}

func TestFTPProviderFilesOnly(t *testing.T) {
	p, err := NewFTPProvider("ftps://example.com", "user", "password")
	if err != nil {
		t.Fatalf("NewFTPProvider() error = %v", err)
	}
	if !p.SupportsAtomicRename() {
		t.Fatal("FTPS provider must support atomic rename")
	}
	if _, err := p.GetFileHash(context.Background(), "files", "/file.txt"); !errors.Is(err, ErrHashNotSupported) {
		t.Fatalf("GetFileHash() error = %v, want ErrHashNotSupported", err)
	}
	for _, resourceType := range []string{"calendars", "contacts"} {
		if _, err := p.GetDirectoryListing(context.Background(), resourceType, "/"); !errors.Is(err, ErrUnsupportedResourceType) {
			t.Fatalf("GetDirectoryListing(%q) error = %v, want ErrUnsupportedResourceType", resourceType, err)
		}
	}
}
