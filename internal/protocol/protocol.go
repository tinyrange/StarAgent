package protocol

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
)

// Protocol for service communications.
//
// All constants are serialized little-endian. The server sends MAGIC, VERSION,
// documentation (possibly empty), and ATTENTION. Thereafter the client sends a
// UTF-8 request followed by ENDOFMSG, and the server sends a UTF-8 response
// followed by ATTENTION. Requests and responses must strictly alternate.
//
// Payload text must not contain the bytes 0x02 (STX) or 0x03 (ETX). The protocol
// is not binary-safe. Other details are implementation-defined for v1.
const (
	MAGIC     uint64 = 0x0074674172617453 // StarAgt \0
	VERSION   uint64 = 0x0000000000000001
	ATTENTION uint64 = 0x036e744167415302 // \STX SAgAtn \ETX
	ENDOFMSG  uint64 = 0x03646e4567415302 // \STX SAgEnd \ETX
)

var (
	magicBytes        = binary.LittleEndian.AppendUint64(nil, MAGIC)
	versionBytes      = binary.LittleEndian.AppendUint64(nil, VERSION)
	attentionBytes    = binary.LittleEndian.AppendUint64(nil, ATTENTION)
	endOfMessageBytes = binary.LittleEndian.AppendUint64(nil, ENDOFMSG)
	headerBytes       = bytes.Join([][]byte{magicBytes, versionBytes}, nil)
)

func writeSignal(w io.Writer, signal []byte) error {
	n, err := w.Write(signal)
	if err != nil {
		return err
	}
	if n != len(signal) {
		return io.ErrShortWrite
	}
	return nil
}

func WriteHeader(w io.Writer) error       { return writeSignal(w, headerBytes) }
func WriteAttention(w io.Writer) error    { return writeSignal(w, attentionBytes) }
func WriteEndOfMessage(w io.Writer) error { return writeSignal(w, endOfMessageBytes) }

// frameReader streams a text payload, stopping at its eight-byte terminator.
// STX cannot occur in payloads, so it unambiguously starts the terminator.
type frameReader struct {
	reader     *bufio.Reader
	terminator []byte
	err        error
}

func (r *frameReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if r.err != nil {
		return 0, r.err
	}

	for n := 0; ; {
		// Don't wait to fill p once all currently buffered payload bytes
		// have been returned. Responses can arrive incrementally.
		if n > 0 && r.reader.Buffered() == 0 {
			return n, nil
		}

		b, err := r.reader.ReadByte()
		if err != nil {
			if err == io.EOF {
				err = io.ErrUnexpectedEOF // A frame must end with its terminator.
			}
			r.err = err
			return n, err
		}

		switch b {
		case 0x02:
			var signal [8]byte
			signal[0] = b
			if _, err := io.ReadFull(r.reader, signal[1:]); err != nil {
				if err == io.EOF {
					err = io.ErrUnexpectedEOF
				}
				r.err = fmt.Errorf("incomplete terminator: %w", err)
				return n, r.err
			}
			if !bytes.Equal(signal[:], r.terminator) {
				r.err = fmt.Errorf("unexpected terminator: %X", signal)
				return n, r.err
			}
			r.err = io.EOF
			return n, io.EOF
		case 0x03:
			r.err = fmt.Errorf("unexpected ETX in payload")
			return n, r.err
		default:
			p[n] = b
			n++
			if n == len(p) {
				return n, nil
			}
		}
	}
}

func readFrame(reader *bufio.Reader, terminator []byte) ([]byte, error) {
	return io.ReadAll(&frameReader{reader: reader, terminator: terminator})
}

type Server struct {
	writer     io.Writer
	reader     *bufio.Reader
	responding bool
}

// Exchange reads one request and returns a response writer. Closing the writer
// sends ATTENTION and allows the next request to be read.
func (s *Server) Exchange(ctx context.Context) (string, io.WriteCloser, error) {
	if s.responding {
		return "", nil, fmt.Errorf("previous response not closed")
	}
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
	// EOF between requests is a clean disconnect; EOF inside a frame is not.
	if _, err := s.reader.Peek(1); err != nil {
		return "", nil, fmt.Errorf("failed to read request: %w", err)
	}

	data, err := readFrame(s.reader, endOfMessageBytes)
	if err != nil {
		return "", nil, fmt.Errorf("failed to read request: %w", err)
	}
	s.responding = true
	return string(data), &serverResponse{server: s}, nil
}

type serverResponse struct {
	server *Server
	closed bool
}

func (r *serverResponse) Write(p []byte) (int, error) {
	if r.closed {
		return 0, io.ErrClosedPipe
	}
	return r.server.writer.Write(p)
}

func (r *serverResponse) Close() error {
	if r.closed {
		return nil
	}
	r.closed = true
	r.server.responding = false
	return WriteAttention(r.server.writer)
}

func StartServer(writer io.Writer, reader io.Reader, documentation io.Reader) (*Server, error) {
	if err := WriteHeader(writer); err != nil {
		return nil, fmt.Errorf("failed to write header: %w", err)
	}
	if documentation != nil {
		if _, err := io.Copy(writer, documentation); err != nil {
			return nil, fmt.Errorf("failed to write documentation: %w", err)
		}
	}
	if err := WriteAttention(writer); err != nil {
		return nil, fmt.Errorf("failed to write attention: %w", err)
	}
	return &Server{writer: writer, reader: bufio.NewReader(reader)}, nil
}

type Client struct {
	writer        io.Writer
	reader        *bufio.Reader
	documentation []byte
	reading       bool
}

func (c *Client) Documentation() string { return string(c.documentation) }

// Exchange writes one request and returns a streaming response reader. The
// response reader must be closed before another Exchange; Close consumes any
// remaining response text through ATTENTION so the next exchange is aligned.
func (c *Client) Exchange(ctx context.Context, data string) (io.ReadCloser, error) {
	if c.reading {
		return nil, fmt.Errorf("previous response not closed")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	if n, err := io.WriteString(c.writer, data); err != nil {
		return nil, fmt.Errorf("failed to write string data: %w", err)
	} else if n != len(data) {
		return nil, fmt.Errorf("failed to write string data: %w", io.ErrShortWrite)
	}
	if err := WriteEndOfMessage(c.writer); err != nil {
		return nil, fmt.Errorf("failed to write end of message: %w", err)
	}

	c.reading = true
	return &clientResponse{
		client:      c,
		frameReader: frameReader{reader: c.reader, terminator: attentionBytes},
	}, nil
}

type clientResponse struct {
	client *Client
	frameReader
	closed bool
}

func (r *clientResponse) Read(p []byte) (int, error) {
	if r.closed {
		return 0, io.ErrClosedPipe
	}
	return r.frameReader.Read(p)
}

func (r *clientResponse) Close() error {
	if r.closed {
		return nil
	}
	_, err := io.Copy(io.Discard, &r.frameReader)
	r.closed = true
	r.client.reading = false
	return err
}

func StartClient(writer io.Writer, reader io.Reader) (*Client, error) {
	var header [16]byte
	if _, err := io.ReadFull(reader, header[:]); err != nil {
		return nil, fmt.Errorf("failed to read header: %w", err)
	}
	if !bytes.Equal(header[:], headerBytes) {
		return nil, fmt.Errorf("invalid header: expected %X got %X", headerBytes, header[:])
	}

	rd := bufio.NewReader(reader)
	documentation, err := readFrame(rd, attentionBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to read documentation: %w", err)
	}

	return &Client{writer: writer, reader: rd, documentation: documentation}, nil
}
