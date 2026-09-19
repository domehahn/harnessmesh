// Package knowledge implements the durable, append-only collaboration archive.
package knowledge

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/klauspost/compress/zstd"
)

var archiveMagic = [8]byte{'H', 'M', 'K', 'B', 'Z', 'S', 'T', '1'}

const (
	defaultBlockBytes = 256 * 1024
	maxFrameBytes     = 512 * 1024 * 1024
)

// Record is the stable interchange format used by the knowledge archive.
// Payload is deliberately retained so future indexers can derive richer fields.
type Record struct {
	SchemaVersion int             `json:"schema_version"`
	ID            string          `json:"id"`
	Timestamp     time.Time       `json:"timestamp"`
	SessionID     string          `json:"session_id,omitempty"`
	SpaceID       string          `json:"space_id,omitempty"`
	Kind          string          `json:"kind"`
	From          string          `json:"from,omitempty"`
	To            string          `json:"to,omitempty"`
	ChannelID     string          `json:"channel_id,omitempty"`
	ThreadID      string          `json:"thread_id,omitempty"`
	Text          string          `json:"text,omitempty"`
	Payload       json.RawMessage `json:"payload,omitempty"`
}

type SearchOptions struct {
	SessionID string
	SpaceID   string
	Kind      string
	Limit     int
	Offset    int
}

type Stats struct {
	Path        string `json:"path"`
	Bytes       int64  `json:"bytes"`
	Blocks      int64  `json:"blocks"`
	Compression string `json:"compression"`
}

type Archive struct {
	mu         sync.Mutex
	path       string
	file       *os.File
	encoder    *zstd.Encoder
	pending    bytes.Buffer
	blockBytes int
}

func Open(path string) (*Archive, error) {
	if path == "" {
		return nil, errors.New("knowledge archive path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, fmt.Errorf("create knowledge archive directory: %w", err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0600)
	if err != nil {
		return nil, fmt.Errorf("open knowledge archive: %w", err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return nil, err
	}
	if info.Size() == 0 {
		if _, err := f.Write(archiveMagic[:]); err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("write knowledge archive header: %w", err)
		}
	} else if info.Size() < int64(len(archiveMagic)) {
		_ = f.Close()
		return nil, errors.New("knowledge archive is truncated before header")
	} else {
		header := make([]byte, len(archiveMagic))
		if _, err := f.ReadAt(header, 0); err != nil || !bytes.Equal(header, archiveMagic[:]) {
			_ = f.Close()
			return nil, errors.New("knowledge archive has an invalid header")
		}
	}
	enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
	if err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("create zstd encoder: %w", err)
	}
	return &Archive{path: path, file: f, encoder: enc, blockBytes: defaultBlockBytes}, nil
}

func (a *Archive) Path() string { return a.path }

func (a *Archive) Append(ctx context.Context, record Record) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if record.SchemaVersion == 0 {
		record.SchemaVersion = 1
	}
	if record.Timestamp.IsZero() {
		record.Timestamp = time.Now().UTC()
	}
	line, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("marshal knowledge record: %w", err)
	}
	line = append(line, '\n')

	a.mu.Lock()
	defer a.mu.Unlock()
	if _, err := a.pending.Write(line); err != nil {
		return err
	}
	if a.pending.Len() >= a.blockBytes {
		return a.flushLocked()
	}
	return nil
}

func (a *Archive) flushLocked() error {
	if a.pending.Len() == 0 {
		return nil
	}
	raw := append([]byte(nil), a.pending.Bytes()...)
	compressed := a.encoder.EncodeAll(raw, nil)
	var frame [16]byte
	binary.BigEndian.PutUint64(frame[0:8], uint64(len(raw)))
	binary.BigEndian.PutUint64(frame[8:16], uint64(len(compressed)))
	if _, err := a.file.Write(frame[:]); err != nil {
		return err
	}
	if _, err := a.file.Write(compressed); err != nil {
		return err
	}
	if err := a.file.Sync(); err != nil {
		return err
	}
	a.pending.Reset()
	return nil
}

func (a *Archive) Flush() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.flushLocked()
}

