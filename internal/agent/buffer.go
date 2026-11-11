package agent

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/domoxiaojun/Meterly/internal/proto"
)

// Buffer 本地持久化缓冲:带总量上限的 JSONL 分段文件。
//
//	<dir>/seg-<首条seq>.jsonl   每行一条 proto.Record,seq 连续递增
//	<dir>/ack                   中心已确认的最大 seq
//
// 达到容量上限时拒绝追加；调用方保留 Pending，上传后重试，不删除未确认分段。
type Buffer struct {
	mu                     sync.Mutex
	dir                    string
	maxBytes               int64
	segBytes               int64
	segs                   []*segment
	acked                  int64
	dropped                int64 // 本进程生命周期内因超限丢弃的条数(累计值由 State 持久化)
	droppedFrom, droppedTo int64
	cur                    *os.File
}

type segment struct {
	path        string
	first, last int64 // last < first 表示空段
	size        int64
}

const defaultSegBytes = 4 << 20

var ErrBufferFull = errors.New("缓冲容量已满，保留待写记录并暂停采集，等待上传释放空间")

func OpenBuffer(dir string, maxBytes int64) (*Buffer, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	if maxBytes <= 0 {
		maxBytes = 256 << 20
	}
	b := &Buffer{dir: dir, maxBytes: maxBytes, segBytes: defaultSegBytes}
	if maxBytes/8 < b.segBytes {
		b.segBytes = max(maxBytes/8, 64<<10)
	}
	if v, err := os.ReadFile(filepath.Join(dir, "ack")); err == nil {
		b.acked, _ = strconv.ParseInt(strings.TrimSpace(string(v)), 10, 64)
	}
	names, _ := filepath.Glob(filepath.Join(dir, "seg-*.jsonl"))
	for _, n := range names {
		base := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(n), "seg-"), ".jsonl")
		first, err := strconv.ParseInt(base, 10, 64)
		if err != nil {
			continue
		}
		st, err := os.Stat(n)
		if err != nil {
			continue
		}
		b.segs = append(b.segs, &segment{path: n, first: first, last: first - 1, size: st.Size()})
	}
	sort.Slice(b.segs, func(i, j int) bool { return b.segs[i].first < b.segs[j].first })
	for i, s := range b.segs {
		if i+1 < len(b.segs) {
			s.last = b.segs[i+1].first - 1
			continue
		}
		if err := s.repairTail(); err != nil {
			return nil, err
		}
	}
	return b, nil
}

// repairTail 截掉崩溃时写了一半的尾行,并读出最后一条 seq。
func (s *segment) repairTail() error {
	data, err := os.ReadFile(s.path)
	if err != nil {
		return err
	}
	if n := bytes.LastIndexByte(data, '\n'); n+1 != len(data) {
		data = data[:n+1]
		if err := os.Truncate(s.path, int64(n+1)); err != nil {
			return err
		}
	}
	s.size = int64(len(data))
	lines := bytes.Split(bytes.TrimRight(data, "\n"), []byte{'\n'})
	for i := len(lines) - 1; i >= 0; i-- {
		var r proto.Record
		if json.Unmarshal(lines[i], &r) == nil && r.Seq > 0 {
			s.last = r.Seq
			return nil
		}
	}
	return nil
}

// LastSeq 已写入缓冲的最大 seq(含已确认后被删除的段)。
func (b *Buffer) LastSeq() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.lastSeqLocked()
}

func (b *Buffer) lastSeqLocked() int64 {
	last := b.acked
	for _, s := range b.segs {
		if s.last > last {
			last = s.last
		}
	}
	return last
}

