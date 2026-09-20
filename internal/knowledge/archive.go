// Package knowledge implements the durable, append-only collaboration archive.
package knowledge

import (
	"bufio"
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/domehahn/harnessmesh/internal/contextpack"
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
	ProjectID     string          `json:"project_id,omitempty"`
	Repository    string          `json:"repository,omitempty"`
	Branch        string          `json:"branch,omitempty"`
	Commit        string          `json:"commit,omitempty"`
	Agent         string          `json:"agent,omitempty"`
	Model         string          `json:"model,omitempty"`
	Source        string          `json:"source,omitempty"`
	Tags          []string        `json:"tags,omitempty"`
	Confidence    float64         `json:"confidence,omitempty"`
	Verified      bool            `json:"verified,omitempty"`
	Sensitive     bool            `json:"sensitive,omitempty"`
	Score         float64         `json:"score,omitempty"`
	ContentHash   string          `json:"content_hash,omitempty"`
	Supersedes    string          `json:"supersedes,omitempty"`
	ExpiresAt     *time.Time      `json:"expires_at,omitempty"`
}

type SearchOptions struct {
	SessionID      string
	SpaceID        string
	Kind           string
	ProjectID      string
	Agent          string
	Source         string
	Since          time.Time
	Until          time.Time
	VerifiedOnly   bool
	MinConfidence  float64
	IncludeExpired bool
	Limit          int
	Offset         int
}

type Stats struct {
	Path        string `json:"path"`
	Bytes       int64  `json:"bytes"`
	Blocks      int64  `json:"blocks"`
	Compression string `json:"compression"`
	Records     int64  `json:"records"`
	Encrypted   bool   `json:"encrypted"`
	Indexed     bool   `json:"indexed"`
	IndexPath   string `json:"index_path,omitempty"`
}

type QualityReport struct {
	Records           int     `json:"records"`
	Verified          int     `json:"verified"`
	Unverified        int     `json:"unverified"`
	Expired           int     `json:"expired"`
	LowConfidence     int     `json:"low_confidence"`
	Duplicates        int     `json:"duplicates"`
	Conflicts         int     `json:"conflicts"`
	SourceCount       int     `json:"source_count"`
	Stale             int     `json:"stale"`
	AverageConfidence float64 `json:"average_confidence"`
}

type Archive struct {
	mu         sync.Mutex
	path       string
	file       *os.File
	encoder    *zstd.Encoder
	pending    bytes.Buffer
	blockBytes int
	projectID  string
	repository string
	branch     string
	commit     string
	agent      string
	model      string
	aead       cipher.AEAD
	records    int64
	indexPath  string
	indexFile  *os.File
}

