package archive

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const HeaderHashBytes = 4096

var (
	ErrOffsetMismatch = errors.New("offset mismatch")
	ErrHeaderMismatch = errors.New("header hash mismatch")
	ErrInvalidPath    = errors.New("invalid relative path")
)

// Store manages lossless on-disk archiving of raw collector files.
// Layout:
//
//	<root>/files/<device_id>/<source>/<rel_path>
//	<root>/versions/<device_id>/<source>/<timestamp>_<sha8>_<rel_path>
type Store struct {
	root             string
	keepAllSnapshots bool
	locks            sync.Map // archivePath -> *sync.Mutex
}

// New creates an Archive Store at root.
func New(root string, keepAllSnapshots bool) (*Store, error) {
	if err := os.MkdirAll(filepath.Join(root, "files"), 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(root, "versions"), 0o755); err != nil {
		return nil, err
	}
	return &Store{root: root, keepAllSnapshots: keepAllSnapshots}, nil
}

// Root returns the archive root directory.
func (s *Store) Root() string { return s.root }

// ResolvePath safely maps (deviceID, source, relPath) to a relative path under <root>/files.
func (s *Store) ResolvePath(deviceID, source, relPath string) (relArchivePath string, absPath string, err error) {
	dev := sanitizeSegment(deviceID)
	src := sanitizeSegment(source)
	cleanRel, err := sanitizeRelPath(relPath)
	if err != nil {
		return "", "", err
	}
	if dev == "" || src == "" {
		return "", "", ErrInvalidPath
	}
	relArchivePath = filepath.Join("files", dev, src, cleanRel)
	absPath = filepath.Join(s.root, relArchivePath)
	if !strings.HasPrefix(absPath, filepath.Join(s.root, "files")+string(os.PathSeparator)) {
		return "", "", ErrInvalidPath
	}
	return relArchivePath, absPath, nil
}

// AbsPath resolves a stored relative archive path back to an absolute path.
func (s *Store) AbsPath(relArchivePath string) string {
	return filepath.Join(s.root, relArchivePath)
}

func (s *Store) lockFor(key string) *sync.Mutex {
	v, _ := s.locks.LoadOrStore(key, &sync.Mutex{})
	return v.(*sync.Mutex)
}

// AppendResult describes the state of the archived file after AppendChunk.
type AppendResult struct {
	RelArchivePath  string
	AbsPath         string
	ByteSize        int64
	SHA256          string
	HeaderSHA256    string
	ArchivedVersion string
}

// AppendChunk appends data to a raw file at the expected byte offset.
// If offset == 0 and the file already exists (e.g. rotated/rewritten), the old file is
// preserved under versions/ before writing the new data so nothing is ever lost.
func (s *Store) AppendChunk(deviceID, source, relPath string, offset int64, expectedHeaderSHA string, r io.Reader) (*AppendResult, error) {
	relArch, absPath, err := s.ResolvePath(deviceID, source, relPath)
	if err != nil {
		return nil, err
	}
	mu := s.lockFor(absPath)
	mu.Lock()
	defer mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		return nil, err
	}

	var archivedVer string
	st, statErr := os.Stat(absPath)
	if statErr == nil {
		if offset == 0 && st.Size() > 0 {
			// Preserve previous version before overwriting so we never lose data.
			archivedVer, err = s.preserveVersionLocked(deviceID, source, relPath, absPath)
			if err != nil {
				return nil, fmt.Errorf("preserve previous version: %w", err)
			}
			if err := os.Remove(absPath); err != nil && !os.IsNotExist(err) {
				return nil, err
			}
			st = nil
		} else if offset != st.Size() {
			return nil, fmt.Errorf("%w: server=%d client=%d", ErrOffsetMismatch, st.Size(), offset)
		} else if expectedHeaderSHA != "" && st.Size() > 0 {
			curHeader, herr := HashHeader(absPath)
			if herr != nil {
				return nil, herr
			}
			if curHeader != expectedHeaderSHA {
				return nil, fmt.Errorf("%w: server=%s client=%s", ErrHeaderMismatch, curHeader, expectedHeaderSHA)
			}
		}
	} else if !os.IsNotExist(statErr) {
		return nil, statErr
	} else if offset != 0 {
		return nil, fmt.Errorf("%w: server=0 client=%d", ErrOffsetMismatch, offset)
	}

	flags := os.O_CREATE | os.O_WRONLY | os.O_APPEND
	f, err := os.OpenFile(absPath, flags, 0o644)
	if err != nil {
		return nil, err
	}
	_, copyErr := io.Copy(f, r)
	closeErr := f.Close()
	if copyErr != nil {
		return nil, copyErr
	}
	if closeErr != nil {
		return nil, closeErr
	}

	size, fullHash, headerHash, err := FileHashes(absPath)
	if err != nil {
		return nil, err
	}
	return &AppendResult{
		RelArchivePath:  relArch,
		AbsPath:         absPath,
		ByteSize:        size,
		SHA256:          fullHash,
		HeaderSHA256:    headerHash,
		ArchivedVersion: archivedVer,
	}, nil
}

// WriteSnapshot writes a full immutable/snapshot file atomically.
// If the existing file has the same SHA-256, it marks Duplicate=true and avoids rewriting.
// If the content changed and keepAllSnapshots is enabled, the previous version is moved into versions/.
type SnapshotResult struct {
	RelArchivePath  string
	AbsPath         string
	ByteSize        int64
	SHA256          string
	HeaderSHA256    string
	Duplicate       bool
	ArchivedVersion string
}