// Append 追加记录(调用方保证 seq 连续递增),fsync 后返回;返回因超限新丢弃的条数。
func (b *Buffer) Append(recs []proto.Record) (int64, error) {
	if len(recs) == 0 {
		return 0, nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	var buf bytes.Buffer
	for _, r := range recs {
		line, err := json.Marshal(r)
		if err != nil {
			return 0, err
		}
		buf.Write(line)
		buf.WriteByte('\n')
	}
	// Reclaim acknowledged segments before enforcing the limit. Unacknowledged data is never evicted.
	for len(b.segs) > 0 && b.segs[0].last <= b.acked {
		seg := b.segs[0]
		if len(b.segs) == 1 && b.cur != nil {
			_ = b.cur.Close()
			b.cur = nil
		}
		if e := os.Remove(seg.path); e != nil && !os.IsNotExist(e) {
			return 0, e
		}
		b.segs = b.segs[1:]
	}
	if b.totalSize()+int64(buf.Len()) > b.maxBytes {
		return 0, ErrBufferFull
	}
	tail := b.tail()
	if tail == nil || tail.size >= b.segBytes {
		if b.cur != nil {
			b.cur.Close()
			b.cur = nil
		}
		tail = &segment{path: filepath.Join(b.dir, fmt.Sprintf("seg-%020d.jsonl", recs[0].Seq)), first: recs[0].Seq, last: recs[0].Seq - 1}
		b.segs = append(b.segs, tail)
	}
	if b.cur == nil {
		f, err := os.OpenFile(tail.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return 0, err
		}
		b.cur = f
	}
	rollback := func(err error) (int64, error) {
		_ = b.cur.Truncate(tail.size)
		_ = b.cur.Sync()
		_ = b.cur.Close()
		b.cur = nil
		return 0, err
	}
	if _, err := b.cur.Write(buf.Bytes()); err != nil {
		return rollback(err)
	}
	if err := b.cur.Sync(); err != nil {
		return rollback(err)
	}
	tail.size += int64(buf.Len())
	tail.last = recs[len(recs)-1].Seq
	return 0, nil
}

func (b *Buffer) tail() *segment {
	if len(b.segs) == 0 {
		return nil
	}
	return b.segs[len(b.segs)-1]
}

func (b *Buffer) totalSize() int64 {
	var n int64
	for _, s := range b.segs {
		n += s.size
	}
	return n
}

// Pending 返回 seq > acked 的最早 n 条记录。
func (b *Buffer) Pending(n int) ([]proto.Record, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []proto.Record
	for _, s := range b.segs {
		if s.last <= b.acked {
			continue
		}
		f, err := os.Open(s.path)
		if err != nil {
			return out, err
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 64<<10), 1<<20)
		for sc.Scan() {
			var r proto.Record
			if json.Unmarshal(sc.Bytes(), &r) != nil || r.Seq <= b.acked {
				continue
			}
			out = append(out, r)
			if len(out) >= n {
				break
			}
		}
		f.Close()
		if err := sc.Err(); err != nil {
			return nil, err
		}
		if len(out) >= n {
			break
		}
	}
	return out, nil
}

// Ack 标记 seq 及之前的记录已被中心确认,并删除已全部确认的旧段(保留正在写的段)。
func (b *Buffer) Ack(seq int64) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if seq <= b.acked {
		return nil
	}
	prev := b.acked
	b.acked = seq
	if err := b.writeAck(); err != nil {
		b.acked = prev
		return err
	}
	for len(b.segs) > 1 && b.segs[0].last <= b.acked {
		os.Remove(b.segs[0].path)
		b.segs = b.segs[1:]
	}
	return nil
}

func (b *Buffer) writeAck() error {
	return writeFileAtomic(filepath.Join(b.dir, "ack"), []byte(strconv.FormatInt(b.acked, 10)+"\n"))
}

func (b *Buffer) DroppedRange() (int64, int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.droppedFrom, b.droppedTo
}

// Count 未确认记录条数。
func (b *Buffer) Count() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	var n int64
	for _, s := range b.segs {
		from := max(s.first, b.acked+1)
		if s.last >= from {
			n += s.last - from + 1
		}
	}
	return n
}

// Size 缓冲文件总字节数。
func (b *Buffer) Size() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.totalSize()
}

func (b *Buffer) Close() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.cur != nil {
		return b.cur.Close()
	}
	return nil
}

// writeFileAtomic 写临时文件 + fsync + rename,权限 600。
func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// AppendChunks persists bounded pieces. A failed suffix remains in State.Pending;
// LastSeq lets the caller safely skip already durable pieces on retry.
func (b *Buffer) AppendChunks(recs []proto.Record) error {
	limit := max(b.maxBytes/4, int64(4096))
	for i := 0; i < len(recs); {
		j := i
		size := int64(0)
		for j < len(recs) {
			raw, e := json.Marshal(recs[j])
			if e != nil {
				return e
			}
			n := int64(len(raw) + 1)
			if j > i && size+n > limit {
				break
			}
			size += n
			j++
		}
		if _, e := b.Append(recs[i:j]); e != nil {
			return e
		}
		i = j
	}
	return nil
}
