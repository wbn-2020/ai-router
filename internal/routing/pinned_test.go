package routing

import (
	"context"
	"errors"
	"testing"

	"ai-router/internal/store"
)

func TestAcquirePinnedOnlyReturnsRequestedChannel(t *testing.T) {
	ctx := context.Background()
	s := newRoutingTestStore(t)
	first := createRoutingChannel(t, s.Channels(), "first", store.ChannelProtocolOpenAIResponses, 0, 2, true)
	second := createRoutingChannel(t, s.Channels(), "second", store.ChannelProtocolOpenAIResponses, 1, 2, true)
	createRoutingRoute(t, s.ModelRoutes(), "m", first.ID, 0, true)
	createRoutingRoute(t, s.ModelRoutes(), "m", second.ID, 1, true)
	scheduler := newStoreScheduler(t, s, Options{})
	candidate, slot, err := scheduler.AcquirePinned(ctx, second.ID, "m", store.ChannelProtocolOpenAIResponses)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Channel.ID != second.ID {
		t.Fatalf("channel = %d", candidate.Channel.ID)
	}
	scheduler.RecordSuccess(candidate)
	slot.Release()

	if _, _, err := scheduler.AcquirePinned(ctx, 999, "m", store.ChannelProtocolOpenAIResponses); !errors.Is(err, ErrNoCandidates) {
		t.Fatalf("unknown channel err = %v", err)
	}
}

func TestAcquirePinnedRejectsDisabledOrWrongProtocol(t *testing.T) {
	ctx := context.Background()
	s := newRoutingTestStore(t)
	responses := createRoutingChannel(t, s.Channels(), "resp", store.ChannelProtocolOpenAIResponses, 0, 1, true)
	chat := createRoutingChannel(t, s.Channels(), "chat", store.ChannelProtocolOpenAIChat, 0, 1, true)
	createRoutingRoute(t, s.ModelRoutes(), "m", responses.ID, 0, true)
	createRoutingRoute(t, s.ModelRoutes(), "m2", chat.ID, 0, true)
	scheduler := newStoreScheduler(t, s, Options{})

	if _, _, err := scheduler.AcquirePinned(ctx, chat.ID, "m", store.ChannelProtocolOpenAIResponses); !errors.Is(err, ErrNoCandidates) {
		t.Fatalf("wrong protocol err = %v", err)
	}
	if _, err := s.Channels().SetEnabled(ctx, responses.ID, false); err != nil {
		t.Fatal(err)
	}
	if _, _, err := scheduler.AcquirePinned(ctx, responses.ID, "m", store.ChannelProtocolOpenAIResponses); !errors.Is(err, ErrNoCandidates) {
		t.Fatalf("disabled err = %v", err)
	}
}

func TestAcquirePinnedCapacityFailsFast(t *testing.T) {
	ctx := context.Background()
	s := newRoutingTestStore(t)
	channel := createRoutingChannel(t, s.Channels(), "one", store.ChannelProtocolOpenAIResponses, 0, 1, true)
	createRoutingRoute(t, s.ModelRoutes(), "m", channel.ID, 0, true)
	scheduler := newStoreScheduler(t, s, Options{CapacityWait: 0, DisableCapacityWait: true})

	candidate, slot, err := scheduler.AcquirePinned(ctx, channel.ID, "m", store.ChannelProtocolOpenAIResponses)
	if err != nil {
		t.Fatal(err)
	}
	defer slot.Release()
	_ = candidate
	if _, _, err := scheduler.AcquirePinned(ctx, channel.ID, "m", store.ChannelProtocolOpenAIResponses); !errors.Is(err, ErrChannelCapacity) {
		t.Fatalf("capacity err = %v", err)
	}
}
