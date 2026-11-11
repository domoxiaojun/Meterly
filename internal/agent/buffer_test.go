package agent

import (
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

func TestBufferLimitDropsOldest(t *testing.T) {
	dir := t.TempDir()
	b, _ := OpenBuffer(dir, 1<<20) // 段 128KiB
	var dropped int64
	seq := int64(1)
	for i := 0; i < 200; i++ {
		d, err := b.Append(recs(seq, 100))
		if err != nil {
			t.Fatal(err)
		}
		dropped += d
		seq += 100
	}
	if b.Size() > 1<<20+b.segBytes {
		t.Fatalf("超出上限: %d", b.Size())
	}
	if dropped == 0 || b.Count()+dropped != 20000 {
		t.Fatalf("丢弃计数不守恒: count=%d dropped=%d", b.Count(), dropped)
	}
	p, _ := b.Pending(1)
	if p[0].Seq != dropped+1 {
		t.Fatalf("应从最旧未丢弃记录开始: %d vs %d", p[0].Seq, dropped+1)
	}
}
