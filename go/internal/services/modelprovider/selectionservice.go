// selectionservice.go: the IModelSelectionService front (the TS
// createModelSelectionService) + the configured-default repository entry
// (NodeModelSelectionConfigRepository).
package modelprovider

import (
	"context"
	"encoding/json"
	"errors"
	"sync"

	"github.com/tylinux/nex/go/internal/providerconfig"
)

// ---------------------------------------------------------------------------
// ModelSelectionService
// ---------------------------------------------------------------------------

// ModelSelectionService ports createModelSelectionService output.
type ModelSelectionService struct {
	runtime      *ProviderRuntime
	listenersMu  sync.Mutex
	listeners    []func(*SelectionView)
	disposed     bool
	stopRegistry func()
}

// GetView ports getView: reads the configured default from the personal file,
// then builds the view (the revision guard keeps the last published one).
func (s *ModelSelectionService) GetView(ctx context.Context, input *providerconfig.ModelSelection) (*SelectionView, error) {
	if s.disposed {
		return nil, errors.New("ModelSelectionService 已 dispose")
	}
	if err := s.runtime.Start(ctx); err != nil {
		return nil, err
	}
	if s.disposed {
		return nil, errors.New("ModelSelectionService 已 dispose")
	}
	personal, err := s.runtime.PersonalRepo.Read(ctx)
	if err != nil {
		return nil, err
	}
	if s.disposed {
		return nil, errors.New("ModelSelectionService 已 dispose")
	}
	return BuildSelectionView(s.runtime.RegistryService, personal.DefaultModelSelection, input)
}

// OnDidChange registers a view listener wired to the registry change events.
func (s *ModelSelectionService) OnDidChange(listener func(*SelectionView)) func() {
	s.listenersMu.Lock()
	defer s.listenersMu.Unlock()
	s.listeners = append(s.listeners, listener)
	if s.stopRegistry == nil {
		s.stopRegistry = s.runtime.RegistryService.OnDidChange(func(ChangedEvent) {
			s.emit()
		})
	}
	return func() {
		s.listenersMu.Lock()
		defer s.listenersMu.Unlock()
		for i, candidate := range s.listeners {
			if &candidate == &listener {
				s.listeners = append(s.listeners[:i], s.listeners[i+1:]...)
				return
			}
		}
	}
}

func (s *ModelSelectionService) emit() {
	if s.disposed {
		return
	}
	view, err := s.GetView(context.Background(), nil)
	if err != nil {
		// Registry 事件触发的异步 View 重建没有 owner；dispose 是明确的取消边界.
		return
	}
	s.listenersMu.Lock()
	listeners := append([]func(*SelectionView){}, s.listeners...)
	s.listenersMu.Unlock()
	for _, listener := range listeners {
		listener(view)
	}
}

// Dispose ports dispose.
func (s *ModelSelectionService) Dispose() {
	if s.disposed {
		return
	}
	s.disposed = true
	if s.stopRegistry != nil {
		s.stopRegistry()
	}
	s.listenersMu.Lock()
	s.listeners = nil
	s.listenersMu.Unlock()
}

// SaveConfiguredDefault ports NodeModelSelectionConfigRepository.saveConfiguredDefault:
// 默认选择只是 Personal 文件的一个字段；IO、锁和失效通知由同一个 Repository 拥有.
func (s *ModelSelectionService) SaveConfiguredDefault(ctx context.Context, selection *providerconfig.ModelSelection) (*providerconfig.ModelSelection, error) {
	if err := s.runtime.Start(ctx); err != nil {
		return nil, err
	}
	snapshot, err := s.runtime.PersonalRepo.Update(ctx, func(current *providerconfig.LayerSnapshot) (*providerconfig.LayerUpdate, error) {
		return &providerconfig.LayerUpdate{
			Providers: current.Providers, Models: current.Models,
			ProviderOrder: current.ProviderOrder, DefaultModelSelection: selection,
		}, nil
	})
	if err != nil {
		return nil, err
	}
	return snapshot.DefaultModelSelection, nil
}

// rpcResult is the map form of an RPC value.
type rpcResult map[string]any

func rawOf(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("null")
	}
	return json.RawMessage(data)
}

func issuesOf(issues []providerconfig.ValidationIssue) []providerconfig.ValidationIssue {
	if issues == nil {
		return []providerconfig.ValidationIssue{}
	}
	return issues
}

func orEmptyMap(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}
