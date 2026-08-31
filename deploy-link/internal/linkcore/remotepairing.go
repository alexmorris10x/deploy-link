package linkcore

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"strconv"
	"sync"
	"time"
)

const (
	remotePairingPortStart                = 49152
	remotePairingPortEnd                  = 65535
	remotePairingProbeTimeout             = time.Second
	remotePairingDiscoveryConcurrency     = 256
	remotePairingDiscoveryTimeout         = 75 * time.Second
	remotePairingDiscoverySuccessInterval = 5 * time.Minute
	remotePairingDiscoveryRetryInitial    = 15 * time.Minute
	remotePairingDiscoveryRetryMaximum    = time.Hour
)

var remotePairingMagic = []byte("RPPairing")

var remotePairingProbe = []byte(`{"message":{"plain":{"_0":{"request":{"_0":{"handshake":{"_0":{"hostOptions":{"attemptPairVerify":true},"wireProtocolVersion":19}}}}}}},"originatedBy":"host","sequenceNumber":0}`)

type remotePairingResponse struct {
	Message struct {
		Plain struct {
			Zero struct {
				Response struct {
					One struct {
						Handshake struct {
							Zero struct {
								WireProtocolVersion int `json:"wireProtocolVersion"`
								DeviceOptions       struct {
									AllowsIncomingTunnelConnections bool `json:"allowsIncomingTunnelConnections"`
								} `json:"deviceOptions"`
							} `json:"_0"`
						} `json:"handshake"`
					} `json:"_1"`
				} `json:"response"`
			} `json:"_0"`
		} `json:"plain"`
	} `json:"message"`
}

func remotePairingProbeFrame() []byte {
	frame := make([]byte, len(remotePairingMagic)+2+len(remotePairingProbe))
	copy(frame, remotePairingMagic)
	binary.BigEndian.PutUint16(frame[len(remotePairingMagic):], uint16(len(remotePairingProbe)))
	copy(frame[len(remotePairingMagic)+2:], remotePairingProbe)
	return frame
}

func probeRemotePairing(ctx context.Context, target string, port int, timeout time.Duration) bool {
	dialer := net.Dialer{Timeout: timeout}
	connection, err := dialer.DialContext(ctx, "tcp", net.JoinHostPort(target, strconv.Itoa(port)))
	if err != nil {
		return false
	}
	defer connection.Close()
	deadline := time.Now().Add(timeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	if err := connection.SetDeadline(deadline); err != nil {
		return false
	}
	if _, err := connection.Write(remotePairingProbeFrame()); err != nil {
		return false
	}
	header := make([]byte, len(remotePairingMagic)+2)
	if _, err := io.ReadFull(connection, header); err != nil {
		return false
	}
	if string(header[:len(remotePairingMagic)]) != string(remotePairingMagic) {
		return false
	}
	bodyLength := int(binary.BigEndian.Uint16(header[len(remotePairingMagic):]))
	if bodyLength <= 0 || bodyLength > 1<<20 {
		return false
	}
	body := make([]byte, bodyLength)
	if _, err := io.ReadFull(connection, body); err != nil {
		return false
	}
	var response remotePairingResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return false
	}
	handshake := response.Message.Plain.Zero.Response.One.Handshake.Zero
	return handshake.WireProtocolVersion > 0 && handshake.DeviceOptions.AllowsIncomingTunnelConnections
}

func findRemotePairingPort(
	ctx context.Context,
	target string,
	start int,
	end int,
	timeout time.Duration,
	concurrency int,
) (int, error) {
	if start < 1 || end > 65535 || start > end || concurrency < 1 || timeout <= 0 {
		return 0, coded("remote_pairing_discovery_invalid", "RemotePairing discovery settings are invalid", nil)
	}
	scanCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	ports := make(chan int)
	found := make(chan int, 1)
	workerCount := concurrency
	if available := end - start + 1; workerCount > available {
		workerCount = available
	}
	var workers sync.WaitGroup
	workers.Add(workerCount)
	for range workerCount {
		go func() {
			defer workers.Done()
			for port := range ports {
				if !probeRemotePairing(scanCtx, target, port, timeout) {
					continue
				}
				select {
				case found <- port:
					cancel()
				case <-scanCtx.Done():
				}
				return
			}
		}()
	}
	go func() {
		defer close(ports)
		for port := start; port <= end; port++ {
			select {
			case ports <- port:
			case <-scanCtx.Done():
				return
			}
		}
	}()
	done := make(chan struct{})
	go func() {
		workers.Wait()
		close(done)
	}()
	select {
	case port := <-found:
		cancel()
		<-done
		return port, nil
	case <-done:
		select {
		case port := <-found:
			return port, nil
		default:
		}
		if err := ctx.Err(); err != nil {
			return 0, coded("remote_pairing_cold", "RemotePairing discovery was interrupted", err)
		}
		return 0, coded("remote_pairing_cold", "no usable RemotePairing listener was discovered", nil)
	case <-ctx.Done():
		cancel()
		<-done
		return 0, coded("remote_pairing_cold", "RemotePairing discovery was interrupted", ctx.Err())
	}
}

func discoverRemotePairingPort(ctx context.Context, config Config) (int, error) {
	return findRemotePairingPort(
		ctx,
		config.TargetTailnetIP,
		remotePairingPortStart,
		remotePairingPortEnd,
		remotePairingProbeTimeout,
		remotePairingDiscoveryConcurrency,
	)
}
