package coreapi

import (
	"context"
	"errors"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	corev1 "github.com/yuha0/vigo/gen/go/vigo/core/v1"
	"github.com/yuha0/vigo/gen/go/vigo/core/v1/corev1connect"
	"github.com/yuha0/vigo/internal/samsung"
	"github.com/yuha0/vigo/internal/tv"
)

type handlers struct {
	tv *tv.Service
}

var (
	_ corev1connect.TVServiceHandler      = (*handlers)(nil)
	_ corev1connect.ArtServiceHandler     = (*handlers)(nil)
	_ corev1connect.PairingServiceHandler = (*handlers)(nil)
	_ corev1connect.ConfigServiceHandler  = (*handlers)(nil)
)

func (h *handlers) GetStatus(ctx context.Context, _ *connect.Request[corev1.GetStatusRequest]) (*connect.Response[corev1.GetStatusResponse], error) {
	return connect.NewResponse(&corev1.GetStatusResponse{Status: h.tv.Status()}), nil
}

func (h *handlers) Refresh(ctx context.Context, _ *connect.Request[corev1.RefreshRequest]) (*connect.Response[corev1.RefreshResponse], error) {
	st, err := h.tv.Refresh(ctx)
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&corev1.RefreshResponse{Status: st}), nil
}

// watchHeartbeat is how long a WatchEvents stream may stay silent before the
// current Status is sent again. Proxies close streams that carry no data for a
// while (Envoy's default stream idle timeout is 5 minutes).
var watchHeartbeat = time.Minute

func (h *handlers) WatchEvents(ctx context.Context, _ *connect.Request[corev1.WatchEventsRequest], stream *connect.ServerStream[corev1.WatchEventsResponse]) error {
	first, events, cancel := h.tv.Subscribe()
	defer cancel()
	err := stream.Send(&corev1.WatchEventsResponse{
		Time:  timestamppb.Now(),
		Event: &corev1.WatchEventsResponse_Status{Status: first},
	})
	if err != nil {
		return err
	}
	beat := time.NewTicker(watchHeartbeat)
	defer beat.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-events:
			if !ok {
				return nil
			}
			if err := stream.Send(ev); err != nil {
				return err
			}
			beat.Reset(watchHeartbeat)
		case <-beat.C:
			err := stream.Send(&corev1.WatchEventsResponse{
				Time:  timestamppb.Now(),
				Event: &corev1.WatchEventsResponse_Status{Status: h.tv.Status()},
			})
			if err != nil {
				return err
			}
		}
	}
}

func (h *handlers) ShowArt(ctx context.Context, req *connect.Request[corev1.ShowArtRequest]) (*connect.Response[corev1.ShowArtResponse], error) {
	st, err := h.tv.ShowArt(ctx, req.Msg.ContentId)
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&corev1.ShowArtResponse{Status: st}), nil
}

func (h *handlers) SetArtMode(ctx context.Context, req *connect.Request[corev1.SetArtModeRequest]) (*connect.Response[corev1.SetArtModeResponse], error) {
	st, err := h.tv.SetArtMode(ctx, req.Msg.On)
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&corev1.SetArtModeResponse{Status: st}), nil
}

func (h *handlers) ListSettings(ctx context.Context, _ *connect.Request[corev1.ListSettingsRequest]) (*connect.Response[corev1.ListSettingsResponse], error) {
	settings, err := h.tv.ListSettings(ctx)
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&corev1.ListSettingsResponse{Settings: settings}), nil
}

func (h *handlers) UpdateSetting(ctx context.Context, req *connect.Request[corev1.UpdateSettingRequest]) (*connect.Response[corev1.UpdateSettingResponse], error) {
	setting, confirmed, err := h.tv.UpdateSetting(ctx, req.Msg)
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&corev1.UpdateSettingResponse{Setting: setting, Confirmed: confirmed}), nil
}

