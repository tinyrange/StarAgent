package protocol

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func pair(t *testing.T, documentation string) (*Server, *Client, *bytes.Buffer, *bytes.Buffer) {
	t.Helper()
	serverToClient := new(bytes.Buffer)
	clientToServer := new(bytes.Buffer)
	server, err := StartServer(serverToClient, clientToServer, strings.NewReader(documentation))
	if err != nil {
		t.Fatal(err)
	}
	client, err := StartClient(clientToServer, serverToClient)
	if err != nil {
		t.Fatal(err)
	}
	return server, client, serverToClient, clientToServer
}

// oneByteReader forces every header, payload, and terminator to be readable
// across arbitrary Read boundaries.
type oneByteReader struct{ reader io.Reader }

func (r oneByteReader) Read(p []byte) (int, error) {
	if len(p) > 1 {
		p = p[:1]
	}
	return r.reader.Read(p)
}

type shortWriter struct{ buffer bytes.Buffer }

func (w *shortWriter) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	return w.buffer.Write(p[:len(p)-1]) // Violates io.Writer contract on purpose.
}

type limitedWriter struct {
	buffer bytes.Buffer
	limit  int
	err    error
}

func (w *limitedWriter) Write(p []byte) (int, error) {
	remaining := w.limit - w.buffer.Len()
	if remaining <= 0 {
		return 0, w.err
	}
	if remaining < len(p) {
		n, _ := w.buffer.Write(p[:remaining])
		return n, w.err
	}
	return w.buffer.Write(p)
}

type errorReader struct{ err error }

func (r errorReader) Read([]byte) (int, error) { return 0, r.err }

func TestWireConstants(t *testing.T) {
	tests := []struct {
		name string
		got  []byte
		want []byte
	}{
		{"magic", magicBytes, []byte("StarAgt\x00")},
		{"version", versionBytes, []byte{1, 0, 0, 0, 0, 0, 0, 0}},
		{"attention", attentionBytes, []byte("\x02SAgAtn\x03")},
		{"end of message", endOfMessageBytes, []byte("\x02SAgEnd\x03")},
		{"header", headerBytes, []byte("StarAgt\x00\x01\x00\x00\x00\x00\x00\x00\x00")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !bytes.Equal(tt.got, tt.want) {
				t.Fatalf("got %X, want %X", tt.got, tt.want)
			}
		})
	}
}

func TestWriteSignals(t *testing.T) {
	tests := []struct {
		name  string
		write func(io.Writer) error
		want  []byte
	}{
		{"header", WriteHeader, headerBytes},
		{"attention", WriteAttention, attentionBytes},
		{"end of message", WriteEndOfMessage, endOfMessageBytes},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer
			if err := tt.write(&output); err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(output.Bytes(), tt.want) {
				t.Fatalf("got %X, want %X", output.Bytes(), tt.want)
			}

			w := &shortWriter{}
			if err := tt.write(w); !errors.Is(err, io.ErrShortWrite) {
				t.Fatalf("short write: got %v, want io.ErrShortWrite", err)
			}

			wantErr := errors.New("write failed")
			failing := &limitedWriter{limit: 0, err: wantErr}
			if err := tt.write(failing); !errors.Is(err, wantErr) {
				t.Fatalf("write error: got %v, want %v", err, wantErr)
			}
		})
	}
}

func TestStartServerHandshake(t *testing.T) {
	tests := []struct {
		name string
		doc  io.Reader
		want string
	}{
		{"nil documentation", nil, ""},
		{"empty documentation", strings.NewReader(""), ""},
		{"multiline UTF-8", strings.NewReader("help:\n  print('世界')\n"), "help:\n  print('世界')\n"},
		{"large documentation", strings.NewReader(strings.Repeat("abcd\n", 1100)), strings.Repeat("abcd\n", 1100)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var output bytes.Buffer
			server, err := StartServer(&output, bytes.NewReader(nil), tt.doc)
			if err != nil || server == nil {
				t.Fatalf("StartServer: %v", err)
			}
			want := append(append([]byte(nil), headerBytes...), []byte(tt.want)...)
			want = append(want, attentionBytes...)
			if !bytes.Equal(output.Bytes(), want) {
				t.Fatalf("handshake mismatch:\n got %X\nwant %X", output.Bytes(), want)
			}
		})
	}
}

