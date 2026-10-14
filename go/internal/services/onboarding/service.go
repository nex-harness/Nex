// Package onboarding ports packages/services/src/onboarding/* (step 3.5):
// IOnboardingRecordService over {appConfigDir}/onboarding-record.json.
// 记录是设备级数据，必须跟随 dataBaseDir（与 telemetry-state.json 一致），
// 不能学 setting.json 固定写 home。
package onboarding

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/tylinux/nex/go/internal/adapters/fsx"
	"github.com/tylinux/nex/go/internal/adapters/logx"
	"github.com/tylinux/nex/go/internal/services/paths"
)

// ChannelName is ServiceChannels.OnboardingRecord.
const ChannelName = "onboarding-record"

// Entry is OnboardingRecordEntry.
type Entry struct {
	UserID                      any    `json:"userId"` // string | null
	Occupation                  any    `json:"occupation"`
	InterfaceMode               any    `json:"interfaceMode"`
	MemoryEnabled               any    `json:"memoryEnabled"`
	ProactiveSuggestionsEnabled any    `json:"proactiveSuggestionsEnabled"`
	CompletedAt                 string `json:"completedAt"`
	UploadState                 string `json:"uploadState"`
}

// Decision is OnboardingDecision.
type Decision struct {
	UserID    any    `json:"userId"`
	Status    string `json:"status"`
	Reason    string `json:"reason"`
	DecidedAt string `json:"decidedAt"`
}

// RecordFile is OnboardingRecordFile (v2 shape after the v1 transform).
type RecordFile struct {
	Version   int64      `json:"version"`
	DeviceMid string     `json:"deviceMid"`
	Entries   []Entry    `json:"entries"`
	Decisions []Decision `json:"decisions"`
}

// Options configures NewService.
type Options struct {
	// RecordFile: the record path. Empty resolves {appConfigDir}/onboarding-record.json.
	RecordFile string
	// Paths owns the app-config-dir resolution.
	Paths *paths.Paths
	// Logger; nil discards.
	Logger logx.ServiceLogger
	// Now for decidedAt timestamps; nil uses time.Now.
	Now func() time.Time
}

// Service is IOnboardingRecordService.
type Service struct {
	file   string
	logger logx.ServiceLogger
	now    func() time.Time
	// writeQueue serializes writes: 引导保存与并发触发判定同时发生时不丢条目.
	writeMu sync.Mutex
}

// NewService builds the service.
func NewService(opts Options) *Service {
	if opts.RecordFile == "" {
		p := opts.Paths
		if p == nil {
			p = paths.NewFromProcess()
		}
		opts.RecordFile = filepath.Join(p.AppConfigDir(), "onboarding-record.json")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Service{file: opts.RecordFile, logger: opts.Logger, now: opts.Now}
}

// RecordFile exposes the path (tests).
func (s *Service) RecordFilePath() string { return s.file }

// readRecordFile ports readRecordFile: missing or corrupt → nil (损坏文件等价
// 于"从未记录"，重新触发引导后在下次 append 时重建).
func (s *Service) readRecordFile() (*RecordFile, error) {
	data, err := os.ReadFile(s.file)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		s.warn("read onboarding record failed: %v", err)
		return nil, nil
	}
	var probe any
	if err := json.Unmarshal(data, &probe); err != nil {
		s.warn("invalid onboarding record json, treating as missing. error: %v", err)
		return nil, nil
	}
	obj, ok := probe.(map[string]any)
	if !ok {
		s.warn("invalid onboarding record json, treating as missing. error: not an object")
		return nil, nil
	}
	file, err := parseRecordFile(obj)
	if err != nil {
		s.warn("invalid onboarding record json, treating as missing. error: %v", err)
		return nil, nil
	}
	return file, nil
}