func (h *handlers) SendKey(ctx context.Context, req *connect.Request[corev1.SendKeyRequest]) (*connect.Response[corev1.SendKeyResponse], error) {
	var action samsung.KeyAction
	switch req.Msg.Action {
	case corev1.KeyAction_KEY_ACTION_UNSPECIFIED, corev1.KeyAction_KEY_ACTION_CLICK:
		action = samsung.KeyClick
	case corev1.KeyAction_KEY_ACTION_PRESS:
		action = samsung.KeyPress
	case corev1.KeyAction_KEY_ACTION_RELEASE:
		action = samsung.KeyRelease
	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("unknown key action"))
	}
	if err := h.tv.SendKey(ctx, req.Msg.Key, action); err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&corev1.SendKeyResponse{}), nil
}

func (h *handlers) ListArt(ctx context.Context, req *connect.Request[corev1.ListArtRequest]) (*connect.Response[corev1.ListArtResponse], error) {
	arts, cats, err := h.tv.ListArt(ctx, req.Msg.CategoryId)
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&corev1.ListArtResponse{Artworks: arts, CategoryIds: cats}), nil
}

func (h *handlers) SelectArt(ctx context.Context, req *connect.Request[corev1.SelectArtRequest]) (*connect.Response[corev1.SelectArtResponse], error) {
	st, err := h.tv.SelectArt(ctx, req.Msg.ContentId)
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&corev1.SelectArtResponse{Status: st}), nil
}

func (h *handlers) DeleteArt(ctx context.Context, req *connect.Request[corev1.DeleteArtRequest]) (*connect.Response[corev1.DeleteArtResponse], error) {
	deleted, err := h.tv.DeleteArt(ctx, req.Msg.ContentIds)
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&corev1.DeleteArtResponse{DeletedContentIds: deleted}), nil
}

func (h *handlers) ListMattes(ctx context.Context, _ *connect.Request[corev1.ListMattesRequest]) (*connect.Response[corev1.ListMattesResponse], error) {
	types, colors, err := h.tv.ListMattes(ctx)
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&corev1.ListMattesResponse{MatteTypes: types, Colors: colors}), nil
}

func (h *handlers) ChangeMatte(ctx context.Context, req *connect.Request[corev1.ChangeMatteRequest]) (*connect.Response[corev1.ChangeMatteResponse], error) {
	art, err := h.tv.ChangeMatte(ctx, req.Msg.ContentId, req.Msg.MatteId, req.Msg.PortraitMatteId)
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&corev1.ChangeMatteResponse{Artwork: art}), nil
}

func (h *handlers) GetPairing(ctx context.Context, _ *connect.Request[corev1.GetPairingRequest]) (*connect.Response[corev1.GetPairingResponse], error) {
	return connect.NewResponse(&corev1.GetPairingResponse{Pairing: h.tv.Pairing()}), nil
}

func (h *handlers) StartPairing(ctx context.Context, _ *connect.Request[corev1.StartPairingRequest]) (*connect.Response[corev1.StartPairingResponse], error) {
	p, err := h.tv.StartPairing(ctx)
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&corev1.StartPairingResponse{Pairing: p}), nil
}

func (h *handlers) CancelPairing(ctx context.Context, _ *connect.Request[corev1.CancelPairingRequest]) (*connect.Response[corev1.CancelPairingResponse], error) {
	return connect.NewResponse(&corev1.CancelPairingResponse{Pairing: h.tv.CancelPairing()}), nil
}

func configToProto(c tv.Config) *corev1.Config {
	return &corev1.Config{TvHost: c.Host, TvPort: int32(c.Port), ClientName: c.ClientName}
}

func (h *handlers) GetConfig(ctx context.Context, _ *connect.Request[corev1.GetConfigRequest]) (*connect.Response[corev1.GetConfigResponse], error) {
	return connect.NewResponse(&corev1.GetConfigResponse{Config: configToProto(h.tv.Config())}), nil
}

func (h *handlers) UpdateConfig(ctx context.Context, req *connect.Request[corev1.UpdateConfigRequest]) (*connect.Response[corev1.UpdateConfigResponse], error) {
	c := req.Msg.Config
	if c == nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("config is required"))
	}
	cfg, err := h.tv.UpdateConfig(ctx, tv.Config{Host: c.TvHost, Port: int(c.TvPort), ClientName: c.ClientName})
	if err != nil {
		return nil, toConnect(err)
	}
	return connect.NewResponse(&corev1.UpdateConfigResponse{Config: configToProto(cfg)}), nil
}
