package linkcore

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func TestPowerAssertionIsACOnlyAndTracksSupervisor(t *testing.T) {
	arguments := powerAssertionArguments(4321)
	if want := []string{"-s", "-w", "4321"}; !reflect.DeepEqual(arguments, want) {
		t.Fatalf("arguments = %#v, want %#v", arguments, want)
	}
}

func TestProfileDaemonAvailableFindsLiveProfileSocket(t *testing.T) {
	daemon := testDaemon(t)
	daemon.openSession = func(context.Context, Config) (sessionHandle, error) {
		return nil, coded("remote_pairing_cold", "listener closed", nil)
	}
	daemon.discoverPort = func(context.Context, Config) (int, error) {
		return 0, coded("remote_pairing_cold", "listener closed", nil)
	}
	ctx, cancel := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- daemon.Serve(ctx) }()
	deadline := time.Now().Add(time.Second)
	for !profileDaemonAvailable(context.Background(), daemon.profilePath) {
		if time.Now().After(deadline) {
			cancel()
			<-served
			t.Fatal("profile daemon did not become available")
		}
		time.Sleep(5 * time.Millisecond)
	}
	cancel()
	if err := <-served; err != nil {
		t.Fatal(err)
	}
	if profileDaemonAvailable(context.Background(), daemon.profilePath) {
		t.Fatal("stopped profile daemon still appears available")
	}
}
