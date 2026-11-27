package cli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/servtuner/servtuner/bench/internal/tlstest"
)

func TestThePriceBookClientCarriesTheRunToken(t *testing.T) {
	var got string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization") + " " + r.URL.Path
	}))
	defer server.Close()
	tokenFile := filepath.Join(t.TempDir(), "token")
	if err := os.WriteFile(tokenFile, []byte("secret-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, _, priceBook, err := newReporter(runFlags{apiURL: server.URL + "/", apiTLS: "false", runTokenFile: tokenFile}, "01ARZ3NDEKTSV4RRFFQ69G5FAV", streams{out: io.Discard, err: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	response, err := priceBook.Client.Get(priceBook.URL)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if got != "Bearer secret-token /runs/01ARZ3NDEKTSV4RRFFQ69G5FAV/datasheet" {
		t.Errorf("request %q", got)
	}
}

func TestThePriceBookClientUsesTheAPIHopsMTLS(t *testing.T) {
	var got string
	authority := tlstest.NewAuthority(t)
	server := tlstest.NewMTLSServer(t, authority, authority, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization") + " " + r.URL.Path
	}))
	directory := t.TempDir()
	certFile, keyFile := authority.IssueClient(t).WriteFiles(t, directory, "client")
	tokenFile := filepath.Join(directory, "token")
	if err := os.WriteFile(tokenFile, []byte("secret-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	flags := runFlags{
		apiURL: server.URL, apiTLS: "true", runTokenFile: tokenFile, tlsCertFile: certFile, tlsKeyFile: keyFile,
		tlsCAFile: tlstest.WriteFile(t, directory, "ca.crt", authority.CertPEM),
	}
	_, _, priceBook, err := newReporter(flags, "01ARZ3NDEKTSV4RRFFQ69G5FAV", streams{out: io.Discard, err: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	response, err := priceBook.Client.Get(priceBook.URL)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if got != "Bearer secret-token /runs/01ARZ3NDEKTSV4RRFFQ69G5FAV/datasheet" {
		t.Errorf("request %q", got)
	}

	// A server that does not trust the client's certificate refuses it.
	stranger := tlstest.NewMTLSServer(t, authority, tlstest.NewAuthority(t), http.NotFoundHandler())
	flags.apiURL = stranger.URL
	_, _, refused, err := newReporter(flags, "01ARZ3NDEKTSV4RRFFQ69G5FAV", streams{out: io.Discard, err: io.Discard})
	if err != nil {
		t.Fatal(err)
	}
	if response, err := refused.Client.Get(refused.URL); err == nil {
		response.Body.Close()
		t.Error("a server requiring another CA's client certificate accepted the price book client")
	}
}
