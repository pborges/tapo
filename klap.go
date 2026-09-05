package tapo

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"
)

const (
	defaultHTTPPort = 80
	klapSessionLife = 23 * time.Hour
)

// AuthenticationError indicates that a KLAP device did not accept any of the
// configured, blank, or known setup credentials.
type AuthenticationError struct {
	Host                string
	CredentialsProvided bool
}

func (e *AuthenticationError) Error() string {
	if !e.CredentialsProvided {
		return "tapo: " + e.Host + " requires KLAP credentials; set TAPO_USERNAME and TAPO_PASSWORD"
	}
	return "tapo: KLAP authentication failed for " + e.Host +
		"; credentials were supplied but rejected (check the owning TP-Link account and Third-Party Compatibility)"
}

type klapVersion uint8

const (
	klapV1 klapVersion = 1
	klapV2 klapVersion = 2
)

type klapTransport struct {
	host, baseURL       string
	username, password  string
	credentialsProvided bool
	client              *http.Client
	mu                  sync.Mutex
	session             *klapSession
	sessionID           string
	expires             time.Time
}

func newKlapTransport(host string, port int, timeout time.Duration, username, password string) *klapTransport {
	transport := &http.Transport{
		DialContext: (&net.Dialer{Timeout: timeout}).DialContext,
	}
	return &klapTransport{
		host: host, baseURL: "http://" + net.JoinHostPort(host, strconv.Itoa(port)) + "/app",
		username: username, password: password, credentialsProvided: username != "" && password != "",
		client: &http.Client{Transport: transport, Timeout: timeout},
	}
}

func (k *klapTransport) address() string { return k.baseURL }

func (k *klapTransport) exchange(ctx context.Context, plain []byte) ([]byte, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if k.session == nil || time.Now().After(k.expires) {
		if err := k.handshake(ctx); err != nil {
			return nil, err
		}
	}

	reply, status, err := k.send(ctx, plain)
	if err != nil {
		return nil, err
	}
	if status == http.StatusForbidden {
		k.reset()
		if err := k.handshake(ctx); err != nil {
			return nil, err
		}
		reply, status, err = k.send(ctx, plain)
		if err != nil {
			return nil, err
		}
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("tapo: KLAP request returned HTTP %d", status)
	}
	return reply, nil
}

func (k *klapTransport) handshake(ctx context.Context) error {
	k.reset()
	localSeed := make([]byte, 16)
	if _, err := rand.Read(localSeed); err != nil {
		return fmt.Errorf("tapo: generate KLAP seed: %w", err)
	}
	status, header, response, err := k.post(ctx, "/handshake1", nil, localSeed, "")
	if err != nil {
		return fmt.Errorf("tapo: KLAP handshake1: %w", err)
	}
	if status != http.StatusOK {
		return fmt.Errorf("tapo: KLAP handshake1 returned HTTP %d", status)
	}
	if len(response) != 48 {
		return fmt.Errorf("tapo: KLAP handshake1 returned %d bytes, want 48", len(response))
	}
	remoteSeed, serverHash := response[:16], response[16:]
	authHash, version, ok := k.matchAuthHash(localSeed, remoteSeed, serverHash)
	if !ok {
		return &AuthenticationError{Host: k.host, CredentialsProvided: k.credentialsProvided}
	}
	for _, cookie := range (&http.Response{Header: header}).Cookies() {
		if cookie.Name == "TP_SESSIONID" {
			k.sessionID = cookie.Value
			break
		}
	}
	if k.sessionID == "" {
		return errors.New("tapo: KLAP handshake1 did not return TP_SESSIONID")
	}

	handshake2 := klapHandshake2Hash(version, localSeed, remoteSeed, authHash)
	status, _, _, err = k.post(ctx, "/handshake2", nil, handshake2, k.sessionID)
	if err != nil {
		return fmt.Errorf("tapo: KLAP handshake2: %w", err)
	}
	if status != http.StatusOK {
		return fmt.Errorf("tapo: KLAP handshake2 returned HTTP %d", status)
	}
	session, err := newKlapSession(localSeed, remoteSeed, authHash)
	if err != nil {
		return err
	}
	k.session = session
	k.expires = time.Now().Add(klapSessionLife)
	return nil
}

