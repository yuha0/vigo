package tv

import (
	"context"
	"fmt"
	"slices"
	"strconv"

	corev1 "github.com/yuha0/vigo/gen/go/vigo/core/v1"
	"github.com/yuha0/vigo/internal/errs"
	"github.com/yuha0/vigo/internal/samsung"
)

// settingFromTV maps one get_artmode_settings item: min/max make a range,
// valid values an enum, and a bare "on"/"off" value a toggle (docs/tv-api.md §5
// "Art-mode settings schema").
func settingFromTV(a samsung.ArtSetting) (*corev1.Setting, error) {
	switch {
	case a.Min != "" || a.Max != "":
		v, err1 := strconv.Atoi(a.Value)
		lo, err2 := strconv.Atoi(a.Min)
		hi, err3 := strconv.Atoi(a.Max)
		if err1 != nil || err2 != nil || err3 != nil {
			return nil, fmt.Errorf("setting %s: non-integer range %q in %q..%q", a.Item, a.Value, a.Min, a.Max)
		}
		return &corev1.Setting{Key: a.Item, Kind: &corev1.Setting_Range{Range: &corev1.RangeSetting{
			Value: int32(v), Min: int32(lo), Max: int32(hi),
		}}}, nil
	case len(a.ValidValues) > 0:
		return &corev1.Setting{Key: a.Item, Kind: &corev1.Setting_Enum{Enum: &corev1.EnumSetting{
			Value: a.Value, Options: slices.Clone(a.ValidValues),
		}}}, nil
	case a.Value == "on" || a.Value == "off":
		return &corev1.Setting{Key: a.Item, Kind: &corev1.Setting_Toggle{Toggle: &corev1.ToggleSetting{
			Value: a.Value == "on",
		}}}, nil
	}
	return nil, fmt.Errorf("setting %s: unrecognised shape (value %q)", a.Item, a.Value)
}

// settingsFromTV maps every item it understands. Unrecognised items are
// counted as protocol errors and left out.
func (s *Service) settingsFromTV(items []samsung.ArtSetting) []*corev1.Setting {
	out := make([]*corev1.Setting, 0, len(items))
	for _, it := range items {
		st, err := settingFromTV(it)
		if err != nil {
			s.m.ProtocolErrors.WithLabelValues("setting_shape").Inc()
			s.log.Error("unexpected art-mode setting", "err", err)
			continue
		}
		out = append(out, st)
	}
	return out
}

func (s *Service) readSettings(ctx context.Context, conn samsung.ArtConn) ([]samsung.ArtSetting, error) {
	return call(s, ctx, "get_artmode_settings", s.opts.RequestTimeout, conn.Settings)
}

func (s *Service) ListSettings(ctx context.Context) ([]*corev1.Setting, error) {
	conn, err := s.artConn()
	if err != nil {
		return nil, err
	}
	items, err := s.readSettings(ctx, conn)
	if err != nil {
		return nil, err
	}
	return s.settingsFromTV(items), nil
}

// wireValue validates req against the setting's current shape and returns
// the string the TV expects.
func wireValue(cur *corev1.Setting, req *corev1.UpdateSettingRequest) (string, error) {
	switch k := cur.Kind.(type) {
	case *corev1.Setting_Range:
		v, ok := req.Value.(*corev1.UpdateSettingRequest_IntValue)
		if !ok {
			return "", errs.New(ErrInvalidArgument, "%s is a range; set int_value", cur.Key)
		}
		if v.IntValue < k.Range.Min || v.IntValue > k.Range.Max {
			return "", errs.New(ErrInvalidArgument, "%s must be between %d and %d", cur.Key, k.Range.Min, k.Range.Max)
		}
		return strconv.Itoa(int(v.IntValue)), nil
	case *corev1.Setting_Enum:
		v, ok := req.Value.(*corev1.UpdateSettingRequest_EnumValue)
		if !ok {
			return "", errs.New(ErrInvalidArgument, "%s is an enum; set enum_value", cur.Key)
		}
		if !slices.Contains(k.Enum.Options, v.EnumValue) {
			return "", errs.New(ErrInvalidArgument, "%s must be one of %v", cur.Key, k.Enum.Options)
		}
		return v.EnumValue, nil
	case *corev1.Setting_Toggle:
		v, ok := req.Value.(*corev1.UpdateSettingRequest_BoolValue)
		if !ok {
			return "", errs.New(ErrInvalidArgument, "%s is a toggle; set bool_value", cur.Key)
		}
		if v.BoolValue {
			return "on", nil
		}
		return "off", nil
	}
	return "", errs.New(ErrInvalidArgument, "%s has no known type", cur.Key)
}

// UpdateSetting sends set_<key> and re-reads the settings: the TV
// acknowledges setters even when the value does not change (docs/tv-api.md §5
// "Three gotchas"). confirmed reports whether the re-read value matches.
func (s *Service) UpdateSetting(ctx context.Context, req *corev1.UpdateSettingRequest) (setting *corev1.Setting, confirmed bool, err error) {
	if req.GetKey() == "" {
		return nil, false, errs.New(ErrInvalidArgument, "key is required")
	}
	if req.Value == nil {
		return nil, false, errs.New(ErrInvalidArgument, "a value is required")
	}
	conn, err := s.artConn()
	if err != nil {
		return nil, false, err
	}
	items, err := s.readSettings(ctx, conn)
	if err != nil {
		return nil, false, err
	}
	cur := findSetting(s.settingsFromTV(items), req.Key)
	if cur == nil {
		return nil, false, errs.New(ErrNotFound, "no setting %q", req.Key)
	}
	value, err := wireValue(cur, req)
	if err != nil {
		return nil, false, err
	}
	// The key is one the TV listed, so the request name is bounded.
	name := "set_" + req.Key
	if err := do(s, ctx, name, s.opts.RequestTimeout, func(ctx context.Context) error {
		return conn.SetSetting(ctx, req.Key, value)
	}); err != nil {
		return nil, false, err
	}

	items, err = s.readSettings(ctx, conn)
	if err != nil {
		return nil, false, fmt.Errorf("re-read settings: %w", err)
	}
	i := slices.IndexFunc(items, func(a samsung.ArtSetting) bool { return a.Item == req.Key })
	if i < 0 {
		return nil, false, fmt.Errorf("re-read settings: %s disappeared", req.Key)
	}
	after, err := settingFromTV(items[i])
	if err != nil {
		s.m.ProtocolErrors.WithLabelValues("setting_shape").Inc()
		return nil, false, err
	}
	confirmed = items[i].Value == value
	if !confirmed {
		s.m.SettingUnconfirmed.WithLabelValues(req.Key).Inc()
		s.log.Warn("setting not confirmed", "key", req.Key, "sent", value, "read_back", items[i].Value)
	}
	s.emit(&corev1.WatchEventsResponse{Event: &corev1.WatchEventsResponse_SettingChanged{SettingChanged: after}})
	return after, confirmed, nil
}

func findSetting(settings []*corev1.Setting, key string) *corev1.Setting {
	for _, st := range settings {
		if st.Key == key {
			return st
		}
	}
	return nil
}