type blockIndexEntry struct {
	Offset int64    `json:"offset"`
	End    int64    `json:"end"`
	Terms  []string `json:"terms,omitempty"`
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
	a := &Archive{
		path: path, file: f, encoder: enc, blockBytes: defaultBlockBytes,
		projectID:  os.Getenv("HARNESSMESH_PROJECT_ID"),
		repository: os.Getenv("HARNESSMESH_REPOSITORY"),
		branch:     os.Getenv("HARNESSMESH_BRANCH"),
		commit:     os.Getenv("HARNESSMESH_COMMIT"),
		agent:      os.Getenv("HARNESSMESH_AGENT"),
		model:      os.Getenv("HARNESSMESH_MODEL"),
	}
	if a.projectID == "" {
		a.projectID = filepath.Base(filepath.Dir(path))
	}
	if rawKey := os.Getenv("HARNESSMESH_KNOWLEDGE_KEY"); rawKey != "" {
		a.aead, err = encryptionForKey(rawKey)
		if err != nil {
			_ = f.Close()
			return nil, err
		}
	}
	if a.aead == nil {
		a.indexPath = path + ".idx"
		a.indexFile, err = os.OpenFile(a.indexPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		if err != nil {
			_ = f.Close()
			enc.Close()
			return nil, fmt.Errorf("open knowledge index: %w", err)
		}
	}
	return a, nil
}

func encryptionForKey(rawKey string) (cipher.AEAD, error) {
	hash := sha256.Sum256([]byte(rawKey))
	block, err := aes.NewCipher(hash[:])
	if err != nil {
		return nil, fmt.Errorf("create knowledge encryption cipher: %w", err)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("create knowledge encryption: %w", err)
	}
	return aead, nil
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
	if record.ProjectID == "" {
		record.ProjectID = a.projectID
	}
	if record.Repository == "" {
		record.Repository = a.repository
	}
	if record.Branch == "" {
		record.Branch = a.branch
	}
	if record.Commit == "" {
		record.Commit = a.commit
	}
	if record.Agent == "" {
		record.Agent = a.agent
	}
	if record.Model == "" {
		record.Model = a.model
	}
	if record.ContentHash == "" {
		hash := sha256.Sum256([]byte(strings.TrimSpace(record.Kind) + "\x00" + strings.TrimSpace(record.Text) + "\x00" + string(record.Payload)))
		record.ContentHash = fmt.Sprintf("%x", hash[:])
	}
	redactedText := contextpack.RedactSecrets(record.Text)
	if redactedText != record.Text {
		record.Sensitive = true
		record.Text = redactedText
	}
	if len(record.Payload) > 0 {
		redactedPayload := contextpack.RedactSecrets(string(record.Payload))
		if redactedPayload != string(record.Payload) {
			record.Sensitive = true
			record.Payload = json.RawMessage(redactedPayload)
		}
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
	if a.aead != nil {
		nonce := make([]byte, a.aead.NonceSize())
		if _, err := rand.Read(nonce); err != nil {
			return err
		}
		compressed = append(nonce, a.aead.Seal(nil, nonce, compressed, nil)...)
	}
	var frame [16]byte
	binary.BigEndian.PutUint64(frame[0:8], uint64(len(raw)))
	binary.BigEndian.PutUint64(frame[8:16], uint64(len(compressed)))
	offset, err := a.file.Seek(0, io.SeekEnd)
	if err != nil {
		return err
	}
	if _, err := a.file.Write(frame[:]); err != nil {
		return err
	}
	if _, err := a.file.Write(compressed); err != nil {
		return err
	}
	if err := a.file.Sync(); err != nil {
		return err
	}
	if a.indexFile != nil {
		entry := blockIndexEntry{Offset: offset, End: offset + int64(len(frame)) + int64(len(compressed)), Terms: indexTerms(raw)}
		if encoded, err := json.Marshal(entry); err == nil {
			_, _ = a.indexFile.Write(append(encoded, '\n'))
			_ = a.indexFile.Sync()
		}
	}
	a.pending.Reset()
	a.records += int64(bytes.Count(raw, []byte{'\n'}))
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
	if a.indexFile != nil {
		_ = a.indexFile.Close()
		a.indexFile = nil
	}
	closeErr := a.file.Close()
	a.file = nil
	if flushErr != nil {
		return flushErr
	}
	return closeErr
}

func indexTerms(raw []byte) []string {
	seen := make(map[string]struct{})
	for _, token := range strings.Fields(strings.ToLower(string(raw))) {
		token = strings.Trim(token, "{}[](),.:;\"'")
		if len(token) >= 3 && len(token) <= 96 {
			seen[token] = struct{}{}
		}
	}
	terms := make([]string, 0, len(seen))
	for term := range seen {
		terms = append(terms, term)
	}
	sort.Strings(terms)
	return terms
}

func (a *Archive) indexedOffsets(terms []string) (map[int64]struct{}, bool) {
	if a.indexPath == "" || len(terms) == 0 {
		return nil, false
	}
	info, err := os.Stat(a.path)
	if err != nil {
		return nil, false
	}
	f, err := os.Open(a.indexPath)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	selected := make(map[int64]struct{})
	scanner := bufio.NewScanner(f)
	var lastEnd int64
	for scanner.Scan() {
		var entry blockIndexEntry
		if json.Unmarshal(scanner.Bytes(), &entry) != nil {
			return nil, false
		}
		lastEnd = entry.End
		termSet := make(map[string]struct{}, len(entry.Terms))
		for _, term := range entry.Terms {
			termSet[term] = struct{}{}
		}
		matched := true
		for _, term := range terms {
			if _, ok := termSet[term]; !ok {
				matched = false
				break
			}
		}
		if matched {
			selected[entry.Offset] = struct{}{}
		}
	}
	if scanner.Err() != nil || lastEnd != info.Size() {
		return nil, false
	}
	if len(selected) == 0 {
		return nil, false
	}
	return selected, true
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
	indexed, hasIndex := a.indexedOffsets(terms)
	var out []Record
	maxCandidates := opts.Offset + opts.Limit
	if maxCandidates < 1000 || maxCandidates > 10000 {
		maxCandidates = 10000
	}
	decoder, err := zstd.NewReader(nil)
	if err != nil {
		return nil, err
	}
	defer decoder.Close()
	frameOffset := int64(len(archiveMagic))
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
		frameEnd := frameOffset + int64(len(frame)) + int64(compressedLen)
		if hasIndex {
			if _, ok := indexed[frameOffset]; !ok {
				frameOffset = frameEnd
				continue
			}
		}
		frameOffset = frameEnd
		if a.aead != nil {
			nonceSize := a.aead.NonceSize()
			if len(compressed) <= nonceSize {
				return nil, errors.New("encrypted knowledge frame is truncated")
			}
			decrypted, err := a.aead.Open(nil, compressed[:nonceSize], compressed[nonceSize:], nil)
			if err != nil {
				return nil, fmt.Errorf("decrypt knowledge archive frame: %w", err)
			}
			compressed = decrypted
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
			if opts.ProjectID != "" && record.ProjectID != opts.ProjectID {
				continue
			}
			if opts.Agent != "" && !strings.EqualFold(record.Agent, opts.Agent) {
				continue
			}
			if opts.Source != "" && !strings.EqualFold(record.Source, opts.Source) {
				continue
			}
			if !opts.Since.IsZero() && record.Timestamp.Before(opts.Since) {
				continue
			}
			if !opts.Until.IsZero() && record.Timestamp.After(opts.Until) {
				continue
			}
			if opts.VerifiedOnly && !record.Verified {
				continue
			}
			if record.Confidence < opts.MinConfidence {
				continue
			}
			if !opts.IncludeExpired && record.ExpiresAt != nil && record.ExpiresAt.Before(time.Now().UTC()) {
				continue
			}
			if needle != "" {
				haystack := strings.ToLower(record.Kind + " " + record.From + " " + record.To + " " + record.ChannelID + " " + record.ThreadID + " " + record.Text + " " + string(record.Payload))
				matched := true
				score := 0.0
				for _, term := range terms {
					if !strings.Contains(haystack, term) {
						matched = false
						break
					}
					score += float64(strings.Count(haystack, term))
				}
				if !matched {
					continue
				}
				record.Score = score + vectorSimilarity(needle, haystack)
			}
			if len(out) >= maxCandidates {
				continue
			}
			out = append(out, record)
		}
		if err := scanner.Err(); err != nil {
			return nil, err
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Score != out[j].Score {
			return out[i].Score > out[j].Score
		}
		return out[i].Timestamp.After(out[j].Timestamp)
	})
	start := opts.Offset
	if start > len(out) {
		start = len(out)
	}
	end := start + opts.Limit
	if end > len(out) {
		end = len(out)
	}
	return out[start:end], nil
}

// vectorSimilarity is a deterministic local vector fallback. It keeps search
// provider-independent while giving repeated/co-occurring terms a cosine score;
// deployments can layer provider embeddings without changing the archive format.
func vectorSimilarity(query, text string) float64 {
	const dimensions = 64
	vector := func(value string) [dimensions]float64 {
		var result [dimensions]float64
		for _, term := range strings.Fields(value) {
			h := fnv.New32a()
			_, _ = h.Write([]byte(term))
			idx := h.Sum32() % dimensions
			result[idx]++
		}
		return result
	}
	left, right := vector(query), vector(text)
	var dot, leftNorm, rightNorm float64
	for i := range left {
		dot += left[i] * right[i]
		leftNorm += left[i] * left[i]
		rightNorm += right[i] * right[i]
	}
	if leftNorm == 0 || rightNorm == 0 {
		return 0
	}
	return dot / (leftNorm * rightNorm) // bounded and intentionally small relative to term score
}

// CompactBefore rewrites the archive and removes records older than before.
// The replacement is atomic on filesystems supporting atomic rename.
func (a *Archive) CompactBefore(ctx context.Context, before time.Time) (int64, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.flushLocked(); err != nil {
		return 0, err
	}
	tmp := a.path + ".compact"
	_ = os.Remove(tmp)
	out, err := Open(tmp)
	if err != nil {
		return 0, err
	}
	var kept int64
	err = a.forEachRecordLocked(ctx, func(record Record) error {
		if !before.IsZero() && record.Timestamp.Before(before) {
			return nil
		}
		if err := out.Append(ctx, record); err != nil {
			return err
		}
		kept++
		return nil
	})
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return 0, err
	}
	if err := a.file.Close(); err != nil {
		_ = os.Remove(tmp)
		return 0, err
	}
	if a.indexFile != nil {
		_ = a.indexFile.Close()
		a.indexFile = nil
	}
	a.encoder.Close()
	if err := os.Rename(tmp, a.path); err != nil {
		return 0, err
	}
	if a.indexPath != "" {
		_ = os.Remove(a.indexPath)
		_ = os.Rename(tmp+".idx", a.indexPath)
	}
	reopened, err := Open(a.path)
	if err != nil {
		return 0, err
	}
	a.file, a.encoder, a.aead, a.indexFile = reopened.file, reopened.encoder, reopened.aead, reopened.indexFile
	a.indexPath = reopened.indexPath
	a.projectID, a.records = reopened.projectID, kept
	a.repository, a.branch, a.commit, a.agent, a.model = reopened.repository, reopened.branch, reopened.commit, reopened.agent, reopened.model
	return kept, nil
}