func (k *klapTransport) matchAuthHash(localSeed, remoteSeed, serverHash []byte) ([]byte, klapVersion, bool) {
	type credential struct{ username, password string }
	candidates := []credential{
		{k.username, k.password},
		{"kasa@tp-link.net", "kasaSetup"},
		{"test@tp-link.net", "test"},
		{"", ""},
	}
	seen := make(map[[32]byte]bool)
	for _, candidate := range candidates {
		for _, version := range []klapVersion{klapV2, klapV1} {
			authHash := klapAuthHash(version, candidate.username, candidate.password)
			key := sha256.Sum256(append([]byte{byte(version)}, authHash...))
			if seen[key] {
				continue
			}
			seen[key] = true
			want := klapHandshake1Hash(version, localSeed, remoteSeed, authHash)
			if subtle.ConstantTimeCompare(want, serverHash) == 1 {
				return authHash, version, true
			}
		}
	}
	return nil, 0, false
}

func (k *klapTransport) send(ctx context.Context, plain []byte) ([]byte, int, error) {
	payload, sequence, err := k.session.encrypt(plain)
	if err != nil {
		return nil, 0, err
	}
	query := url.Values{"seq": []string{strconv.FormatInt(int64(sequence), 10)}}
	status, _, response, err := k.post(ctx, "/request", query, payload, k.sessionID)
	if err != nil || status != http.StatusOK {
		return nil, status, err
	}
	plainResponse, err := k.session.decrypt(response, sequence)
	if err != nil {
		return nil, status, fmt.Errorf("tapo: decrypt KLAP response: %w", err)
	}
	return plainResponse, status, nil
}

func (k *klapTransport) post(ctx context.Context, path string, query url.Values, body []byte, sessionID string) (int, http.Header, []byte, error) {
	endpoint := k.baseURL + path
	if len(query) != 0 {
		endpoint += "?" + query.Encode()
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return 0, nil, nil, err
	}
	request.Header.Set("Content-Type", "application/octet-stream")
	if sessionID != "" {
		request.AddCookie(&http.Cookie{Name: "TP_SESSIONID", Value: sessionID})
	}
	response, err := k.client.Do(request)
	if err != nil {
		return 0, nil, nil, err
	}
	defer response.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(response.Body, maxFrameLen+1))
	if err != nil {
		return 0, nil, nil, err
	}
	if len(payload) > maxFrameLen {
		return 0, nil, nil, errors.New("tapo: KLAP response is too large")
	}
	return response.StatusCode, response.Header.Clone(), payload, nil
}

func (k *klapTransport) reset() {
	k.session, k.sessionID = nil, ""
	k.expires = time.Time{}
}

func klapAuthHash(version klapVersion, username, password string) []byte {
	if version == klapV2 {
		usernameHash := sha1.Sum([]byte(username))
		passwordHash := sha1.Sum([]byte(password))
		joined := append(usernameHash[:], passwordHash[:]...)
		hash := sha256.Sum256(joined)
		return hash[:]
	}
	usernameHash := md5.Sum([]byte(username)) // Required by the KLAP v1 protocol.
	passwordHash := md5.Sum([]byte(password)) // Required by the KLAP v1 protocol.
	joined := append(usernameHash[:], passwordHash[:]...)
	hash := md5.Sum(joined) // Required by the KLAP v1 protocol.
	return hash[:]
}

func klapHandshake1Hash(version klapVersion, localSeed, remoteSeed, authHash []byte) []byte {
	payload := append([]byte{}, localSeed...)
	if version == klapV2 {
		payload = append(payload, remoteSeed...)
	}
	payload = append(payload, authHash...)
	hash := sha256.Sum256(payload)
	return hash[:]
}

