package download

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"hash"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// --- Cross-route resumable file downloads (1.7.12) ---
//
// The route plan's legs used to restart the body from byte zero
// on every attempt: tmpPath was truncated per attempt, so a leg
// that died mid-transfer threw away everything it had fetched.
// On slow nodes that was fatal inside the shared whole-operation
// budget (240s for the self-upgrade): the first admitted route
// trickled most of the budget away, and every later leg inherited
// an almost-expired context and died on its first GET — the
// receipt showed all 10 routes dead although most were never
// really tried (production report 2026-11-01: batch upgrade of
// domestic nodes, direct probe latency 1000ms+).
//
// FileTransfer carries ONE file's download state across the legs
// of a single operation: bytes an attempt managed to persist stay
// on disk, and the next leg resumes after them with a Range
// request instead of starting over. The integrity caliber is
// unchanged — consumers still verify the assembled file against
// the release SHA-256, which is the backstop for every assembly
// hazard (a prefix that is not a true prefix of the asset can
// only produce a hash mismatch, which fails the operation closed;
// the partial file is discarded by the caller, so a later
// operation starts from zero again).

// FileTransfer is the resumable state of one file download: the
// target path, the number of bytes persisted so far, and a
// running SHA-256 over exactly those bytes. Create one per
// download operation and drive it from that operation's TryPlan
// attempt closure. Not safe for concurrent use.
type FileTransfer struct {
	path     string
	maxBytes int64 // total file size cap; <= 0 means no cap
	perm     os.FileMode

	ready  bool // persisted prefix has been folded into hasher
	have   int64
	hasher hash.Hash
}

// NewFileTransfer starts a transfer for path. A file left at path
// by an earlier attempt OF THE SAME operation is adopted as the
// resume prefix on the first Attempt; callers stage into a fresh
// temp path per operation, so nothing stale is ever adopted.
func NewFileTransfer(path string, maxBytes int64, perm os.FileMode) *FileTransfer {
	return &FileTransfer{path: path, maxBytes: maxBytes, perm: perm, hasher: sha256.New()}
}

// Size returns the bytes persisted so far.
func (t *FileTransfer) Size() int64 { return t.have }

// Sum returns the hex SHA-256 of the persisted bytes — the whole
// file's hash once the transfer is complete.
func (t *FileTransfer) Sum() string { return hex.EncodeToString(t.hasher.Sum(nil)) }

// Reset discards every persisted byte (file truncated, hash
// restarted), so the next Attempt starts from byte zero. A
// consumer whose completed assembly fails verification uses it
// to keep the pre-resume caliber: the next route restarts the
// file instead of resuming onto unverifiable bytes.
func (t *FileTransfer) Reset() error {
	t.hasher.Reset()
	t.have = 0
	t.ready = true
	if err := os.Truncate(t.path, 0); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("截断已下载文件失败: %v", err)
	}
	return nil
}

// init folds a pre-existing file at path (written by an earlier
// leg of this operation) into the hash state, once.
func (t *FileTransfer) init() error {
	if t.ready {
		return nil
	}
	f, err := os.Open(t.path)
	if os.IsNotExist(err) {
		t.ready = true
		return nil
	}
	if err != nil {
		return fmt.Errorf("读取已下载断点失败: %v", err)
	}
	defer f.Close()
	n, err := io.Copy(t.hasher, f)
	if err != nil {
		return fmt.Errorf("读取已下载断点失败: %v", err)
	}
	t.have = n
	t.ready = true
	return nil
}