func (a *Archive) forEachRecordLocked(ctx context.Context, fn func(Record) error) error {
	f, err := os.Open(a.path)
	if err != nil {
		return err
	}
	defer f.Close()
	reader := bufio.NewReaderSize(f, 64*1024)
	header := make([]byte, len(archiveMagic))
	if _, err := io.ReadFull(reader, header); err != nil {
		return err
	}
	decoder, err := zstd.NewReader(nil)
	if err != nil {
		return err
	}
	defer decoder.Close()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var frame [16]byte
		if _, err := io.ReadFull(reader, frame[:]); err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
		compressedLen := binary.BigEndian.Uint64(frame[8:16])
		if compressedLen == 0 || compressedLen > maxFrameBytes {
			return errors.New("knowledge archive contains an invalid frame size")
		}
		compressed := make([]byte, compressedLen)
		if _, err := io.ReadFull(reader, compressed); err != nil {
			return err
		}
		if a.aead != nil {
			nonceSize := a.aead.NonceSize()
			if len(compressed) <= nonceSize {
				return errors.New("encrypted knowledge frame is truncated")
			}
			compressed, err = a.aead.Open(nil, compressed[:nonceSize], compressed[nonceSize:], nil)
			if err != nil {
				return err
			}
		}
		raw, err := decoder.DecodeAll(compressed, nil)
		if err != nil {
			return err
		}
		scanner := bufio.NewScanner(bytes.NewReader(raw))
		scanner.Buffer(make([]byte, 64*1024), maxFrameBytes)
		for scanner.Scan() {
			var record Record
			if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
				return err
			}
			if err := fn(record); err != nil {
				return err
			}
		}
		if err := scanner.Err(); err != nil {
			return err
		}
	}
}