func klapHandshake2Hash(version klapVersion, localSeed, remoteSeed, authHash []byte) []byte {
	payload := append([]byte{}, remoteSeed...)
	if version == klapV2 {
		payload = append(payload, localSeed...)
	}
	payload = append(payload, authHash...)
	hash := sha256.Sum256(payload)
	return hash[:]
}

type klapSession struct {
	block cipher.Block
	iv    [12]byte
	seq   int32
	sign  [28]byte
}

func newKlapSession(localSeed, remoteSeed, authHash []byte) (*klapSession, error) {
	material := func(prefix string) [32]byte {
		payload := append([]byte(prefix), localSeed...)
		payload = append(payload, remoteSeed...)
		payload = append(payload, authHash...)
		return sha256.Sum256(payload)
	}
	key := material("lsk")
	block, err := aes.NewCipher(key[:16])
	if err != nil {
		return nil, fmt.Errorf("tapo: create KLAP cipher: %w", err)
	}
	ivMaterial := material("iv")
	signMaterial := material("ldk")
	session := &klapSession{block: block, seq: int32(binary.BigEndian.Uint32(ivMaterial[28:]))}
	copy(session.iv[:], ivMaterial[:12])
	copy(session.sign[:], signMaterial[:28])
	return session, nil
}

func (s *klapSession) encrypt(plain []byte) ([]byte, int32, error) {
	s.seq++
	sequence := s.seq
	iv := s.sequenceIV(sequence)
	padded := pkcs7Pad(plain, aes.BlockSize)
	ciphertext := make([]byte, len(padded))
	cipher.NewCBCEncrypter(s.block, iv).CryptBlocks(ciphertext, padded)
	signature := s.signature(sequence, ciphertext)
	return append(signature, ciphertext...), sequence, nil
}

func (s *klapSession) decrypt(payload []byte, sequence int32) ([]byte, error) {
	if len(payload) < sha256.Size+aes.BlockSize || (len(payload)-sha256.Size)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("invalid payload length %d", len(payload))
	}
	signature, ciphertext := payload[:sha256.Size], payload[sha256.Size:]
	if subtle.ConstantTimeCompare(signature, s.signature(sequence, ciphertext)) != 1 {
		return nil, errors.New("invalid response signature")
	}
	plain := make([]byte, len(ciphertext))
	cipher.NewCBCDecrypter(s.block, s.sequenceIV(sequence)).CryptBlocks(plain, ciphertext)
	return pkcs7Unpad(plain, aes.BlockSize)
}

func (s *klapSession) sequenceIV(sequence int32) []byte {
	iv := make([]byte, aes.BlockSize)
	copy(iv, s.iv[:])
	binary.BigEndian.PutUint32(iv[12:], uint32(sequence))
	return iv
}

func (s *klapSession) signature(sequence int32, ciphertext []byte) []byte {
	payload := make([]byte, 0, len(s.sign)+4+len(ciphertext))
	payload = append(payload, s.sign[:]...)
	var encoded [4]byte
	binary.BigEndian.PutUint32(encoded[:], uint32(sequence))
	payload = append(payload, encoded[:]...)
	payload = append(payload, ciphertext...)
	hash := sha256.Sum256(payload)
	return hash[:]
}

func pkcs7Pad(plain []byte, blockSize int) []byte {
	padding := blockSize - len(plain)%blockSize
	return append(append([]byte{}, plain...), bytes.Repeat([]byte{byte(padding)}, padding)...)
}

func pkcs7Unpad(padded []byte, blockSize int) ([]byte, error) {
	if len(padded) == 0 || len(padded)%blockSize != 0 {
		return nil, errors.New("invalid PKCS#7 padded length")
	}
	padding := int(padded[len(padded)-1])
	if padding == 0 || padding > blockSize || padding > len(padded) {
		return nil, errors.New("invalid PKCS#7 padding")
	}
	for _, value := range padded[len(padded)-padding:] {
		if int(value) != padding {
			return nil, errors.New("invalid PKCS#7 padding")
		}
	}
	return padded[:len(padded)-padding], nil
}
