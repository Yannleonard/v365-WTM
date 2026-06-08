package guac

import (
	"bufio"
	"net"
	"strings"
	"testing"
	"time"
)

// fakeGuacd plays the guacd side of the handshake on one end of a net.Pipe,
// recording what the proxy sent and replying with scripted instructions.
func TestDial_Handshake(t *testing.T) {
	client, server := net.Pipe()
	defer client.Close()
	defer server.Close()

	gotConnect := make(chan []string, 1)

	go func() {
		br := bufio.NewReader(server)
		// 1) expect select,vnc
		op, elems := mustRead(t, br)
		if op != "select" || len(elems) != 2 || elems[1] != "vnc" {
			t.Errorf("expected select,vnc got %v", elems)
		}
		// 2) reply args (version + param names)
		writeRaw(server, "4.args,13.VERSION_1_5_0,8.hostname,4.port,8.password;")
		// 3) expect size, audio, video, image
		for _, want := range []string{"size", "audio", "video", "image"} {
			op, _ := mustRead(t, br)
			if op != want {
				t.Errorf("expected %s got %s", want, op)
			}
		}
		// 4) expect connect with values in the announced order
		op, elems = mustRead(t, br)
		if op != "connect" {
			t.Errorf("expected connect got %s", op)
		}
		gotConnect <- elems
		// 5) reply ready
		writeRaw(server, "5.ready,8.$abcdef0;")
	}()

	// Drive Dial against the in-memory pipe by injecting the conn.
	c := &Conn{tcp: client, br: bufio.NewReader(client)}
	err := c.handshake(Params{
		Protocol: "vnc", Hostname: "10.0.0.5", Port: 5900, Password: "secret",
		Width: 1280, Height: 800, DPI: 96,
	})
	if err != nil {
		t.Fatalf("handshake error: %v", err)
	}

	select {
	case elems := <-gotConnect:
		// elems[0]=="connect"; then EXACTLY one value per `args` element after the
		// opcode: the version slot (echoed) + one per param (hostname, port, password).
		// args was: args,VERSION_1_5_0,hostname,port,password -> 4 connect values.
		if len(elems) != 5 {
			t.Fatalf("connect elements = %v (want opcode + 4 values)", elems)
		}
		if elems[1] != "VERSION_1_5_0" {
			t.Errorf("version slot not echoed: %v", elems)
		}
		if elems[2] != "10.0.0.5" || elems[3] != "5900" || elems[4] != "secret" {
			t.Errorf("connect values wrong: %v", elems)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("did not receive connect")
	}
}

// TestWriteElem verifies the "<runeLen>.<value>" encoding incl. multibyte runes.
func TestWriteElem(t *testing.T) {
	var sb strings.Builder
	writeElem(&sb, "vnc")
	if sb.String() != "3.vnc" {
		t.Errorf("got %q want 3.vnc", sb.String())
	}
	sb.Reset()
	writeElem(&sb, "café") // 4 runes (é is one rune), not 5 bytes
	if sb.String() != "4.café" {
		t.Errorf("got %q want 4.café", sb.String())
	}
}

func mustRead(t *testing.T, br *bufio.Reader) (string, []string) {
	t.Helper()
	c := &Conn{br: br}
	op, elems, err := c.readInstr()
	if err != nil {
		t.Fatalf("readInstr: %v", err)
	}
	return op, elems
}

func writeRaw(w net.Conn, s string) { _, _ = w.Write([]byte(s)) }