// AppendRecord ports appendRecord. deviceMid 以文件内已有值为权威.
func (s *Service) AppendRecord(ctx context.Context, deviceMid string, entry Entry) error {
	return s.enqueueWrite(func() error {
		existing, err := s.readRecordFile()
		if err != nil {
			return err
		}
		var file *RecordFile
		if existing != nil {
			if existing.DeviceMid != deviceMid {
				s.warn("deviceMid mismatch, keep existing: %s incoming: %s", existing.DeviceMid, deviceMid)
			}
			file = existing
		} else {
			file = &RecordFile{Version: 2, DeviceMid: deviceMid, Entries: []Entry{}, Decisions: []Decision{}}
		}
		record := Entry{
			UserID:                      nil, // Nex 没有账号体系，记录固定归属匿名身份
			Occupation:                  entry.Occupation,
			InterfaceMode:               entry.InterfaceMode,
			MemoryEnabled:               entry.MemoryEnabled,
			ProactiveSuggestionsEnabled: entry.ProactiveSuggestionsEnabled,
			CompletedAt:                 entry.CompletedAt,
			UploadState:                 "pending",
		}
		if _, err := parseEntry(entryToMap(record)); err != nil {
			return err
		}
		// 每 userId（含 null）至多一条：重复完成引导覆盖旧条目并重新置 pending.
		replaced := false
		for i := range file.Entries {
			if file.Entries[i].UserID == nil {
				file.Entries[i] = record
				replaced = true
				break
			}
		}
		if !replaced {
			file.Entries = append(file.Entries, record)
		}
		decisions := make([]Decision, 0, len(file.Decisions))
		for _, d := range file.Decisions {
			if d.UserID != nil {
				decisions = append(decisions, d)
			}
		}
		file.Decisions = decisions
		return s.writeRecordFile(file)
	})
}

// DismissOnboarding ports dismissOnboarding: 已有作答时为空操作.
func (s *Service) DismissOnboarding(ctx context.Context, deviceMid string) error {
	return s.enqueueWrite(func() error {
		file, err := s.readRecordFile()
		if err != nil {
			return err
		}
		if file == nil {
			file = &RecordFile{Version: 2, DeviceMid: deviceMid, Entries: []Entry{}, Decisions: []Decision{}}
		}
		for _, entry := range file.Entries {
			if entry.UserID == nil {
				return nil
			}
		}
		decision := Decision{
			UserID:    nil,
			Status:    "dismissed",
			Reason:    "user_closed",
			DecidedAt: s.now().UTC().Format(time.RFC3339Nano),
		}
		replaced := false
		for i := range file.Decisions {
			if file.Decisions[i].UserID == nil {
				file.Decisions[i] = decision
				replaced = true
				break
			}
		}
		if !replaced {
			file.Decisions = append(file.Decisions, decision)
		}
		return s.writeRecordFile(file)
	})
}

// GetLatestEntry ports getLatestEntry.
func (s *Service) GetLatestEntry(ctx context.Context) (*Entry, error) {
	file, err := s.readRecordFile()
	if err != nil || file == nil {
		return nil, err
	}
	var latest *Entry
	for i := range file.Entries {
		if file.Entries[i].UserID == nil {
			latest = &file.Entries[i]
		}
	}
	return latest, nil
}

// UpdateRecordPreferences ports updateRecordPreferences: 当前用户无条目时忽略.
func (s *Service) UpdateRecordPreferences(ctx context.Context, patch PreferencesPatch) error {
	return s.enqueueWrite(func() error {
		file, err := s.readRecordFile()
		if err != nil {
			return err
		}
		if file == nil {
			return nil
		}
		index := -1
		for i := range file.Entries {
			if file.Entries[i].UserID == nil {
				index = i
			}
		}
		if index < 0 {
			return nil
		}
		if patch.MemoryEnabledSet {
			file.Entries[index].MemoryEnabled = patch.MemoryEnabled
		}
		if patch.ProactiveSuggestionsEnabledSet {
			file.Entries[index].ProactiveSuggestionsEnabled = patch.ProactiveSuggestionsEnabled
		}
		if _, err := parseEntry(entryToMap(file.Entries[index])); err != nil {
			return err
		}
		return s.writeRecordFile(file)
	})
}

// PreferencesPatch is the updateRecordPreferences patch (tri-state fields).
type PreferencesPatch struct {
	MemoryEnabled                  any
	MemoryEnabledSet               bool
	ProactiveSuggestionsEnabled    any
	ProactiveSuggestionsEnabledSet bool
}

// GetRecords ports getRecords.
func (s *Service) GetRecords(ctx context.Context) (*RecordFile, error) {
	return s.readRecordFile()
}

// ClearRecords ports clearRecords.
func (s *Service) ClearRecords(ctx context.Context) error {
	return s.enqueueWrite(func() error {
		return os.Remove(s.file)
	})
}

// enqueueWrite serializes writes (the TS writeQueue promise chain).
func (s *Service) enqueueWrite(task func() error) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	return task()
}

func (s *Service) writeRecordFile(file *RecordFile) error {
	if err := os.MkdirAll(filepath.Dir(s.file), 0o777); err != nil {
		return err
	}
	data, err := marshalRecordFile(file)
	if err != nil {
		return err
	}
	return fsx.AtomicWriteText(context.Background(), s.file, string(data), nil)
}
