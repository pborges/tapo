package tapo

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestKLAPCryptoRoundTrip(t *testing.T) {
	t.Parallel()
	local := bytes.Repeat([]byte{1}, 16)
	remote := bytes.Repeat([]byte{2}, 16)
	auth := klapAuthHash(klapV2, "user@example.com", "correct horse")
	encryptor, err := newKlapSession(local, remote, auth)
	if err != nil {
		t.Fatal(err)
	}
	decryptor, err := newKlapSession(local, remote, auth)
	if err != nil {
		t.Fatal(err)
	}
	want := []byte(`{"system":{"get_sysinfo":null}}`)
	payload, sequence, err := encryptor.encrypt(want)
	if err != nil {
		t.Fatal(err)
	}
	got, err := decryptor.decrypt(payload, sequence)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("decrypt = %q, want %q", got, want)
	}
	payload[0] ^= 1
	if _, err := decryptor.decrypt(payload, sequence); err == nil {
		t.Fatal("decrypt accepted a bad signature")
	}
}

func TestKLAPV1AndV2HashesDiffer(t *testing.T) {
	t.Parallel()
	local := bytes.Repeat([]byte{3}, 16)
	remote := bytes.Repeat([]byte{4}, 16)
	v1 := klapAuthHash(klapV1, "user@example.com", "password")
	v2 := klapAuthHash(klapV2, "user@example.com", "password")
	if bytes.Equal(v1, v2) {
		t.Fatal("KLAP v1 and v2 auth hashes are equal")
	}
	if bytes.Equal(klapHandshake1Hash(klapV1, local, remote, v1), klapHandshake1Hash(klapV2, local, remote, v2)) {
		t.Fatal("KLAP v1 and v2 handshake hashes are equal")
	}
}

func TestKLAPTransportIntegration(t *testing.T) {
	const username, password = "owner@example.com", "secret"
	remoteSeed := bytes.Repeat([]byte{0x42}, 16)
	authHash := klapAuthHash(klapV2, username, password)
	var mu sync.Mutex
	var localSeed []byte
	var session *klapSession
	var handshake2OK bool

	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("read request: %v", err)
			writer.WriteHeader(http.StatusInternalServerError)
			return
		}
		mu.Lock()
		defer mu.Unlock()
		switch request.URL.Path {
		case "/app/handshake1":
			if len(body) != 16 {
				t.Errorf("handshake1 body is %d bytes", len(body))
				writer.WriteHeader(http.StatusBadRequest)
				return
			}
			localSeed = append([]byte{}, body...)
			writer.Header().Set("Set-Cookie", "TP_SESSIONID=test-session;TIMEOUT=86400")
			_, _ = writer.Write(append(append([]byte{}, remoteSeed...), klapHandshake1Hash(klapV2, localSeed, remoteSeed, authHash)...))
		case "/app/handshake2":
			if cookie, err := request.Cookie("TP_SESSIONID"); err != nil || cookie.Value != "test-session" {
				writer.WriteHeader(http.StatusForbidden)
				return
			}
			if !bytes.Equal(body, klapHandshake2Hash(klapV2, localSeed, remoteSeed, authHash)) {
				writer.WriteHeader(http.StatusForbidden)
				return
			}
			session, err = newKlapSession(localSeed, remoteSeed, authHash)
			if err != nil {
				t.Errorf("create server session: %v", err)
				writer.WriteHeader(http.StatusInternalServerError)
				return
			}
			handshake2OK = true
		case "/app/request":
			if !handshake2OK {
				writer.WriteHeader(http.StatusForbidden)
				return
			}
			sequence64, err := strconv.ParseInt(request.URL.Query().Get("seq"), 10, 32)
			if err != nil {
				t.Errorf("parse sequence: %v", err)
				writer.WriteHeader(http.StatusBadRequest)
				return
			}
			sequence := int32(sequence64)
			plain, err := session.decrypt(body, sequence)
			if err != nil || !bytes.Contains(plain, []byte(`"get_sysinfo"`)) {
				t.Errorf("decrypt request: plain=%q err=%v", plain, err)
				writer.WriteHeader(http.StatusBadRequest)
				return
			}
			response := []byte(`{"system":{"get_sysinfo":{"err_code":0,"alias":"KLAP strip","model":"HS300(US)","deviceId":"klap-id","children":[]}}}`)
			encrypted, responseSequence, err := session.encrypt(response)
			if err != nil || responseSequence != sequence {
				t.Errorf("encrypt response: sequence=%d want=%d err=%v", responseSequence, sequence, err)
				writer.WriteHeader(http.StatusInternalServerError)
				return
			}
			_, _ = writer.Write(encrypted)
		default:
			writer.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()

	parsed, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	host, portText, err := net.SplitHostPort(parsed.Host)
	if err != nil {
		t.Fatal(err)
	}
	httpPort, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	strip, err := New(host, WithPort(1), WithHTTPPort(httpPort), WithCredentials(username, password), WithTimeout(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	info, err := strip.Info(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.Alias != "KLAP strip" || strip.mode.Load() != transportKLAP {
		t.Fatalf("Info = %+v, mode = %d", info, strip.mode.Load())
	}
}

func TestKLAPAuthenticationError(t *testing.T) {
	t.Parallel()
	err := (&AuthenticationError{Host: "192.0.2.1", CredentialsProvided: true}).Error()
	if want := "credentials were supplied but rejected"; !bytes.Contains([]byte(err), []byte(want)) {
		t.Fatalf("AuthenticationError = %q, want hint %q", err, want)
	}
	err = (&AuthenticationError{Host: "192.0.2.1"}).Error()
	if want := "set TAPO_USERNAME and TAPO_PASSWORD"; !bytes.Contains([]byte(err), []byte(want)) {
		t.Fatalf("AuthenticationError = %q, want hint %q", err, want)
	}
}

func ExampleWithCredentials() {
	strip, err := New("192.0.2.1", WithCredentials("owner@example.com", "password"))
	fmt.Println(strip != nil, err == nil)
	// Output: true true
}