func TestStartClientDocumentation(t *testing.T) {
	for _, size := range []int{0, 1, 7, 8, 15, 16, 511, 512, 513, 1024, 1025, 8193} {
		t.Run(fmt.Sprintf("%d bytes", size), func(t *testing.T) {
			doc := strings.Repeat("x", size)
			wire := append(append(append([]byte{}, headerBytes...), []byte(doc)...), attentionBytes...)
			for _, fragment := range []bool{false, true} {
				var r io.Reader = bytes.NewReader(wire)
				if fragment {
					r = oneByteReader{r}
				}
				client, err := StartClient(io.Discard, r)
				if err != nil {
					t.Fatalf("fragment=%v: %v", fragment, err)
				}
				if string(client.documentation) != doc {
					t.Fatalf("fragment=%v: documentation mismatch", fragment)
				}
			}
		})
	}

	// Unicode, newlines, tabs, and NUL are valid UTF-8; STX and ETX are not.
	doc := "λ世界\n\t\x00done"
	wire := append(append(append([]byte{}, headerBytes...), []byte(doc)...), attentionBytes...)
	client, err := StartClient(io.Discard, oneByteReader{bytes.NewReader(wire)})
	if err != nil || string(client.documentation) != doc {
		t.Fatalf("Unicode documentation: got %q, %v", client.documentation, err)
	}
}

func TestStartClientRejectsBadHeader(t *testing.T) {
	for _, length := range []int{0, 1, 7, 8, 9, 15} {
		t.Run(fmt.Sprintf("truncated at %d", length), func(t *testing.T) {
			_, err := StartClient(io.Discard, bytes.NewReader(headerBytes[:length]))
			if err == nil {
				t.Fatal("accepted truncated header")
			}
		})
	}
	for _, offset := range []int{0, 7, 8, 15} {
		t.Run(fmt.Sprintf("corrupt byte %d", offset), func(t *testing.T) {
			wire := append(append([]byte{}, headerBytes...), attentionBytes...)
			wire[offset] ^= 0xff
			if _, err := StartClient(io.Discard, bytes.NewReader(wire)); err == nil || !strings.Contains(err.Error(), "invalid header") {
				t.Fatalf("got %v, want invalid header", err)
			}
		})
	}
}

func TestStartClientRejectsBadDocumentation(t *testing.T) {
	tests := []struct {
		name string
		body []byte
	}{
		{"missing terminator", []byte("hello")},
		{"wrong terminator", append([]byte("help"), endOfMessageBytes...)},
		{"unexpected ETX", []byte("help\x03")},
		{"unexpected STX", []byte("help\x02abcdefg")},
	}
	for n := 1; n < len(attentionBytes); n++ {
		tests = append(tests, struct {
			name string
			body []byte
		}{fmt.Sprintf("terminator truncated after %d bytes", n), append([]byte("help"), attentionBytes[:n]...)})
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			wire := append(append([]byte{}, headerBytes...), tt.body...)
			if _, err := StartClient(io.Discard, oneByteReader{bytes.NewReader(wire)}); err == nil {
				t.Fatal("accepted malformed documentation frame")
			}
		})
	}
}

func TestStartServerFailures(t *testing.T) {
	writeErr := errors.New("failed writer")
	for _, tt := range []struct {
		name  string
		limit int
		stage string
	}{
		{"header", 0, "header"},
		{"documentation", 17, "documentation"},
		{"attention", 19, "attention"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			w := &limitedWriter{limit: tt.limit, err: writeErr}
			server, err := StartServer(w, bytes.NewReader(nil), strings.NewReader("abc"))
			if server != nil || !errors.Is(err, writeErr) || !strings.Contains(err.Error(), tt.stage) {
				t.Fatalf("got server=%v, err=%v", server, err)
			}
		})
	}

	readErr := errors.New("documentation read failed")
	_, err := StartServer(io.Discard, bytes.NewReader(nil), errorReader{readErr})
	if !errors.Is(err, readErr) {
		t.Fatalf("documentation read error: %v", err)
	}
}