func (a *Archive) Stats(ctx context.Context) (Stats, error) {
	if err := ctx.Err(); err != nil {
		return Stats{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.flushLocked(); err != nil {
		return Stats{}, err
	}
	info, err := os.Stat(a.path)
	if err != nil {
		return Stats{}, err
	}
	blocks, err := a.countBlocksLocked()
	if err != nil {
		return Stats{}, err
	}
	var records int64
	if err := a.forEachRecordLocked(ctx, func(Record) error { records++; return nil }); err != nil {
		return Stats{}, err
	}
	a.records = records
	return Stats{Path: a.path, Bytes: info.Size(), Blocks: blocks, Compression: "zstd", Encrypted: a.aead != nil, Indexed: a.indexFile != nil, IndexPath: a.indexPath, Records: records}, nil
}

// Verify scans every frame and record without materializing the archive.
// It is suitable for scheduled integrity checks and backup validation.
func (a *Archive) Verify(ctx context.Context) (int64, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.flushLocked(); err != nil {
		return 0, err
	}
	var records int64
	if err := a.forEachRecordLocked(ctx, func(Record) error {
		records++
		return nil
	}); err != nil {
		return 0, err
	}
	return records, nil
}

// RebuildIndex reconstructs the plaintext term index for an unencrypted archive.
// Encrypted archives intentionally do not expose searchable terms in a sidecar.
func (a *Archive) RebuildIndex(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.aead != nil {
		return errors.New("encrypted archives do not use a plaintext index")
	}
	if err := a.flushLocked(); err != nil {
		return err
	}
	tmp := a.indexPath + ".tmp"
	_ = os.Remove(tmp)
	index, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer index.Close()
	f, err := os.Open(a.path)
	if err != nil {
		return err
	}
	defer f.Close()
	reader := bufio.NewReaderSize(f, 64*1024)
	header := make([]byte, len(archiveMagic))
	if _, err := io.ReadFull(reader, header); err != nil || !bytes.Equal(header, archiveMagic[:]) {
		return errors.New("invalid knowledge archive header")
	}
	decoder, err := zstd.NewReader(nil)
	if err != nil {
		return err
	}
	defer decoder.Close()
	offset := int64(len(archiveMagic))
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		var frame [16]byte
		if _, err := io.ReadFull(reader, frame[:]); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return err
		}
		rawLen := binary.BigEndian.Uint64(frame[:8])
		compressedLen := binary.BigEndian.Uint64(frame[8:])
		if rawLen == 0 || compressedLen == 0 || rawLen > maxFrameBytes || compressedLen > maxFrameBytes {
			return errors.New("knowledge archive contains an invalid frame size")
		}
		compressed := make([]byte, compressedLen)
		if _, err := io.ReadFull(reader, compressed); err != nil {
			return err
		}
		raw, err := decoder.DecodeAll(compressed, nil)
		if err != nil || uint64(len(raw)) != rawLen {
			return errors.New("knowledge archive frame decode failed")
		}
		entry, _ := json.Marshal(blockIndexEntry{Offset: offset, End: offset + int64(len(frame)) + int64(compressedLen), Terms: indexTerms(raw)})
		if _, err := index.Write(append(entry, '\n')); err != nil {
			return err
		}
		offset += int64(len(frame)) + int64(compressedLen)
	}
	if err := index.Sync(); err != nil {
		return err
	}
	if err := index.Close(); err != nil {
		return err
	}
	if a.indexFile != nil {
		_ = a.indexFile.Close()
	}
	if err := os.Rename(tmp, a.indexPath); err != nil {
		return err
	}
	a.indexFile, err = os.OpenFile(a.indexPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
	return err
}

// ExportTo creates a byte-for-byte snapshot of the compressed archive.
func (a *Archive) ExportTo(ctx context.Context, destination string) error {
	if destination == "" {
		return errors.New("export destination is empty")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.flushLocked(); err != nil {
		return err
	}
	if filepath.Clean(destination) == filepath.Clean(a.path) {
		return errors.New("export destination must differ from archive path")
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
		return err
	}
	in, err := os.Open(a.path)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp := destination + ".tmp"
	_ = os.Remove(tmp)
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(out, in)
	syncErr := out.Sync()
	closeErr := out.Close()
	if copyErr != nil {
		_ = os.Remove(tmp)
		return copyErr
	}
	if syncErr != nil {
		_ = os.Remove(tmp)
		return syncErr
	}
	if closeErr != nil {
		_ = os.Remove(tmp)
		return closeErr
	}
	return os.Rename(tmp, destination)
}

// RestoreFrom validates and atomically replaces the active archive with a snapshot.
func (a *Archive) RestoreFrom(ctx context.Context, source string) error {
	if source == "" || filepath.Clean(source) == filepath.Clean(a.path) {
		return errors.New("restore source must differ from archive path")
	}
	validated, err := Open(source)
	if err != nil {
		return err
	}
	if _, err := validated.Verify(ctx); err != nil {
		_ = validated.Close()
		return fmt.Errorf("validate restore source: %w", err)
	}
	if err := validated.Close(); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.flushLocked(); err != nil {
		return err
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	tmp := a.path + ".restore"
	_ = os.Remove(tmp)
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		_ = in.Close()
		return err
	}
	_, copyErr := io.Copy(out, in)
	_ = in.Close()
	if copyErr == nil {
		copyErr = out.Sync()
	}
	closeErr := out.Close()
	if copyErr != nil || closeErr != nil {
		_ = os.Remove(tmp)
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	}
	if err := a.file.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if a.indexFile != nil {
		_ = a.indexFile.Close()
		a.indexFile = nil
	}
	a.encoder.Close()
	if err := os.Rename(tmp, a.path); err != nil {
		return err
	}
	if a.indexPath != "" {
		_ = os.Remove(a.indexPath)
		if sourceIndex := source + ".idx"; func() bool { _, e := os.Stat(sourceIndex); return e == nil }() {
			_ = copyFile(sourceIndex, a.indexPath)
		}
	}
	reopened, err := Open(a.path)
	if err != nil {
		return err
	}
	a.file, a.encoder, a.aead, a.indexFile = reopened.file, reopened.encoder, reopened.aead, reopened.indexFile
	a.indexPath, a.records = reopened.indexPath, reopened.records
	return nil
}

// RotateEncryptionKey rewrites the archive with a new AES-GCM key.
// The caller must persist the new key before restarting the process.
func (a *Archive) RotateEncryptionKey(ctx context.Context, newKey string) error {
	if strings.TrimSpace(newKey) == "" {
		return errors.New("new knowledge encryption key is empty")
	}
	newAEAD, err := encryptionForKey(newKey)
	if err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.flushLocked(); err != nil {
		return err
	}
	tmp := a.path + ".rotate"
	_ = os.Remove(tmp)
	out, err := Open(tmp)
	if err != nil {
		return err
	}
	if out.indexFile != nil {
		_ = out.indexFile.Close()
		out.indexFile = nil
		_ = os.Remove(out.indexPath)
		out.indexPath = ""
	}
	out.aead = newAEAD
	var count int64
	err = a.forEachRecordLocked(ctx, func(record Record) error {
		if err := out.Append(ctx, record); err != nil {
			return err
		}
		count++
		return nil
	})
	if closeErr := out.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := a.file.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if a.indexFile != nil {
		_ = a.indexFile.Close()
		a.indexFile = nil
	}
	a.encoder.Close()
	if err := os.Rename(tmp, a.path); err != nil {
		return err
	}
	_ = os.Remove(a.path + ".idx")
	reopened, err := Open(a.path)
	if err != nil {
		return err
	}
	if reopened.indexFile != nil {
		_ = reopened.indexFile.Close()
		reopened.indexFile = nil
		_ = os.Remove(reopened.indexPath)
		reopened.indexPath = ""
	}
	reopened.aead = newAEAD
	a.file, a.encoder, a.aead, a.indexFile = reopened.file, reopened.encoder, reopened.aead, reopened.indexFile
	a.indexPath, a.records = reopened.indexPath, count
	return nil
}

func copyFile(source, destination string) error {
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func (a *Archive) countBlocksLocked() (int64, error) {
	f, err := os.Open(a.path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	reader := bufio.NewReaderSize(f, 64*1024)
	header := make([]byte, len(archiveMagic))
	if _, err := io.ReadFull(reader, header); err != nil {
		return 0, err
	}
	if !bytes.Equal(header, archiveMagic[:]) {
		return 0, errors.New("invalid knowledge archive header")
	}
	var blocks int64
	for {
		var frame [16]byte
		if _, err := io.ReadFull(reader, frame[:]); err != nil {
			if errors.Is(err, io.EOF) {
				return blocks, nil
			}
			return 0, err
		}
		compressedLen := binary.BigEndian.Uint64(frame[8:16])
		if compressedLen == 0 || compressedLen > maxFrameBytes {
			return 0, errors.New("knowledge archive contains an invalid frame size")
		}
		if _, err := io.CopyN(io.Discard, reader, int64(compressedLen)); err != nil {
			return 0, err
		}
		blocks++
	}
}

func (a *Archive) Remember(ctx context.Context, text, kind, source string, tags []string) (Record, error) {
	r := Record{ID: fmt.Sprintf("remember_%d", time.Now().UnixNano()), Timestamp: time.Now().UTC(), Kind: kind, Text: text, Source: source, Tags: tags, Confidence: 1}
	return r, a.Append(ctx, r)
}

// ImportText stores an externally captured transcript as a first-class record.
func (a *Archive) ImportText(ctx context.Context, text, kind, source, projectID string, tags []string) (Record, error) {
	r := Record{ID: fmt.Sprintf("import_%d", time.Now().UnixNano()), Timestamp: time.Now().UTC(), Kind: kind, Text: text, Source: source, ProjectID: projectID, Tags: tags, Confidence: 1}
	return r, a.Append(ctx, r)
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

func AssessQuality(records []Record) QualityReport {
	report := QualityReport{Records: len(records)}
	seen := make(map[string]struct{})
	conflictKeys := make(map[string]map[string]struct{})
	sources := make(map[string]struct{})
	now := time.Now().UTC()
	for _, record := range records {
		if record.Source != "" {
			sources[record.Source] = struct{}{}
		}
		if record.Confidence > 0 {
			report.AverageConfidence += record.Confidence
		}
		if !record.Timestamp.IsZero() && record.Timestamp.Before(now.Add(-30*24*time.Hour)) {
			report.Stale++
		}
		if record.Verified {
			report.Verified++
		} else {
			report.Unverified++
		}
		if record.Confidence > 0 && record.Confidence < 0.5 {
			report.LowConfidence++
		}
		if record.ExpiresAt != nil && record.ExpiresAt.Before(now) {
			report.Expired++
		}
		key := record.ContentHash
		if key == "" {
			key = strings.ToLower(strings.TrimSpace(record.Kind + "\x00" + record.Text))
		}
		if _, exists := seen[key]; exists {
			report.Duplicates++
		}
		seen[key] = struct{}{}
		if record.Kind != "" && record.Supersedes != "" {
			if conflictKeys[record.Supersedes] == nil {
				conflictKeys[record.Supersedes] = make(map[string]struct{})
			}
			conflictKeys[record.Supersedes][record.ContentHash] = struct{}{}
		}
	}
	for _, variants := range conflictKeys {
		if len(variants) > 1 {
			report.Conflicts++
		}
	}
	report.SourceCount = len(sources)
	if report.Records > 0 {
		report.AverageConfidence /= float64(report.Records)
	}
	return report
}