func (a *Archive) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.file == nil {
		return nil
	}
	flushErr := a.flushLocked()
	a.encoder.Close()
	closeErr := a.file.Close()
	a.file = nil
	if flushErr != nil {
		return flushErr
	}
	return closeErr
}

func (a *Archive) Search(ctx context.Context, query string, opts SearchOptions) ([]Record, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.flushLocked(); err != nil {
		return nil, err
	}
	f, err := os.Open(a.path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	reader := bufio.NewReaderSize(f, 64*1024)
	header := make([]byte, len(archiveMagic))
	if _, err := io.ReadFull(reader, header); err != nil {
		return nil, err
	}
	if !bytes.Equal(header, archiveMagic[:]) {
		return nil, errors.New("invalid knowledge archive header")
	}

	if opts.Limit <= 0 || opts.Limit > 1000 {
		opts.Limit = 100
	}
	needle := strings.ToLower(strings.TrimSpace(query))
	terms := strings.Fields(needle)
	seen, skipped := 0, 0
	var out []Record
	decoder, err := zstd.NewReader(nil)
	if err != nil {
		return nil, err
	}
	defer decoder.Close()
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var frame [16]byte
		if _, err := io.ReadFull(reader, frame[:]); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			if errors.Is(err, io.ErrUnexpectedEOF) {
				return nil, errors.New("knowledge archive ends in a partial frame")
			}
			return nil, err
		}
		rawLen := binary.BigEndian.Uint64(frame[0:8])
		compressedLen := binary.BigEndian.Uint64(frame[8:16])
		if rawLen == 0 || compressedLen == 0 || rawLen > maxFrameBytes || compressedLen > maxFrameBytes {
			return nil, errors.New("knowledge archive contains an invalid frame size")
		}
		compressed := make([]byte, compressedLen)
		if _, err := io.ReadFull(reader, compressed); err != nil {
			return nil, err
		}
		raw, err := decoder.DecodeAll(compressed, nil)
		if err != nil {
			return nil, fmt.Errorf("decode knowledge archive frame: %w", err)
		}
		if uint64(len(raw)) != rawLen {
			return nil, errors.New("knowledge archive frame length mismatch")
		}
		scanner := bufio.NewScanner(bytes.NewReader(raw))
		scanner.Buffer(make([]byte, 64*1024), maxFrameBytes)
		for scanner.Scan() {
			var record Record
			if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
				return nil, fmt.Errorf("decode knowledge record: %w", err)
			}
			if opts.SessionID != "" && record.SessionID != opts.SessionID {
				continue
			}
			if opts.SpaceID != "" && record.SpaceID != opts.SpaceID {
				continue
			}
			if opts.Kind != "" && record.Kind != opts.Kind {
				continue
			}
			if needle != "" {
				haystack := strings.ToLower(record.Kind + " " + record.From + " " + record.To + " " + record.ChannelID + " " + record.ThreadID + " " + record.Text + " " + string(record.Payload))
				matched := true
				for _, term := range terms {
					if !strings.Contains(haystack, term) {
						matched = false
						break
					}
				}
				if !matched {
					continue
				}
			}
			if skipped < opts.Offset {
				skipped++
				continue
			}
			seen++
			out = append(out, record)
			if seen >= opts.Limit {
				return out, nil
			}
		}
		if err := scanner.Err(); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (a *Archive) Stats(ctx context.Context) (Stats, error) {
	if err := ctx.Err(); err != nil {
		return Stats{}, err
	}
	if err := a.Flush(); err != nil {
		return Stats{}, err
	}
	info, err := os.Stat(a.path)
	if err != nil {
		return Stats{}, err
	}
	return Stats{Path: a.path, Bytes: info.Size(), Compression: "zstd"}, nil
}

func BuildContext(records []Record, maxChars int) string {
	var b strings.Builder
	for _, r := range records {
		line := fmt.Sprintf("[%s] %s (%s -> %s): %s\n", r.Timestamp.UTC().Format(time.RFC3339), r.Kind, r.From, r.To, r.Text)
		if maxChars > 0 && b.Len()+len(line) > maxChars {
			break
		}
		b.WriteString(line)
	}
	return b.String()
}
