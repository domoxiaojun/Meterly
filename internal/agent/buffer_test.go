package agent

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/domoxiaojun/Meterly/internal/proto"
)

func recs(from, n int64) []proto.Record {
	out := make([]proto.Record, n)
	for i := range out {
		out[i] = proto.Record{Seq: from + int64(i), TS: 1794000000 + int64(i)*60, Kind: "iface", Dim1: "eth0", RX: 1, TX: 2}
	}
	return out
}

func TestBufferAppendAckReopen(t *testing.T) {
	dir := t.TempDir()
	b, err := OpenBuffer(dir, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	b.Append(recs(1, 10))
	if b.Count() != 10 || b.LastSeq() != 10 {
		t.Fatalf("count=%d last=%d", b.Count(), b.LastSeq())
	}
	p, _ := b.Pending(4)
	if len(p) != 4 || p[0].Seq != 1 {
		t.Fatal(p)
	}
	b.Ack(4)
	b.Close()
	b, _ = OpenBuffer(dir, 1<<20)
	if b.Count() != 6 || b.LastSeq() != 10 {
		t.Fatalf("重开后 count=%d last=%d", b.Count(), b.LastSeq())
	}
	p, _ = b.Pending(100)
	if p[0].Seq != 5 || len(p) != 6 {
		t.Fatal(p)
	}
	if st, _ := os.Stat(filepath.Join(dir, "ack")); st.Mode().Perm() != 0o600 {
		t.Fatal("ack 文件权限应为 600")
	}
}

func TestBufferRepairsTornTail(t *testing.T) {
	dir := t.TempDir()
	b, _ := OpenBuffer(dir, 1<<20)
	b.Append(recs(1, 3))
	b.Close()
	segs, _ := filepath.Glob(filepath.Join(dir, "seg-*.jsonl"))
	f, _ := os.OpenFile(segs[0], os.O_APPEND|os.O_WRONLY, 0)
	f.WriteString(`{"seq":4,"ts":17940`) // 崩溃时写了一半
	f.Close()
	b, err := OpenBuffer(dir, 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if b.LastSeq() != 3 || b.Count() != 3 {
		t.Fatalf("last=%d count=%d", b.LastSeq(), b.Count())
	}
	b.Append(recs(4, 1))
	p, _ := b.Pending(10)
	if len(p) != 4 || p[3].Seq != 4 {
		t.Fatalf("修复后追加失败: %v", p)
	}
}

func TestBufferLimitPausesWithoutEviction(t *testing.T) {
	b, e := OpenBuffer(t.TempDir(), 1<<20)
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	seq := int64(1)
	for {
		_, e = b.Append(recs(seq, 100))
		if errors.Is(e, ErrBufferFull) {
			break
		}
		if e != nil {
			t.Fatal(e)
		}
		seq += 100
	}
	if b.Size() > 1<<20 || b.Count() != seq-1 {
		t.Fatal("capacity or count changed", b.Size(), b.Count())
	}
	pending, e := b.Pending(1)
	if e != nil || pending[0].Seq != 1 {
		t.Fatal("oldest unacknowledged record lost")
	}
	if e = b.Ack(seq - 1); e != nil {
		t.Fatal(e)
	}
	if _, e = b.Append(recs(seq, 100)); e != nil {
		t.Fatal("upload did not release capacity", e)
	}
}
