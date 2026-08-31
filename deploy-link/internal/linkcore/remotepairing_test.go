package linkcore

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"
)

func serveRemotePairingProbe(t *testing.T, response []byte) (string, int) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	address := listener.Addr().(*net.TCPAddr)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer connection.Close()
		header := make([]byte, len(remotePairingMagic)+2)
		if _, readErr := io.ReadFull(connection, header); readErr != nil {
			return
		}
		bodyLength := int(binary.BigEndian.Uint16(header[len(remotePairingMagic):]))
		if bodyLength > 0 {
			_, _ = io.CopyN(io.Discard, connection, int64(bodyLength))
		}
		frame := make([]byte, len(remotePairingMagic)+2+len(response))
		copy(frame, remotePairingMagic)
		binary.BigEndian.PutUint16(frame[len(remotePairingMagic):], uint16(len(response)))
		copy(frame[len(remotePairingMagic)+2:], response)
		_, _ = connection.Write(frame)
	}()
	return address.IP.String(), address.Port
}

func TestFindRemotePairingPortRequiresProtocolHandshake(t *testing.T) {
	response := []byte(`{"message":{"plain":{"_0":{"response":{"_1":{"handshake":{"_0":{"wireProtocolVersion":19,"deviceOptions":{"allowsIncomingTunnelConnections":true}}}}}}}}}`)
	target, port := serveRemotePairingProbe(t, response)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	found, err := findRemotePairingPort(ctx, target, port, port, 250*time.Millisecond, 1)
	if err != nil {
		t.Fatal(err)
	}
	if found != port {
		t.Fatalf("found port = %d, want %d", found, port)
	}
}

func TestFindRemotePairingPortRejectsNonRemotePairingResponse(t *testing.T) {
	target, port := serveRemotePairingProbe(t, []byte(`{"ok":true}`))
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := findRemotePairingPort(ctx, target, port, port, 250*time.Millisecond, 1); errorCode(err) != "remote_pairing_cold" {
		t.Fatalf("error = %v, want remote_pairing_cold", err)
	}
}

func TestFindRemotePairingPortHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := findRemotePairingPort(ctx, "127.0.0.1", 49152, 49154, time.Second, 2); errorCode(err) != "remote_pairing_cold" {
		t.Fatalf("error = %v, want remote_pairing_cold", err)
	}
}