func (s *Store) WriteSnapshot(deviceID, source, relPath string, r io.Reader) (*SnapshotResult, error) {
	relArch, absPath, err := s.ResolvePath(deviceID, source, relPath)
	if err != nil {
		return nil, err
	}
	mu := s.lockFor(absPath)
	mu.Lock()
	defer mu.Unlock()

	if err := os.MkdirAll(filepath.Dir(absPath), 0o755); err != nil {
		return nil, err
	}

	tmpPath := absPath + ".tmp." + fmt.Sprintf("%d", time.Now().UnixNano())
	tmpFile, err := os.OpenFile(tmpPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return nil, err
	}

	fullHasher := sha256.New()
	headerHasher := sha256.New()
	var headerWritten int64
	var totalWritten int64

	buf := make([]byte, 64*1024)
	for {
		n, rerr := r.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			if _, werr := tmpFile.Write(chunk); werr != nil {
				tmpFile.Close()
				os.Remove(tmpPath)
				return nil, werr
			}
			fullHasher.Write(chunk)
			if headerWritten < HeaderHashBytes {
				rem := HeaderHashBytes - headerWritten
				if int64(n) < rem {
					rem = int64(n)
				}
				headerHasher.Write(chunk[:rem])
				headerWritten += rem
			}
			totalWritten += int64(n)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			tmpFile.Close()
			os.Remove(tmpPath)
			return nil, rerr
		}
	}
	if err := tmpFile.Close(); err != nil {
		os.Remove(tmpPath)
		return nil, err
	}

	newHash := hex.EncodeToString(fullHasher.Sum(nil))
	newHeaderHash := hex.EncodeToString(headerHasher.Sum(nil))

	var archivedVer string
	if st, statErr := os.Stat(absPath); statErr == nil && st.Size() > 0 {
		_, curHash, curHeader, herr := FileHashes(absPath)
		if herr == nil && curHash == newHash {
			os.Remove(tmpPath)
			return &SnapshotResult{
				RelArchivePath: relArch,
				AbsPath:        absPath,
				ByteSize:       st.Size(),
				SHA256:         curHash,
				HeaderSHA256:   curHeader,
				Duplicate:      true,
			}, nil
		}
		if s.keepAllSnapshots {
			archivedVer, err = s.preserveVersionLocked(deviceID, source, relPath, absPath)
			if err != nil {
				os.Remove(tmpPath)
				return nil, fmt.Errorf("preserve snapshot version: %w", err)
			}
		}
	}

	if err := os.Rename(tmpPath, absPath); err != nil {
		os.Remove(tmpPath)
		return nil, err
	}

	return &SnapshotResult{
		RelArchivePath:  relArch,
		AbsPath:         absPath,
		ByteSize:        totalWritten,
		SHA256:          newHash,
		HeaderSHA256:    newHeaderHash,
		ArchivedVersion: archivedVer,
	}, nil
}

func (s *Store) preserveVersionLocked(deviceID, source, relPath, absPath string) (string, error) {
	_, sha, _, err := FileHashes(absPath)
	if err != nil {
		sha = "unknown"
	}
	short := sha
	if len(short) > 12 {
		short = short[:12]
	}
	ts := time.Now().UTC().Format("20060102T150405Z")
	cleanRel, err := sanitizeRelPath(relPath)
	if err != nil {
		return "", err
	}
	verRel := filepath.Join("versions", sanitizeSegment(deviceID), sanitizeSegment(source), ts+"_"+short, cleanRel)
	verAbs := filepath.Join(s.root, verRel)
	if err := os.MkdirAll(filepath.Dir(verAbs), 0o755); err != nil {
		return "", err
	}
	if err := os.Link(absPath, verAbs); err != nil {
		// Fallback to copy across filesystems
		if cerr := copyFile(absPath, verAbs); cerr != nil {
			return "", cerr
		}
	}
	return verRel, nil
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}

// FileHashes returns byte size, full SHA-256, and first-4KiB header SHA-256.
func FileHashes(path string) (int64, string, string, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, "", "", err
	}
	defer f.Close()
	full := sha256.New()
	header := sha256.New()
	var total, hdr int64
	buf := make([]byte, 64*1024)
	for {
		n, rerr := f.Read(buf)
		if n > 0 {
			full.Write(buf[:n])
			if hdr < HeaderHashBytes {
				rem := HeaderHashBytes - hdr
				if int64(n) < rem {
					rem = int64(n)
				}
				header.Write(buf[:rem])
				hdr += rem
			}
			total += int64(n)
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			return 0, "", "", rerr
		}
	}
	return total, hex.EncodeToString(full.Sum(nil)), hex.EncodeToString(header.Sum(nil)), nil
}

// HashHeader computes SHA-256 of up to the first HeaderHashBytes of a file.
func HashHeader(path string) (string, error) {
	return HashPrefix(path, HeaderHashBytes)
}

// HashPrefix computes SHA-256 of up to the first maxBytes of a file.
func HashPrefix(path string, maxBytes int64) (string, error) {
	if maxBytes > HeaderHashBytes {
		maxBytes = HeaderHashBytes
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if maxBytes > 0 {
		if _, err := io.CopyN(h, f, maxBytes); err != nil && !errors.Is(err, io.EOF) {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func sanitizeSegment(seg string) string {
	seg = strings.TrimSpace(seg)
	if seg == "" || seg == "." || seg == ".." {
		return ""
	}
	var b strings.Builder
	for _, r := range seg {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

func sanitizeRelPath(rel string) (string, error) {
	rel = strings.ReplaceAll(rel, "\\", "/")
	rel = strings.TrimLeft(rel, "/")
	if rel == "" {
		return "", ErrInvalidPath
	}
	cleaned := filepath.Clean(rel)
	if cleaned == "." || cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(os.PathSeparator)) || filepath.IsAbs(cleaned) {
		return "", ErrInvalidPath
	}
	return cleaned, nil
}
