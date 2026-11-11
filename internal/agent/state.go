package agent

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"

	"github.com/domoxiaojun/Meterly/internal/proto"
)

// IfaceState 网卡计数器基线。
type IfaceState struct {
	BootID string `json:"boot_id"`
	RX     uint64 `json:"rx"`
	TX     uint64 `json:"tx"`
	TS     int64  `json:"ts"`
}

// State 每个汇总周期原子落盘一次(state.json,mode 600)。
//
// 崩溃一致性:先写入"新基线 + 本批待写记录(Pending)",再追加到缓冲;
// 启动时把 Pending 中 seq 大于缓冲最大 seq 的记录补写进缓冲,保证不丢不重。
type State struct {
	InstanceID  string                `json:"instance_id"`
	NextSeq     int64                 `json:"next_seq"`
	LastTick    int64                 `json:"last_tick"`
	Dropped     int64                 `json:"dropped"` // 缓冲超限累计丢弃条数
	Ifaces      map[string]IfaceState `json:"ifaces"`
	Singbox     SBState               `json:"singbox"`
	SiteOffsets map[string]LogOffset  `json:"site_offsets,omitempty"`
	Netbird     map[string]PeerState  `json:"netbird,omitempty"`
	Xray        map[string]uint64     `json:"xray,omitempty"`
	Pending     []proto.Record        `json:"pending,omitempty"`
}

func statePath(dir string) string { return filepath.Join(dir, "state.json") }

func LoadState(dir string) (*State, error) {
	b, err := os.ReadFile(statePath(dir))
	if errors.Is(err, os.ErrNotExist) {
		id := make([]byte, 8)
		if _, err := rand.Read(id); err != nil {
			return nil, err
		}
		return &State{InstanceID: hex.EncodeToString(id), NextSeq: 1, Ifaces: map[string]IfaceState{},
			SiteOffsets: map[string]LogOffset{}, Netbird: map[string]PeerState{}, Xray: map[string]uint64{}}, nil
	}
	if err != nil {
		return nil, err
	}
	s := &State{}
	if err := json.Unmarshal(b, s); err != nil {
		return nil, errors.New("state.json 损坏;如需重置请删除后重启(会生成新实例 ID)")
	}
	if s.Ifaces == nil {
		s.Ifaces = map[string]IfaceState{}
	}
	if s.SiteOffsets == nil {
		s.SiteOffsets = map[string]LogOffset{}
	}
	if s.Netbird == nil {
		s.Netbird = map[string]PeerState{}
	}
	if s.Xray == nil {
		s.Xray = map[string]uint64{}
	}
	if s.NextSeq < 1 {
		s.NextSeq = 1
	}
	return s, nil
}

func (s *State) Save(dir string) error {
	b, err := json.Marshal(s)
	if err != nil {
		return err
	}
	return writeFileAtomic(statePath(dir), b)
}