// Attempt performs ONE download leg of the transfer over an
// already-built route URL: it issues the GET through do (the
// caller's client, whose redirect policy and transport stay the
// caller's), resuming from the persisted prefix when there is
// one, and streams the body onto the file while hashing it.
//
// Leg outcomes:
//   - 206 Partial Content: the Content-Range start must equal
//     the persisted size; a mismatching (or unparsable) range
//     wastes the leg WITHOUT touching the persisted bytes, and
//     the next leg resumes from the same offset.
//   - 200 OK on a resume request (server ignored Range): the leg
//     restarts the file — the persisted prefix is truncated and
//     the full body is written from byte zero.
//   - 416 on a resume request: the persisted prefix cannot be a
//     prefix of the remote object, so it is discarded (Reset)
//     and the leg fails; the next leg restarts from zero.
//   - A body that breaks mid-transfer keeps the bytes it managed
//     to persist; the returned error fails the leg and the next
//     route resumes after them.
//   - Progress judgment is the caller's watchdog's call, exactly
//     like the other attempt shapes: watcher (may be nil) feeds
//     on every chunk; a stall surfaces as ErrNoProgress and a
//     below-floor trickle as ErrTooSlow (WDG-01/WDG-02).
//
// report, when non-nil, receives (persistedBytes, totalBytes)
// after every chunk whenever the total is known.
func (t *FileTransfer) Attempt(ctx context.Context, watcher *Watcher, do func(*http.Request) (*http.Response, error), rawURL string, report func(downloaded, total int64)) error {
	if err := t.init(); err != nil {
		return err
	}
	if watcher == nil {
		watcher = NewWatcher(0)
	}
	actx, stopWatch := watcher.Watch(ctx)
	defer stopWatch()

	req, err := http.NewRequestWithContext(actx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	resumeFrom := t.have
	if resumeFrom > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", resumeFrom))
	}
	resp, err := do(req)
	if err != nil {
		if watcher.Stalled() {
			return ErrNoProgress
		}
		if watcher.TooSlow() {
			return ErrTooSlow
		}
		return err
	}
	defer resp.Body.Close()

	var total int64 = -1
	switch {
	case resp.StatusCode == http.StatusOK:
		// Full body: the leg (re)starts the file. When this was a
		// resume request the server ignored Range — the persisted
		// prefix is superseded and truncated below.
		total = resp.ContentLength
	case resp.StatusCode == http.StatusPartialContent:
		start, rangeTotal, perr := parseContentRange(resp.Header.Get("Content-Range"))
		if perr != nil {
			return fmt.Errorf("续传响应 Content-Range 不可解析: %v", perr)
		}
		if start != resumeFrom {
			return fmt.Errorf("续传响应 Content-Range 起点 %d 与断点 %d 不符，本线路作废", start, resumeFrom)
		}
		total = rangeTotal
		if total < 0 && resp.ContentLength >= 0 {
			total = resumeFrom + resp.ContentLength
		}
	case resp.StatusCode == http.StatusRequestedRangeNotSatisfiable && resumeFrom > 0:
		if rerr := t.Reset(); rerr != nil {
			return rerr
		}
		return fmt.Errorf("HTTP状态码: 416（断点超出文件范围，已丢弃断点，下条线路从头下载）")
	default:
		return fmt.Errorf("HTTP状态码: %d", resp.StatusCode)
	}
	if t.maxBytes > 0 && total > t.maxBytes {
		return fmt.Errorf("文件过大 (%d bytes)，超过大小限制 (%d bytes)，拒绝下载", total, t.maxBytes)
	}

	out, err := os.OpenFile(t.path, os.O_CREATE|os.O_WRONLY, t.perm)
	if err != nil {
		return fmt.Errorf("创建临时文件失败: %v", err)
	}
	defer out.Close()
	if resp.StatusCode == http.StatusOK && resumeFrom > 0 {
		if err := out.Truncate(0); err != nil {
			return fmt.Errorf("截断已下载文件失败: %v", err)
		}
		t.hasher.Reset()
		t.have = 0
	}
	if _, err := out.Seek(t.have, io.SeekStart); err != nil {
		return fmt.Errorf("定位已下载文件失败: %v", err)
	}

	buf := make([]byte, 32*1024)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			watcher.Note(n)
			if t.maxBytes > 0 && t.have+int64(n) > t.maxBytes {
				return fmt.Errorf("文件超过大小限制 (%d bytes)，拒绝下载", t.maxBytes)
			}
			if _, wErr := out.Write(buf[:n]); wErr != nil {
				return fmt.Errorf("写入下载文件失败: %v", wErr)
			}
			t.hasher.Write(buf[:n])
			t.have += int64(n)
			if total > 0 && report != nil {
				report(t.have, total)
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				break
			}
			if watcher.Stalled() {
				return fmt.Errorf("读取响应正文失败: %w", ErrNoProgress)
			}
			if watcher.TooSlow() {
				return fmt.Errorf("读取响应正文失败: %w", ErrTooSlow)
			}
			return fmt.Errorf("读取响应正文失败: %v", readErr)
		}
	}
	if total > 0 && t.have < total {
		return fmt.Errorf("响应正文不完整: 已下载 %d / %d bytes", t.have, total)
	}
	if total > 0 && t.have > total {
		return fmt.Errorf("响应正文超出声明长度: 已下载 %d / %d bytes", t.have, total)
	}
	if t.have == 0 {
		return fmt.Errorf("下载内容为空")
	}
	return nil
}

// parseContentRange parses a "bytes <start>-<end>/<total>"
// Content-Range header (total may be "*" → -1).
func parseContentRange(v string) (start, total int64, err error) {
	total = -1
	rest, ok := strings.CutPrefix(v, "bytes ")
	if !ok {
		return 0, -1, fmt.Errorf("缺少 bytes 单位: %q", v)
	}
	rangePart, totalPart, ok := strings.Cut(rest, "/")
	if !ok {
		return 0, -1, fmt.Errorf("缺少总数段: %q", v)
	}
	startPart, _, ok := strings.Cut(rangePart, "-")
	if !ok {
		return 0, -1, fmt.Errorf("缺少范围段: %q", v)
	}
	start, err = strconv.ParseInt(strings.TrimSpace(startPart), 10, 64)
	if err != nil {
		return 0, -1, fmt.Errorf("起点不可解析: %q", v)
	}
	if totalPart != "*" {
		total, err = strconv.ParseInt(strings.TrimSpace(totalPart), 10, 64)
		if err != nil {
			return 0, -1, fmt.Errorf("总数不可解析: %q", v)
		}
	}
	return start, total, nil
}