func TestExchangeRoundTrips(t *testing.T) {
	server, client, _, _ := pair(t, "commands: eval\n")
	if string(client.documentation) != "commands: eval\n" {
		t.Fatalf("documentation: %q", client.documentation)
	}

	for i, tt := range []struct {
		request  string
		response string
	}{
		{"", ""},
		{"print(1 + 2)\n", "3\n"},
		{"print('🌍')", "🌍\n"},
		{strings.Repeat("a", 16385), strings.Repeat("b", 16385)},
		{"a\x00b\n\tc", "ok\x00\n"},
	} {
		t.Run(fmt.Sprintf("exchange %d", i), func(t *testing.T) {
			responseReader, err := client.Exchange(context.Background(), tt.request)
			if err != nil {
				t.Fatal(err)
			}
			request, responseWriter, err := server.Exchange(context.Background())
			if err != nil || request != tt.request {
				t.Fatalf("server read %q: %v", request, err)
			}
			// Multiple writes must form a single response frame.
			cut := len(tt.response) / 2
			if _, err := responseWriter.Write([]byte(tt.response[:cut])); err != nil {
				t.Fatal(err)
			}
			if _, err := responseWriter.Write([]byte(tt.response[cut:])); err != nil {
				t.Fatal(err)
			}
			if err := responseWriter.Close(); err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(responseReader)
			if err != nil || string(got) != tt.response {
				t.Fatalf("client read %q, err=%v, want %q", got, err, tt.response)
			}
			if err := responseReader.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestStrictClientSequencing(t *testing.T) {
	server, client, _, requestWire := pair(t, "")
	first, err := client.Exchange(context.Background(), "first")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := client.Exchange(context.Background(), "second"); err == nil {
		t.Fatal("second Exchange succeeded before first response closed")
	}
	if !bytes.Equal(requestWire.Bytes(), append([]byte("first"), endOfMessageBytes...)) {
		t.Fatal("rejected second Exchange wrote to transport")
	}

	_, response, err := server.Exchange(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := response.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := io.ReadAll(first); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Exchange(context.Background(), "second"); err == nil {
		t.Fatal("second Exchange succeeded after EOF but before Close")
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := client.Exchange(context.Background(), "second")
	if err != nil {
		t.Fatal(err)
	}
	_, response, err = server.Exchange(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := response.Close(); err != nil {
		t.Fatal(err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestStrictServerSequencing(t *testing.T) {
	server, client, _, _ := pair(t, "")
	first, err := client.Exchange(context.Background(), "first")
	if err != nil {
		t.Fatal(err)
	}
	_, response, err := server.Exchange(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := server.Exchange(context.Background()); err == nil {
		t.Fatal("server allowed next request before responding")
	}
	if err := response.Close(); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	second, err := client.Exchange(context.Background(), "second")
	if err != nil {
		t.Fatal(err)
	}
	request, response, err := server.Exchange(context.Background())
	if err != nil || request != "second" {
		t.Fatalf("request=%q, err=%v", request, err)
	}
	if err := response.Close(); err != nil {
		t.Fatal(err)
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestClientCloseDrainsUnreadResponse(t *testing.T) {
	server, client, _, _ := pair(t, "")
	responseReader, err := client.Exchange(context.Background(), "one")
	if err != nil {
		t.Fatal(err)
	}
	_, responseWriter, err := server.Exchange(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(responseWriter, strings.Repeat("x", 4096)); err != nil {
		t.Fatal(err)
	}
	if err := responseWriter.Close(); err != nil {
		t.Fatal(err)
	}

	prefix := make([]byte, 3)
	if n, err := responseReader.Read(prefix); err != nil || n != len(prefix) || string(prefix) != "xxx" {
		t.Fatalf("Read got %q (%d bytes), err=%v", prefix[:n], n, err)
	}
	if err := responseReader.Close(); err != nil {
		t.Fatal(err)
	}

	next, err := client.Exchange(context.Background(), "two")
	if err != nil {
		t.Fatal(err)
	}
	request, responseWriter, err := server.Exchange(context.Background())
	if err != nil || request != "two" {
		t.Fatalf("request=%q, err=%v", request, err)
	}
	if _, err := io.WriteString(responseWriter, "next"); err != nil {
		t.Fatal(err)
	}
	if err := responseWriter.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(next)
	if err != nil || string(got) != "next" {
		t.Fatalf("next response=%q, err=%v", got, err)
	}
	if err := next.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestResponseCloseAndPostCloseOperations(t *testing.T) {
	server, client, serverWire, _ := pair(t, "")
	clientReader, err := client.Exchange(context.Background(), "")
	if err != nil {
		t.Fatal(err)
	}
	_, serverWriter, err := server.Exchange(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	before := serverWire.Len()
	if err := serverWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err := serverWriter.Close(); err != nil {
		t.Fatalf("second server Close: %v", err)
	}
	if serverWire.Len() != before+len(attentionBytes) {
		t.Fatalf("Close wrote attention more than once")
	}
	if _, err := serverWriter.Write([]byte("late")); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("Write after Close: %v", err)
	}

	if err := clientReader.Close(); err != nil {
		t.Fatal(err)
	}
	if err := clientReader.Close(); err != nil {
		t.Fatalf("second client Close: %v", err)
	}
	if _, err := clientReader.Read(make([]byte, 1)); !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("Read after Close: %v", err)
	}
}

func TestMalformedRequestFrames(t *testing.T) {
	for _, tt := range []struct {
		name string
		wire []byte
	}{
		{"wrong terminator", append([]byte("code"), attentionBytes...)},
		{"missing terminator", []byte("code")},
		{"partial terminator", append([]byte("code"), endOfMessageBytes[:5]...)},
		{"unexpected ETX", []byte("code\x03")},
		{"unexpected STX", []byte("code\x02zzzzzzz")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			server, err := StartServer(io.Discard, oneByteReader{bytes.NewReader(tt.wire)}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, writer, err := server.Exchange(context.Background()); err == nil || writer != nil {
				t.Fatalf("malformed request: writer=%v, err=%v", writer, err)
			}
		})
	}
}

func TestMalformedResponseFrames(t *testing.T) {
	for _, tt := range []struct {
		name string
		wire []byte
	}{
		{"wrong terminator", append([]byte("result"), endOfMessageBytes...)},
		{"missing terminator", []byte("result")},
		{"partial terminator", append([]byte("result"), attentionBytes[:5]...)},
		{"unexpected ETX", []byte("result\x03")},
		{"unexpected STX", []byte("result\x02zzzzzzz")},
	} {
		t.Run(tt.name, func(t *testing.T) {
			wire := append(append([]byte{}, headerBytes...), attentionBytes...)
			wire = append(wire, tt.wire...)
			client, err := StartClient(io.Discard, oneByteReader{bytes.NewReader(wire)})
			if err != nil {
				t.Fatal(err)
			}
			reader, err := client.Exchange(context.Background(), "request")
			if err != nil {
				t.Fatal(err)
			}
			got, readErr := io.ReadAll(reader)
			if string(got) != "result" || readErr == nil {
				t.Fatalf("ReadAll got %q, err=%v", got, readErr)
			}
			if err := reader.Close(); err == nil {
				t.Fatal("Close hid framing error")
			}
		})
	}
}

func TestFrameReaderSmallReadsAndEOF(t *testing.T) {
	for _, tt := range []struct {
		name    string
		payload string
		marker  []byte
	}{
		{"empty", "", attentionBytes},
		{"one byte", "x", endOfMessageBytes},
		{"multibyte UTF-8", "λ世界🌍", attentionBytes},
	} {
		t.Run(tt.name, func(t *testing.T) {
			wire := append([]byte(tt.payload), tt.marker...)
			reader := &frameReader{reader: bufio.NewReader(oneByteReader{bytes.NewReader(wire)}), terminator: tt.marker}
			if n, err := reader.Read(nil); n != 0 || err != nil {
				t.Fatalf("zero-length Read got n=%d err=%v", n, err)
			}
			var got bytes.Buffer
			for {
				var b [1]byte
				n, err := reader.Read(b[:])
				got.Write(b[:n])
				if err == io.EOF {
					break
				}
				if err != nil {
					t.Fatal(err)
				}
			}
			if got.String() != tt.payload {
				t.Fatalf("got %q, want %q", got.String(), tt.payload)
			}
			if _, err := reader.Read(make([]byte, 1)); err != io.EOF {
				t.Fatalf("Read after EOF: %v", err)
			}
		})
	}
}

func TestFrameReaderReturnsAvailableDataBeforeTerminator(t *testing.T) {
	pr, pw := io.Pipe()
	defer pr.Close()
	defer pw.Close()
	reader := &frameReader{reader: bufio.NewReader(pr), terminator: attentionBytes}

	type result struct {
		data string
		err  error
	}
	results := make(chan result, 1)
	go func() {
		buf := make([]byte, 4096)
		n, err := reader.Read(buf)
		results <- result{string(buf[:n]), err}
	}()
	if _, err := pw.Write([]byte("first chunk")); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-results:
		if got.data != "first chunk" || got.err != nil {
			t.Fatalf("Read returned %+v", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Read blocked waiting for the terminator, rather than streaming")
	}

	markerWrite := make(chan error, 1)
	go func() {
		_, err := pw.Write(attentionBytes)
		markerWrite <- err
	}()
	if rest, err := io.ReadAll(reader); err != nil || len(rest) != 0 {
		t.Fatalf("trailing ReadAll got %q, err=%v", rest, err)
	}
	if err := <-markerWrite; err != nil {
		t.Fatal(err)
	}
}

func TestContextCanceledBeforeExchange(t *testing.T) {
	server, client, _, clientWire := pair(t, "")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	before := clientWire.Len()
	if reader, err := client.Exchange(ctx, "must not send"); !errors.Is(err, context.Canceled) || reader != nil {
		t.Fatalf("client Exchange: reader=%v, err=%v", reader, err)
	}
	if clientWire.Len() != before {
		t.Fatal("client wrote data despite canceled context")
	}
	if request, writer, err := server.Exchange(ctx); !errors.Is(err, context.Canceled) || writer != nil || request != "" {
		t.Fatalf("server Exchange: request=%q, writer=%v, err=%v", request, writer, err)
	}
}

func TestClientWriteFailures(t *testing.T) {
	t.Run("request", func(t *testing.T) {
		writeErr := errors.New("request write failed")
		writer := &limitedWriter{limit: 0, err: writeErr}
		client := &Client{writer: writer, reader: bufio.NewReader(bytes.NewReader(nil))}
		if _, err := client.Exchange(context.Background(), "hi"); !errors.Is(err, writeErr) {
			t.Fatalf("request write: %v", err)
		}
	})
	t.Run("request short write", func(t *testing.T) {
		client := &Client{writer: &shortWriter{}, reader: bufio.NewReader(bytes.NewReader(nil))}
		if _, err := client.Exchange(context.Background(), "hi"); !errors.Is(err, io.ErrShortWrite) {
			t.Fatalf("short request write: %v", err)
		}
	})
	t.Run("end of message", func(t *testing.T) {
		writeErr := errors.New("terminator write failed")
		writer := &limitedWriter{limit: 2, err: writeErr}
		client := &Client{writer: writer, reader: bufio.NewReader(bytes.NewReader(nil))}
		if _, err := client.Exchange(context.Background(), "hi"); !errors.Is(err, writeErr) {
			t.Fatalf("terminator write: %v", err)
		}
	})
}

func TestServerResponseWriteFailures(t *testing.T) {
	writeErr := errors.New("write failed")
	server := &Server{
		reader: bufio.NewReader(bytes.NewReader(endOfMessageBytes)),
		writer: &limitedWriter{limit: 0, err: writeErr},
	}
	_, response, err := server.Exchange(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := response.Write([]byte("result")); !errors.Is(err, writeErr) {
		t.Fatalf("response Write: %v", err)
	}
	if err := response.Close(); !errors.Is(err, writeErr) {
		t.Fatalf("response Close: %v", err)
	}
}

// Exercise the complete protocol over a duplex stream, including the case
// where the server has sent output but has not yet sent ATTENTION.
func TestDuplexPipeStreamsBeforeAttention(t *testing.T) {
	serverConn, clientConn := net.Pipe()
	defer serverConn.Close()
	defer clientConn.Close()
	deadline := time.Now().Add(3 * time.Second)
	if err := serverConn.SetDeadline(deadline); err != nil {
		t.Fatal(err)
	}
	if err := clientConn.SetDeadline(deadline); err != nil {
		t.Fatal(err)
	}

	finishResponse := make(chan struct{})
	serverDone := make(chan error, 1)
	go func() {
		server, err := StartServer(serverConn, serverConn, strings.NewReader("help"))
		if err != nil {
			serverDone <- err
			return
		}
		request, response, err := server.Exchange(context.Background())
		if err != nil {
			serverDone <- err
			return
		}
		if request != "print(42)" {
			serverDone <- fmt.Errorf("unexpected request: %q", request)
			return
		}
		if _, err := io.WriteString(response, "42"); err != nil {
			serverDone <- err
			return
		}
		<-finishResponse
		serverDone <- response.Close()
	}()

	client, err := StartClient(clientConn, clientConn)
	if err != nil {
		t.Fatal(err)
	}
	if string(client.documentation) != "help" {
		t.Fatalf("documentation=%q", client.documentation)
	}
	reader, err := client.Exchange(context.Background(), "print(42)")
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 1024)
	n, err := reader.Read(buf)
	if err != nil || string(buf[:n]) != "42" {
		t.Fatalf("Read before ATTENTION got %q, err=%v", buf[:n], err)
	}
	close(finishResponse)
	if rest, err := io.ReadAll(reader); err != nil || len(rest) != 0 {
		t.Fatalf("final read got %q, err=%v", rest, err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
}

func TestTruncatedFramesReturnUnexpectedEOF(t *testing.T) {
	for n := 0; n < len(attentionBytes); n++ {
		t.Run(fmt.Sprintf("terminator bytes %d", n), func(t *testing.T) {
			wire := append([]byte("body"), attentionBytes[:n]...)
			reader := &frameReader{
				reader:     bufio.NewReader(bytes.NewReader(wire)),
				terminator: attentionBytes,
			}
			body, err := io.ReadAll(reader)
			if string(body) != "body" || !errors.Is(err, io.ErrUnexpectedEOF) {
				t.Fatalf("got body=%q, err=%v", body, err)
			}
		})
	}
}
